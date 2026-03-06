package compliance

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"

	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

// RemoteProbeExecutor can run commands on remote probes.
type RemoteProbeExecutor interface {
	Execute(ctx context.Context, ps *fleet.ProbeState, cmd protocol.CommandPayload, onChunk func(protocol.OutputChunkPayload)) (*protocol.CommandResultPayload, error)
}

// Scanner runs compliance checks across the fleet.
type Scanner struct {
	fleet           fleet.Fleet
	executor        RemoteProbeExecutor
	store           *Store
	checks          []ComplianceCheck
	logger          *zap.Logger
	commandDispatch *corecommanddispatch.Service // For agent-type probes
}

// NewScanner creates a new compliance scanner.
func NewScanner(f fleet.Fleet, executor RemoteProbeExecutor, store *Store, logger *zap.Logger) *Scanner {
	return &Scanner{
		fleet:    f,
		executor: executor,
		store:    store,
		checks:   BuiltinChecks(),
		logger:   logger,
	}
}

// NewScannerWithCommandDispatch creates a scanner with command dispatch for agent probes.
func NewScannerWithCommandDispatch(f fleet.Fleet, executor RemoteProbeExecutor, store *Store, logger *zap.Logger, cmdDispatch *corecommanddispatch.Service) *Scanner {
	s := NewScanner(f, executor, store, logger)
	s.commandDispatch = cmdDispatch
	return s
}

// Checks returns the registered compliance checks.
func (s *Scanner) Checks() []ComplianceCheck {
	out := make([]ComplianceCheck, len(s.checks))
	copy(out, s.checks)
	return out
}

// Scan runs compliance checks across probes specified in the request.
// If no probes are specified, all probes are scanned.
// If no checks are specified, all checks are run.
func (s *Scanner) Scan(ctx context.Context, req ScanRequest) ScanResponse {
	scanID := uuid.NewString()
	startedAt := time.Now().UTC()

	probes := s.selectProbes(req)
	checks := s.selectChecks(req)

	s.logger.Info("compliance scan started",
		zap.String("scan_id", scanID),
		zap.Int("probes", len(probes)),
		zap.Int("checks", len(checks)),
	)

	var mu sync.Mutex
	var results []ComplianceResult

	var wg sync.WaitGroup
	sem := make(chan struct{}, 4) // max 4 concurrent probe scans

	for _, ps := range probes {
		wg.Add(1)
		go func(probe *fleet.ProbeState) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			probeResults := s.scanProbe(ctx, probe, checks)

			mu.Lock()
			results = append(results, probeResults...)
			mu.Unlock()

			// Persist results
			for _, r := range probeResults {
				if err := s.store.UpsertResult(r); err != nil {
					s.logger.Warn("failed to persist compliance result",
						zap.String("probe_id", probe.ID),
						zap.String("check_id", r.CheckID),
						zap.Error(err),
					)
				}
			}
		}(ps)
	}

	wg.Wait()
	endedAt := time.Now().UTC()

	summary := buildSummary(results, len(probes))

	s.logger.Info("compliance scan complete",
		zap.String("scan_id", scanID),
		zap.Float64("score_pct", summary.ScorePct),
		zap.Duration("duration", endedAt.Sub(startedAt)),
	)

	return ScanResponse{
		ScanID:    scanID,
		StartedAt: startedAt,
		EndedAt:   endedAt,
		Results:   results,
		Summary:   summary,
	}
}

// scanProbe runs all checks against one probe.
func (s *Scanner) scanProbe(ctx context.Context, ps *fleet.ProbeState, checks []ComplianceCheck) []ComplianceResult {
	exec := s.buildExecutor(ctx, ps)

	results := make([]ComplianceResult, 0, len(checks))
	for _, chk := range checks {
		r := s.runCheck(ctx, ps, chk, exec)
		results = append(results, r)
	}
	return results
}

// runCheck executes a single check against a single probe.
func (s *Scanner) runCheck(ctx context.Context, ps *fleet.ProbeState, chk ComplianceCheck, exec ProbeExecutor) ComplianceResult {
	r := ComplianceResult{
		ID:        uuid.NewString(),
		CheckID:   chk.ID,
		CheckName: chk.Name,
		Category:  chk.Category,
		Severity:  chk.Severity,
		ProbeID:   ps.ID,
		Timestamp: time.Now().UTC(),
	}

	if exec == nil {
		r.Status = StatusSkipped
		r.Evidence = fmt.Sprintf("Probe %s (%s) is not accessible for remote execution (status: %s, type: %s)", ps.ID, ps.Hostname, ps.Status, ps.Type)
		return r
	}

	// Timeout per check
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	status, evidence, err := chk.CheckFunc(checkCtx, exec)
	if err != nil {
		r.Status = StatusUnknown
		r.Evidence = fmt.Sprintf("Check execution error: %v", err)
		return r
	}

	r.Status = status
	r.Evidence = evidence
	return r
}

// buildExecutor creates a ProbeExecutor for the given probe.
// Returns nil if the probe cannot be executed against.
func (s *Scanner) buildExecutor(ctx context.Context, ps *fleet.ProbeState) ProbeExecutor {
	if ps == nil {
		return nil
	}

	probeType := strings.ToLower(strings.TrimSpace(ps.Type))
	if probeType == "" {
		// Legacy records can have an empty probe type; treat them as agent probes.
		probeType = fleet.ProbeTypeAgent
	}

	// Check if this is a remote probe (SSH-style)
	if probeType == fleet.ProbeTypeRemote && s.executor != nil && ps.Remote != nil {
		// Use the RemoteProbeExecutor for remote-type probes
		return func(execCtx context.Context, cmd string) (string, int, error) {
			result, err := s.executor.Execute(execCtx, ps, protocol.CommandPayload{
				RequestID: uuid.NewString(),
				Command:   "sh",
				Args:      []string{"-c", cmd},
			}, func(protocol.OutputChunkPayload) {})
			if err != nil {
				return "", -1, err
			}
			return result.Stdout, result.ExitCode, nil
		}
	}

	// Check if this is an agent probe (WebSocket-connected) with command dispatch
	if probeType == fleet.ProbeTypeAgent && s.commandDispatch != nil {
		if ps.Status != "online" {
			return nil
		}
		// Use command dispatch service for agent-type probes
		return func(execCtx context.Context, cmd string) (string, int, error) {
			result, err := s.commandDispatch.DispatchAndWait(execCtx, ps.ID, protocol.CommandPayload{
				RequestID: uuid.NewString(),
				Command:   "sh",
				Args:      []string{"-c", cmd},
			}, 30*time.Second)
			if err != nil {
				return "", -1, err
			}
			return corecommanddispatch.ResultText(result), result.ExitCode, nil
		}
	}

	// Cannot execute against this probe type
	if ps.Status != "online" {
		return nil
	}
	// Non-remote, non-agent online probe: no execution path available
	return nil
}

// selectProbes returns the probes to scan based on the request.
func (s *Scanner) selectProbes(req ScanRequest) []*fleet.ProbeState {
	all := s.fleet.List()

	if len(req.ProbeIDs) == 0 && len(req.Tags) == 0 {
		return all
	}

	idSet := make(map[string]bool, len(req.ProbeIDs))
	for _, id := range req.ProbeIDs {
		idSet[id] = true
	}

	tagSet := make(map[string]bool, len(req.Tags))
	for _, t := range req.Tags {
		tagSet[strings.ToLower(t)] = true
	}

	var out []*fleet.ProbeState
	for _, ps := range all {
		if idSet[ps.ID] {
			out = append(out, ps)
			continue
		}
		for _, tag := range ps.Tags {
			if tagSet[strings.ToLower(tag)] {
				out = append(out, ps)
				break
			}
		}
	}
	return out
}

// selectChecks returns the checks to run based on the request.
func (s *Scanner) selectChecks(req ScanRequest) []ComplianceCheck {
	if len(req.CheckIDs) == 0 {
		return s.checks
	}
	idSet := make(map[string]bool, len(req.CheckIDs))
	for _, id := range req.CheckIDs {
		idSet[id] = true
	}
	var out []ComplianceCheck
	for _, c := range s.checks {
		if idSet[c.ID] {
			out = append(out, c)
		}
	}
	return out
}

// buildSummary computes a ComplianceSummary from a slice of results.
func buildSummary(results []ComplianceResult, probeCount int) ComplianceSummary {
	summary := ComplianceSummary{
		TotalProbes: probeCount,
		ByCategory:  map[string]CategorySummary{},
	}

	catMap := map[string]*CategorySummary{}

	for _, r := range results {
		summary.TotalChecks++
		cs, ok := catMap[r.Category]
		if !ok {
			cs = &CategorySummary{Category: r.Category}
			catMap[r.Category] = cs
		}
		cs.Total++

		switch r.Status {
		case StatusPass:
			summary.Passing++
			cs.Passing++
		case StatusFail:
			summary.Failing++
			cs.Failing++
		case StatusWarning:
			summary.Warning++
			cs.Warning++
		default:
			summary.Unknown++
			cs.Unknown++
		}
	}

	scored := summary.Passing + summary.Failing + summary.Warning
	if scored > 0 {
		summary.ScorePct = float64(summary.Passing) / float64(scored) * 100
	}

	for cat, cs := range catMap {
		scoredCat := cs.Passing + cs.Failing + cs.Warning
		if scoredCat > 0 {
			cs.ScorePct = float64(cs.Passing) / float64(scoredCat) * 100
		}
		summary.ByCategory[cat] = *cs
	}

	return summary
}
