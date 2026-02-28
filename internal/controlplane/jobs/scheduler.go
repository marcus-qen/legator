package jobs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/protocol"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

const defaultCommandTimeout = 60 * time.Second

type trackable interface {
	Track(requestID, probeID, command string, level protocol.CapabilityLevel) *cmdtracker.PendingCommand
	Cancel(requestID string)
}

type sender interface {
	SendTo(probeID string, msgType protocol.MessageType, payload any) error
}

// Scheduler dispatches due jobs and records run history.
type Scheduler struct {
	store   *Store
	hub     sender
	fleet   fleet.Fleet
	tracker trackable
	logger  *zap.Logger

	mu            sync.Mutex
	cancel        context.CancelFunc
	ticker        *time.Ticker
	inFlight      map[string]string // request_id -> run_id
	requestTarget map[string]string // request_id -> jobID::probeID
	activeTargets map[string]struct{}
	wg            sync.WaitGroup
}

// NewScheduler creates a recurring job scheduler.
func NewScheduler(store *Store, hub sender, fleetMgr fleet.Fleet, tracker trackable, logger *zap.Logger) *Scheduler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Scheduler{
		store:         store,
		hub:           hub,
		fleet:         fleetMgr,
		tracker:       tracker,
		logger:        logger,
		inFlight:      make(map[string]string),
		requestTarget: make(map[string]string),
		activeTargets: make(map[string]struct{}),
	}
}

// Start starts the scheduler loop. It is safe to call Start multiple times.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.ticker != nil {
		s.mu.Unlock()
		return
	}

	loopCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.ticker = time.NewTicker(30 * time.Second)
	ticker := s.ticker
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runOnce(time.Now().UTC())
		for {
			select {
			case <-loopCtx.Done():
				return
			case now := <-ticker.C:
				s.runOnce(now.UTC())
			}
		}
	}()
}

// Stop stops background scheduling and cancels tracked requests.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if s.ticker == nil {
		s.mu.Unlock()
		return
	}

	s.ticker.Stop()
	s.ticker = nil
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	for requestID := range s.inFlight {
		s.tracker.Cancel(requestID)
	}
	s.inFlight = make(map[string]string)
	s.requestTarget = make(map[string]string)
	s.activeTargets = make(map[string]struct{})
	s.mu.Unlock()

	s.wg.Wait()
}

// TriggerNow executes a job immediately, regardless of schedule.
func (s *Scheduler) TriggerNow(jobID string) error {
	job, err := s.store.GetJob(jobID)
	if err != nil {
		return err
	}
	return s.dispatchJob(*job, time.Now().UTC())
}

func (s *Scheduler) runOnce(now time.Time) {
	if s.store == nil {
		return
	}

	jobs, err := s.store.ListJobs()
	if err != nil {
		s.logger.Warn("list jobs failed", zap.Error(err))
		return
	}

	for _, job := range jobs {
		if !job.Enabled {
			continue
		}
		due, err := isScheduleDue(job.Schedule, job.LastRunAt, job.CreatedAt, now)
		if err != nil {
			s.logger.Warn("invalid job schedule",
				zap.String("job_id", job.ID),
				zap.String("schedule", job.Schedule),
				zap.Error(err),
			)
			continue
		}
		if !due {
			continue
		}

		if err := s.dispatchJob(job, now); err != nil {
			s.logger.Warn("dispatch scheduled job failed", zap.String("job_id", job.ID), zap.Error(err))
		}
	}
}

func (s *Scheduler) dispatchJob(job Job, now time.Time) error {
	probeIDs := s.resolveTargets(job.Target)
	if len(probeIDs) == 0 {
		return fmt.Errorf("no probes resolved for target")
	}

	for _, probeID := range probeIDs {
		targetKey := inFlightTargetKey(job.ID, probeID)
		if !s.claimTarget(targetKey) {
			s.logger.Debug("skipping overlapping run for target", zap.String("job_id", job.ID), zap.String("probe_id", probeID))
			continue
		}

		if !s.probeOnline(probeID) {
			s.recordOfflineRun(job.ID, probeID, now)
			s.releaseTarget(targetKey)
			continue
		}

		requestID := fmt.Sprintf("job-%s-%d-%s", job.ID, now.UnixNano(), probeID)
		run, err := s.store.RecordRunStart(JobRun{
			JobID:     job.ID,
			ProbeID:   probeID,
			RequestID: requestID,
			StartedAt: now,
			Status:    RunStatusRunning,
		})
		if err != nil {
			s.releaseTarget(targetKey)
			s.logger.Warn("record run start failed", zap.String("job_id", job.ID), zap.String("probe_id", probeID), zap.Error(err))
			continue
		}

		payload := protocol.CommandPayload{
			RequestID: requestID,
			Command:   "/bin/sh",
			Args:      []string{"-lc", job.Command},
			Timeout:   defaultCommandTimeout,
			Level:     protocol.CapObserve,
		}

		pending := s.tracker.Track(requestID, probeID, job.Command, payload.Level)
		if pending == nil {
			s.completeRun(run.ID, RunStatusFailed, nil, "command tracking unavailable")
			s.releaseTarget(targetKey)
			continue
		}
		if err := s.hub.SendTo(probeID, protocol.MsgCommand, payload); err != nil {
			s.tracker.Cancel(requestID)
			s.completeRun(run.ID, RunStatusFailed, nil, "probe offline")
			s.releaseTarget(targetKey)
			continue
		}

		s.mu.Lock()
		s.inFlight[requestID] = run.ID
		s.requestTarget[requestID] = targetKey
		s.mu.Unlock()

		s.wg.Add(1)
		go s.awaitRunResult(run.ID, requestID, pending)
	}

	return nil
}

func (s *Scheduler) awaitRunResult(runID, requestID string, pending *cmdtracker.PendingCommand) {
	defer s.wg.Done()

	if pending == nil || pending.Result == nil {
		s.completeRun(runID, RunStatusFailed, nil, "command tracking unavailable")
		s.clearInFlight(requestID)
		return
	}

	result, ok := <-pending.Result
	if !ok || result == nil {
		s.completeRun(runID, RunStatusFailed, nil, "command canceled")
		s.clearInFlight(requestID)
		return
	}

	status := RunStatusSuccess
	if result.ExitCode != 0 {
		status = RunStatusFailed
	}
	exitCode := result.ExitCode
	output := formatResultOutput(result)
	s.completeRun(runID, status, &exitCode, output)
	s.clearInFlight(requestID)
}

func (s *Scheduler) completeRun(runID, status string, exitCode *int, output string) {
	if err := s.store.CompleteRun(runID, status, exitCode, output); err != nil {
		s.logger.Warn("complete run failed", zap.String("run_id", runID), zap.Error(err))
	}
}

func (s *Scheduler) clearInFlight(requestID string) {
	s.mu.Lock()
	if targetKey, ok := s.requestTarget[requestID]; ok {
		delete(s.requestTarget, requestID)
		delete(s.activeTargets, targetKey)
	}
	delete(s.inFlight, requestID)
	s.mu.Unlock()
}

func (s *Scheduler) claimTarget(targetKey string) bool {
	targetKey = strings.TrimSpace(targetKey)
	if targetKey == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.activeTargets[targetKey]; ok {
		return false
	}
	s.activeTargets[targetKey] = struct{}{}
	return true
}

func (s *Scheduler) releaseTarget(targetKey string) {
	targetKey = strings.TrimSpace(targetKey)
	if targetKey == "" {
		return
	}
	s.mu.Lock()
	delete(s.activeTargets, targetKey)
	s.mu.Unlock()
}

func inFlightTargetKey(jobID, probeID string) string {
	return strings.TrimSpace(jobID) + "::" + strings.TrimSpace(probeID)
}

func (s *Scheduler) recordOfflineRun(jobID, probeID string, now time.Time) {
	run, err := s.store.RecordRunStart(JobRun{
		JobID:     jobID,
		ProbeID:   probeID,
		RequestID: fmt.Sprintf("job-%s-%d-%s", jobID, now.UnixNano(), probeID),
		StartedAt: now,
		Status:    RunStatusRunning,
	})
	if err != nil {
		s.logger.Warn("record offline run failed", zap.String("job_id", jobID), zap.String("probe_id", probeID), zap.Error(err))
		return
	}
	s.completeRun(run.ID, RunStatusFailed, nil, "probe offline")
}

func (s *Scheduler) probeOnline(probeID string) bool {
	ps, ok := s.fleet.Get(probeID)
	if !ok || ps == nil {
		return false
	}
	return strings.EqualFold(ps.Status, "online")
}

func (s *Scheduler) resolveTargets(target Target) []string {
	switch target.Kind {
	case TargetKindProbe:
		return uniqueSorted([]string{target.Value})
	case TargetKindTag:
		probes := s.fleet.ListByTag(target.Value)
		ids := make([]string, 0, len(probes))
		for _, p := range probes {
			if p != nil {
				ids = append(ids, p.ID)
			}
		}
		return uniqueSorted(ids)
	case TargetKindAll:
		probes := s.fleet.List()
		ids := make([]string, 0, len(probes))
		for _, p := range probes {
			if p != nil {
				ids = append(ids, p.ID)
			}
		}
		return uniqueSorted(ids)
	default:
		return nil
	}
}

func uniqueSorted(ids []string) []string {
	set := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := set[id]; ok {
			continue
		}
		set[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func formatResultOutput(result *protocol.CommandResultPayload) string {
	if result == nil {
		return ""
	}
	stdout := strings.TrimSpace(result.Stdout)
	stderr := strings.TrimSpace(result.Stderr)
	switch {
	case stdout != "" && stderr != "":
		return stdout + "\n" + stderr
	case stdout != "":
		return stdout
	case stderr != "":
		return stderr
	default:
		return ""
	}
}

func isScheduleDue(schedule string, lastRunAt *time.Time, createdAt, now time.Time) (bool, error) {
	schedule = strings.TrimSpace(schedule)
	if schedule == "" {
		return false, fmt.Errorf("schedule is required")
	}

	anchor := createdAt.UTC()
	if anchor.IsZero() {
		anchor = now.UTC()
	}
	if lastRunAt != nil {
		anchor = lastRunAt.UTC()
	}

	if interval, err := time.ParseDuration(schedule); err == nil {
		if interval <= 0 {
			return false, fmt.Errorf("interval must be > 0")
		}
		return !anchor.Add(interval).After(now.UTC()), nil
	}

	spec, err := cron.ParseStandard(schedule)
	if err != nil {
		return false, err
	}
	next := spec.Next(anchor)
	return !next.After(now.UTC()), nil
}
