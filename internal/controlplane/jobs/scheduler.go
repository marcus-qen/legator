package jobs

import (
	"context"
	"database/sql"
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

type SchedulerOption func(*Scheduler)

// WithDefaultRetryPolicy sets the global retry defaults used when a job does not
// provide a specific retry policy value.
func WithDefaultRetryPolicy(policy RetryPolicy) SchedulerOption {
	return func(s *Scheduler) {
		s.defaultRetryPolicy = policy
	}
}

// WithLifecycleObserver wires lifecycle event notifications for scheduler transitions.
func WithLifecycleObserver(observer LifecycleObserver) SchedulerOption {
	return func(s *Scheduler) {
		if observer == nil {
			s.lifecycleObserver = noopLifecycleObserver{}
			return
		}
		s.lifecycleObserver = observer
	}
}

// Scheduler dispatches due jobs and records run history.
type Scheduler struct {
	store   *Store
	hub     sender
	fleet   fleet.Fleet
	tracker trackable
	logger  *zap.Logger

	mu                 sync.Mutex
	cancel             context.CancelFunc
	ticker             *time.Ticker
	inFlight           map[string]string // request_id -> run_id
	runRequest         map[string]string // run_id -> request_id
	requestTarget      map[string]string // request_id -> jobID::probeID
	activeTargets      map[string]struct{}
	pendingRetryCancel map[string]context.CancelFunc // jobID::probeID -> retry cancel
	defaultRetryPolicy RetryPolicy
	lifecycleObserver  LifecycleObserver
	wg                 sync.WaitGroup
}

// NewScheduler creates a recurring job scheduler.
func NewScheduler(store *Store, hub sender, fleetMgr fleet.Fleet, tracker trackable, logger *zap.Logger, opts ...SchedulerOption) *Scheduler {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Scheduler{
		store:              store,
		hub:                hub,
		fleet:              fleetMgr,
		tracker:            tracker,
		logger:             logger,
		inFlight:           make(map[string]string),
		runRequest:         make(map[string]string),
		requestTarget:      make(map[string]string),
		activeTargets:      make(map[string]struct{}),
		pendingRetryCancel: make(map[string]context.CancelFunc),
		defaultRetryPolicy: RetryPolicy{},
		lifecycleObserver:  noopLifecycleObserver{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
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
	for targetKey, cancelRetry := range s.pendingRetryCancel {
		if cancelRetry != nil {
			cancelRetry()
		}
		delete(s.pendingRetryCancel, targetKey)
	}
	s.inFlight = make(map[string]string)
	s.runRequest = make(map[string]string)
	s.requestTarget = make(map[string]string)
	s.activeTargets = make(map[string]struct{})
	s.pendingRetryCancel = make(map[string]context.CancelFunc)
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

type CancelJobSummary struct {
	CanceledRuns        int
	AlreadyTerminalRuns int
	CanceledRetries     int
}

// CancelJob cancels all pending/running runs for a job.
func (s *Scheduler) CancelJob(jobID string) (CancelJobSummary, error) {
	runs, err := s.store.ListActiveRunsByJob(jobID)
	if err != nil {
		return CancelJobSummary{}, err
	}

	summary := CancelJobSummary{}
	for _, run := range runs {
		if err := s.store.CancelRun(run.ID, "canceled via API"); err != nil {
			if IsInvalidRunTransition(err) {
				summary.AlreadyTerminalRuns++
				continue
			}
			return summary, err
		}
		summary.CanceledRuns++
		s.emitLifecycleEvent(LifecycleEvent{
			Type:        EventJobRunCanceled,
			Actor:       "scheduler",
			JobID:       run.JobID,
			RunID:       run.ID,
			ExecutionID: run.ExecutionID,
			ProbeID:     run.ProbeID,
			Attempt:     run.Attempt,
			MaxAttempts: run.MaxAttempts,
			RequestID:   run.RequestID,
		})
		if requestID := s.requestIDForRun(run.ID); requestID != "" {
			s.tracker.Cancel(requestID)
		}
	}
	if canceled := s.cancelScheduledRetriesForJob(jobID); canceled > 0 {
		summary.CanceledRetries = canceled
	}

	return summary, nil
}

// CancelRun cancels one run by job/run id pair.
func (s *Scheduler) CancelRun(jobID, runID string) (*JobRun, error) {
	run, err := s.store.GetRun(runID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(run.JobID) != strings.TrimSpace(jobID) {
		return nil, sql.ErrNoRows
	}

	if err := s.store.CancelRun(runID, "canceled via API"); err != nil {
		return nil, err
	}
	s.emitLifecycleEvent(LifecycleEvent{
		Type:        EventJobRunCanceled,
		Actor:       "scheduler",
		JobID:       run.JobID,
		RunID:       run.ID,
		ExecutionID: run.ExecutionID,
		ProbeID:     run.ProbeID,
		Attempt:     run.Attempt,
		MaxAttempts: run.MaxAttempts,
		RequestID:   run.RequestID,
	})
	if requestID := s.requestIDForRun(runID); requestID != "" {
		s.tracker.Cancel(requestID)
	}
	s.cancelScheduledRetry(inFlightTargetKey(run.JobID, run.ProbeID))
	return s.store.GetRun(runID)
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

	policy, err := resolveRetryPolicy(job.RetryPolicy, s.defaultRetryPolicy)
	if err != nil {
		return fmt.Errorf("resolve retry policy: %w", err)
	}

	for _, probeID := range probeIDs {
		targetKey := inFlightTargetKey(job.ID, probeID)
		if !s.claimTarget(targetKey) {
			s.logger.Debug("skipping overlapping run for target", zap.String("job_id", job.ID), zap.String("probe_id", probeID))
			continue
		}

		executionID := fmt.Sprintf("jobexec-%s-%s-%d", job.ID, probeID, now.UnixNano())
		s.dispatchAttempt(job, probeID, targetKey, executionID, 1, policy, now)
	}

	return nil
}

func (s *Scheduler) dispatchAttempt(job Job, probeID, targetKey, executionID string, attempt int, policy resolvedRetryPolicy, now time.Time) {
	requestID := fmt.Sprintf("job-%s-%s-attempt-%d-%d", job.ID, probeID, attempt, now.UnixNano())
	run, err := s.store.RecordRunStart(JobRun{
		JobID:       job.ID,
		ProbeID:     probeID,
		RequestID:   requestID,
		ExecutionID: executionID,
		Attempt:     attempt,
		MaxAttempts: policy.MaxAttempts,
		StartedAt:   now,
		Status:      RunStatusPending,
	})
	if err != nil {
		s.releaseTarget(targetKey)
		s.logger.Warn("record run start failed",
			zap.String("job_id", job.ID),
			zap.String("probe_id", probeID),
			zap.Int("attempt", attempt),
			zap.Error(err),
		)
		return
	}

	s.emitLifecycleEvent(LifecycleEvent{
		Type:        EventJobRunQueued,
		Actor:       "scheduler",
		Timestamp:   run.StartedAt,
		JobID:       run.JobID,
		RunID:       run.ID,
		ExecutionID: run.ExecutionID,
		ProbeID:     run.ProbeID,
		Attempt:     run.Attempt,
		MaxAttempts: run.MaxAttempts,
		RequestID:   run.RequestID,
	})

	if err := s.store.MarkRunRunning(run.ID); err != nil {
		if !IsInvalidRunTransition(err) {
			s.logger.Warn("mark run running failed", zap.String("run_id", run.ID), zap.Error(err))
		}
		s.releaseTarget(targetKey)
		return
	}

	s.emitLifecycleEvent(LifecycleEvent{
		Type:        EventJobRunStarted,
		Actor:       "scheduler",
		JobID:       run.JobID,
		RunID:       run.ID,
		ExecutionID: run.ExecutionID,
		ProbeID:     run.ProbeID,
		Attempt:     run.Attempt,
		MaxAttempts: run.MaxAttempts,
		RequestID:   run.RequestID,
	})

	if !s.probeOnline(probeID) {
		s.finishAttempt(*run, job, policy, targetKey, requestID, false, RunStatusFailed, nil, "probe offline")
		return
	}

	payload := protocol.CommandPayload{
		RequestID: requestID,
		Command:   "/bin/sh",
		Args:      []string{"-lc", job.Command},
		Timeout:   defaultCommandTimeout,
		Level:     protocol.CapObserve,
		Stream:    true,
	}

	pending := s.tracker.Track(requestID, probeID, job.Command, payload.Level)
	if pending == nil {
		s.finishAttempt(*run, job, policy, targetKey, requestID, false, RunStatusFailed, nil, "command tracking unavailable")
		return
	}

	s.mu.Lock()
	s.inFlight[requestID] = run.ID
	s.runRequest[run.ID] = requestID
	s.requestTarget[requestID] = targetKey
	s.mu.Unlock()

	if err := s.hub.SendTo(probeID, protocol.MsgCommand, payload); err != nil {
		s.tracker.Cancel(requestID)
		s.finishAttempt(*run, job, policy, targetKey, requestID, true, RunStatusFailed, nil, "probe offline")
		return
	}

	s.wg.Add(1)
	go s.awaitRunResult(*run, requestID, pending, job, policy, targetKey)
}

func (s *Scheduler) awaitRunResult(run JobRun, requestID string, pending *cmdtracker.PendingCommand, job Job, policy resolvedRetryPolicy, targetKey string) {
	defer s.wg.Done()

	if pending == nil || pending.Result == nil {
		s.finishAttempt(run, job, policy, targetKey, requestID, true, RunStatusFailed, nil, "command tracking unavailable")
		return
	}

	result, ok := <-pending.Result
	if !ok || result == nil {
		if err := s.store.CancelRun(run.ID, "command canceled"); err != nil {
			if !IsInvalidRunTransition(err) {
				s.logger.Warn("cancel run failed", zap.String("run_id", run.ID), zap.Error(err))
			}
		} else {
			s.emitLifecycleEvent(LifecycleEvent{
				Type:        EventJobRunCanceled,
				Actor:       "scheduler",
				JobID:       run.JobID,
				RunID:       run.ID,
				ExecutionID: run.ExecutionID,
				ProbeID:     run.ProbeID,
				Attempt:     run.Attempt,
				MaxAttempts: run.MaxAttempts,
				RequestID:   run.RequestID,
			})
		}
		s.clearInFlight(requestID, true)
		return
	}

	status := RunStatusSuccess
	if result.ExitCode != 0 {
		status = RunStatusFailed
	}
	exitCode := result.ExitCode
	output := formatResultOutput(result)
	s.finishAttempt(run, job, policy, targetKey, requestID, true, status, &exitCode, output)
}

func (s *Scheduler) finishAttempt(run JobRun, job Job, policy resolvedRetryPolicy, targetKey, requestID string, hadInFlight bool, status string, exitCode *int, output string) {
	var retryScheduledAt *time.Time
	if status == RunStatusFailed && run.Attempt < policy.MaxAttempts {
		delay := policy.nextRetryDelay(run.Attempt)
		ts := time.Now().UTC().Add(delay)
		retryScheduledAt = &ts
	}

	if err := s.store.CompleteRunWithRetry(run.ID, status, exitCode, output, retryScheduledAt); err != nil {
		if !IsInvalidRunTransition(err) {
			s.logger.Warn("complete run failed", zap.String("run_id", run.ID), zap.Error(err))
		}
		if hadInFlight {
			s.clearInFlight(requestID, true)
		} else {
			s.releaseTarget(targetKey)
		}
		return
	}

	terminalType := EventJobRunFailed
	switch status {
	case RunStatusSuccess:
		terminalType = EventJobRunSucceeded
	case RunStatusCanceled:
		terminalType = EventJobRunCanceled
	}
	s.emitLifecycleEvent(LifecycleEvent{
		Type:        terminalType,
		Actor:       "scheduler",
		JobID:       run.JobID,
		RunID:       run.ID,
		ExecutionID: run.ExecutionID,
		ProbeID:     run.ProbeID,
		Attempt:     run.Attempt,
		MaxAttempts: run.MaxAttempts,
		RequestID:   run.RequestID,
	})

	if retryScheduledAt != nil {
		s.logger.Info("scheduling job retry",
			zap.String("job_id", job.ID),
			zap.String("probe_id", run.ProbeID),
			zap.Int("attempt", run.Attempt+1),
			zap.Int("max_attempts", policy.MaxAttempts),
			zap.Time("retry_scheduled_at", *retryScheduledAt),
		)
		s.emitLifecycleEvent(LifecycleEvent{
			Type:        EventJobRunRetryScheduled,
			Actor:       "scheduler",
			Timestamp:   *retryScheduledAt,
			JobID:       run.JobID,
			RunID:       run.ID,
			ExecutionID: run.ExecutionID,
			ProbeID:     run.ProbeID,
			Attempt:     run.Attempt,
			MaxAttempts: run.MaxAttempts,
			RequestID:   run.RequestID,
		})
		s.scheduleRetry(job, run.ProbeID, targetKey, run.ExecutionID, run.Attempt+1, policy, *retryScheduledAt)
	}

	releaseTarget := retryScheduledAt == nil
	if hadInFlight {
		s.clearInFlight(requestID, releaseTarget)
	} else if releaseTarget {
		s.releaseTarget(targetKey)
	}
}

func (s *Scheduler) scheduleRetry(job Job, probeID, targetKey, executionID string, attempt int, policy resolvedRetryPolicy, scheduledAt time.Time) {
	delay := time.Until(scheduledAt)
	if delay < 0 {
		delay = 0
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if existing := s.pendingRetryCancel[targetKey]; existing != nil {
		existing()
	}
	s.pendingRetryCancel[targetKey] = cancel
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		timer := time.NewTimer(delay)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		s.mu.Lock()
		delete(s.pendingRetryCancel, targetKey)
		s.mu.Unlock()

		latest, err := s.store.GetJob(job.ID)
		if err != nil || !latest.Enabled {
			s.releaseTarget(targetKey)
			return
		}
		if attempt > policy.MaxAttempts {
			s.releaseTarget(targetKey)
			return
		}

		s.dispatchAttempt(*latest, probeID, targetKey, executionID, attempt, policy, time.Now().UTC())
	}()
}

func (s *Scheduler) clearInFlight(requestID string, releaseTarget bool) {
	s.mu.Lock()
	if targetKey, ok := s.requestTarget[requestID]; ok {
		delete(s.requestTarget, requestID)
		if releaseTarget {
			delete(s.activeTargets, targetKey)
		}
	}
	if runID, ok := s.inFlight[requestID]; ok {
		delete(s.runRequest, runID)
	}
	delete(s.inFlight, requestID)
	s.mu.Unlock()
}

func (s *Scheduler) requestIDForRun(runID string) string {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runRequest[runID]
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

func (s *Scheduler) cancelScheduledRetry(targetKey string) {
	targetKey = strings.TrimSpace(targetKey)
	if targetKey == "" {
		return
	}

	s.mu.Lock()
	cancel, ok := s.pendingRetryCancel[targetKey]
	if ok {
		delete(s.pendingRetryCancel, targetKey)
		delete(s.activeTargets, targetKey)
	}
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func (s *Scheduler) cancelScheduledRetriesForJob(jobID string) int {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return 0
	}
	prefix := jobID + "::"

	s.mu.Lock()
	keys := make([]string, 0)
	for targetKey := range s.pendingRetryCancel {
		if strings.HasPrefix(targetKey, prefix) {
			keys = append(keys, targetKey)
		}
	}
	s.mu.Unlock()

	for _, key := range keys {
		s.cancelScheduledRetry(key)
	}
	return len(keys)
}

func (s *Scheduler) emitLifecycleEvent(evt LifecycleEvent) {
	if s == nil || s.lifecycleObserver == nil {
		return
	}
	s.lifecycleObserver.ObserveJobLifecycleEvent(evt.normalize())
}

func inFlightTargetKey(jobID, probeID string) string {
	return strings.TrimSpace(jobID) + "::" + strings.TrimSpace(probeID)
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
