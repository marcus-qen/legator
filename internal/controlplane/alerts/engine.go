package alerts

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/marcus-qen/legator/internal/controlplane/events"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/webhook"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

// Notifier is the webhook dispatcher contract used by the alerts engine.
type Notifier interface {
	Notify(event, probeID, summary string, detail any)
	List() []webhook.WebhookConfig
}

// Engine evaluates alert rules and delivers notifications.
type Engine struct {
	store        *Store
	routingStore *RoutingStore
	fleet        fleet.Fleet
	notifier     Notifier
	bus          *events.Bus
	logger       *zap.Logger

	evalMu sync.Mutex

	firing  map[FiringKey]*AlertEvent
	pending map[FiringKey]time.Time

	runMu  sync.Mutex
	ticker *time.Ticker
	stopCh chan struct{}
	subID  string
	subCh  <-chan events.Event
}

// NewEngine creates an alert evaluation engine.
func NewEngine(store *Store, fleetMgr fleet.Fleet, notifier Notifier, bus *events.Bus, logger *zap.Logger) *Engine {
	if logger == nil {
		logger = zap.NewNop()
	}
	e := &Engine{
		store:    store,
		fleet:    fleetMgr,
		notifier: notifier,
		bus:      bus,
		logger:   logger,
		firing:   make(map[FiringKey]*AlertEvent),
		pending:  make(map[FiringKey]time.Time),
	}

	if store != nil {
		for _, evt := range store.ActiveAlerts() {
			evtCopy := evt
			e.firing[FiringKey{RuleID: evt.RuleID, ProbeID: evt.ProbeID}] = &evtCopy
		}
	}

	return e
}

// SetRoutingStore attaches an optional routing store to the engine.
// When set, each alert delivery is enriched with a resolved RoutingOutcome.
// This method is safe to call before Start(); it is additive and does not
// affect the core alert evaluation behaviour.
func (e *Engine) SetRoutingStore(rs *RoutingStore) {
	e.routingStore = rs
}

// Start begins periodic rule evaluation.
func (e *Engine) Start() {
	e.runMu.Lock()
	defer e.runMu.Unlock()

	if e.ticker != nil {
		return
	}

	e.stopCh = make(chan struct{})
	e.ticker = time.NewTicker(30 * time.Second)
	if e.bus != nil {
		e.subID = "alerts-engine-" + uuid.NewString()
		e.subCh = e.bus.Subscribe(e.subID)
	}

	stopCh := e.stopCh
	tickCh := e.ticker.C
	subCh := e.subCh

	go e.loop(stopCh, tickCh, subCh)
	go e.safeEvaluate("startup")
}

// Stop stops periodic evaluation and unsubscribes from the event bus.
func (e *Engine) Stop() {
	e.runMu.Lock()
	defer e.runMu.Unlock()

	if e.ticker == nil {
		return
	}

	e.ticker.Stop()
	close(e.stopCh)
	e.ticker = nil
	e.stopCh = nil

	if e.bus != nil && e.subID != "" {
		e.bus.Unsubscribe(e.subID)
	}
	e.subID = ""
	e.subCh = nil
}

func (e *Engine) loop(stopCh <-chan struct{}, tickCh <-chan time.Time, subCh <-chan events.Event) {
	for {
		select {
		case <-stopCh:
			return
		case <-tickCh:
			e.safeEvaluate("ticker")
		case evt, ok := <-subCh:
			if !ok {
				return
			}
			if evt.Type == events.ProbeDisconnected {
				e.safeEvaluate("probe.disconnected")
			}
		}
	}
}

func (e *Engine) safeEvaluate(trigger string) {
	if err := e.Evaluate(); err != nil {
		e.logger.Warn("alert evaluation failed", zap.String("trigger", trigger), zap.Error(err))
	}
}

// Evaluate runs one full evaluation pass over all enabled rules.
func (e *Engine) Evaluate() error {
	e.evalMu.Lock()
	defer e.evalMu.Unlock()

	if e.store == nil || e.fleet == nil {
		return nil
	}

	rules, err := e.store.ListRules()
	if err != nil {
		return err
	}
	probes := e.fleet.List()
	now := time.Now().UTC()

	enabledRules := make(map[string]AlertRule)
	matched := make(map[FiringKey]ruleMatch)

	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		enabledRules[rule.ID] = rule

		dur, err := parseRuleDuration(rule.Condition.Duration)
		if err != nil {
			e.logger.Warn("invalid alert rule duration; skipping rule", zap.String("rule_id", rule.ID), zap.String("duration", rule.Condition.Duration), zap.Error(err))
			continue
		}

		for _, probe := range probes {
			if probe == nil {
				continue
			}
			if !matchTags(probe.Tags, rule.Condition.Tags) {
				continue
			}

			key := FiringKey{RuleID: rule.ID, ProbeID: probe.ID}
			ok, message := e.conditionMet(rule, probe, now)
			if !ok {
				delete(e.pending, key)
				continue
			}

			if rule.Condition.Type != "probe_offline" && dur > 0 {
				since, exists := e.pending[key]
				if !exists {
					e.pending[key] = now
					continue
				}
				if now.Sub(since) < dur {
					continue
				}
			}

			matched[key] = ruleMatch{rule: rule, message: message}
			delete(e.pending, key)

			if _, already := e.firing[key]; already {
				continue
			}

			evt := AlertEvent{
				ID:       uuid.NewString(),
				RuleID:   rule.ID,
				RuleName: rule.Name,
				ProbeID:  probe.ID,
				Status:   "firing",
				Message:  message,
				FiredAt:  now,
			}
			if err := e.store.RecordEvent(evt); err != nil {
				e.logger.Warn("failed to persist firing alert event", zap.String("rule_id", rule.ID), zap.String("probe_id", probe.ID), zap.Error(err))
				continue
			}
			evtCopy := evt
			e.firing[key] = &evtCopy
			e.deliver(rule, evtCopy, events.AlertFired)
		}
	}

	for key, evt := range e.firing {
		if _, stillMatched := matched[key]; stillMatched {
			continue
		}

		resolvedAt := now
		resolved := *evt
		resolved.Status = "resolved"
		resolved.ResolvedAt = &resolvedAt
		resolved.Message = fmt.Sprintf("Alert resolved for probe %s", key.ProbeID)

		if err := e.store.RecordEvent(resolved); err != nil {
			e.logger.Warn("failed to persist resolved alert event", zap.String("rule_id", key.RuleID), zap.String("probe_id", key.ProbeID), zap.Error(err))
			continue
		}

		rule, ok := enabledRules[key.RuleID]
		if !ok {
			rule = AlertRule{ID: resolved.RuleID, Name: resolved.RuleName}
		}
		e.deliver(rule, resolved, events.AlertResolved)
		delete(e.firing, key)
		delete(e.pending, key)
	}

	for key := range e.pending {
		if _, ok := enabledRules[key.RuleID]; !ok {
			delete(e.pending, key)
		}
	}

	return nil
}

type ruleMatch struct {
	rule    AlertRule
	message string
}

func (e *Engine) conditionMet(rule AlertRule, probe *fleet.ProbeState, now time.Time) (bool, string) {
	switch rule.Condition.Type {
	case "probe_offline":
		if probe.Status != "offline" {
			return false, ""
		}
		dur, err := parseRuleDuration(rule.Condition.Duration)
		if err != nil {
			return false, ""
		}
		offlineFor := now.Sub(probe.LastSeen)
		if dur > 0 && offlineFor < dur {
			return false, ""
		}
		return true, fmt.Sprintf("Probe %s has been offline for %s", probe.ID, offlineFor.Round(time.Second))
	case "disk_threshold":
		hb := lastHeartbeat(probe)
		if hb == nil || hb.DiskTotal == 0 {
			return false, ""
		}
		usage := (float64(hb.DiskUsed) / float64(hb.DiskTotal)) * 100
		if usage <= rule.Condition.Threshold {
			return false, ""
		}
		return true, fmt.Sprintf("Probe %s disk usage %.1f%% exceeds %.1f%%", probe.ID, usage, rule.Condition.Threshold)
	case "cpu_threshold":
		hb := lastHeartbeat(probe)
		if hb == nil {
			return false, ""
		}
		cpus := 1.0
		if probe.Inventory != nil && probe.Inventory.CPUs > 0 {
			cpus = float64(probe.Inventory.CPUs)
		}
		usage := (hb.Load[0] / cpus) * 100
		if usage <= rule.Condition.Threshold {
			return false, ""
		}
		return true, fmt.Sprintf("Probe %s CPU usage %.1f%% exceeds %.1f%%", probe.ID, usage, rule.Condition.Threshold)
	default:
		return false, ""
	}
}

func (e *Engine) deliver(rule AlertRule, evt AlertEvent, evtType events.EventType) {
	summary := fmt.Sprintf("[%s] %s", strings.ToUpper(evt.Status), evt.Message)

	if e.bus != nil {
		e.bus.Publish(events.Event{
			Type:      evtType,
			ProbeID:   evt.ProbeID,
			Summary:   summary,
			Detail:    evt,
			Timestamp: time.Now().UTC(),
		})
	}

	if e.notifier == nil {
		return
	}

	if len(rule.Actions) > 0 {
		available := make(map[string]struct{})
		for _, cfg := range e.notifier.List() {
			available[cfg.ID] = struct{}{}
		}

		hasTarget := false
		for _, action := range rule.Actions {
			if action.Type != "webhook" {
				continue
			}
			if _, ok := available[action.WebhookID]; ok {
				hasTarget = true
				break
			}
		}
		if !hasTarget {
			return
		}
	}

	e.notifier.Notify(string(evtType), evt.ProbeID, summary, evt)
}

func parseRuleDuration(raw string) (time.Duration, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	return time.ParseDuration(value)
}

func matchTags(probeTags, ruleTags []string) bool {
	if len(ruleTags) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(probeTags))
	for _, tag := range probeTags {
		set[strings.ToLower(strings.TrimSpace(tag))] = struct{}{}
	}

	for _, tag := range ruleTags {
		needle := strings.ToLower(strings.TrimSpace(tag))
		if needle == "" {
			continue
		}
		if _, ok := set[needle]; !ok {
			return false
		}
	}
	return true
}

func lastHeartbeat(probe *fleet.ProbeState) *protocol.HeartbeatPayload {
	if probe == nil {
		return nil
	}
	return probe.LastHeartbeat()
}

// SnapshotFiring returns current firing keys/events for tests and diagnostics.
func (e *Engine) SnapshotFiring() []AlertEvent {
	e.evalMu.Lock()
	defer e.evalMu.Unlock()

	out := make([]AlertEvent, 0, len(e.firing))
	for _, evt := range e.firing {
		out = append(out, *evt)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RuleID == out[j].RuleID {
			return out[i].ProbeID < out[j].ProbeID
		}
		return out[i].RuleID < out[j].RuleID
	})
	return out
}
