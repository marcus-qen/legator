package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	defaultAsyncSchedulerMaxInFlight  = 8
	defaultAsyncSchedulerPollInterval = 200 * time.Millisecond
	defaultAsyncSchedulerFetchBatch   = 64
)

// AsyncJobDispatcher executes a claimed async job.
type AsyncJobDispatcher interface {
	DispatchAsyncJob(ctx context.Context, job AsyncJob) error
}

type AsyncJobDispatcherFunc func(ctx context.Context, job AsyncJob) error

func (f AsyncJobDispatcherFunc) DispatchAsyncJob(ctx context.Context, job AsyncJob) error {
	return f(ctx, job)
}

type AsyncWorkerConfig struct {
	MaxInFlight    int
	MaxPerProbe    int
	PollInterval   time.Duration
	FetchBatchSize int
}

func (c AsyncWorkerConfig) normalized() AsyncWorkerConfig {
	n := c
	if n.MaxInFlight <= 0 {
		n.MaxInFlight = defaultAsyncSchedulerMaxInFlight
	}
	if n.MaxPerProbe < 0 {
		n.MaxPerProbe = 0
	}
	if n.PollInterval <= 0 {
		n.PollInterval = defaultAsyncSchedulerPollInterval
	}
	if n.FetchBatchSize <= 0 {
		n.FetchBatchSize = defaultAsyncSchedulerFetchBatch
	}
	if n.FetchBatchSize < n.MaxInFlight {
		n.FetchBatchSize = n.MaxInFlight
	}
	if n.FetchBatchSize > maxAsyncJobListLimit {
		n.FetchBatchSize = maxAsyncJobListLimit
	}
	return n
}

type AsyncDispatchOutcome string

const (
	AsyncDispatchOutcomeStarted AsyncDispatchOutcome = "started"
	AsyncDispatchOutcomeQueued  AsyncDispatchOutcome = "queued"
)

type AsyncDispatchResult struct {
	Outcome AsyncDispatchOutcome
	Reason  string
	Job     *AsyncJob
}

// AsyncWorkerScheduler drains queued async jobs with bounded in-flight limits.
type AsyncWorkerScheduler struct {
	store      *Store
	dispatcher AsyncJobDispatcher
	logger     *zap.Logger
	cfg        AsyncWorkerConfig

	metrics asyncSchedulerMetrics

	runMu   sync.Mutex
	schedMu sync.Mutex
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func NewAsyncWorkerScheduler(store *Store, dispatcher AsyncJobDispatcher, logger *zap.Logger, cfg AsyncWorkerConfig) *AsyncWorkerScheduler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &AsyncWorkerScheduler{
		store:      store,
		dispatcher: dispatcher,
		logger:     logger,
		cfg:        cfg.normalized(),
		metrics:    newAsyncSchedulerMetrics([]float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}),
	}
}

func (s *AsyncWorkerScheduler) Start(ctx context.Context) {
	if s == nil || s.store == nil || s.dispatcher == nil {
		return
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.started {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	loopCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.started = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.loop(loopCtx)
	}()
}

func (s *AsyncWorkerScheduler) Stop() {
	if s == nil {
		return
	}
	s.runMu.Lock()
	if !s.started {
		s.runMu.Unlock()
		return
	}
	cancel := s.cancel
	s.cancel = nil
	s.started = false
	s.runMu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

func (s *AsyncWorkerScheduler) loop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	s.drainQueued(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.drainQueued(ctx)
		}
	}
}

func (s *AsyncWorkerScheduler) drainQueued(ctx context.Context) {
	if s == nil || s.store == nil || s.dispatcher == nil {
		return
	}

	s.schedMu.Lock()
	defer s.schedMu.Unlock()

	runningByProbe, err := s.store.RunningAsyncJobsByProbe()
	if err != nil {
		s.logger.Debug("async scheduler: count running by probe failed", zap.Error(err))
		return
	}
	runningTotal := 0
	for _, count := range runningByProbe {
		runningTotal += count
	}

	available := s.cfg.MaxInFlight - runningTotal
	if available <= 0 {
		return
	}

	queued, err := s.store.ListAsyncJobsByState(AsyncJobStateQueued, s.cfg.FetchBatchSize)
	if err != nil {
		s.logger.Debug("async scheduler: list queued jobs failed", zap.Error(err))
		return
	}

	started := 0
	for _, candidate := range queued {
		if started >= available {
			break
		}
		claimed, reason, err := s.claimQueuedJob(candidate, &runningTotal, runningByProbe)
		if err != nil {
			s.logger.Warn("async scheduler: claim queued job failed", zap.String("job_id", candidate.ID), zap.Error(err))
			continue
		}
		if claimed == nil {
			if reason != "" {
				s.logger.Debug("async scheduler: deferred queued job", zap.String("job_id", candidate.ID), zap.String("reason", reason))
			}
			continue
		}
		started++
		s.dispatchClaimedAsync(ctx, *claimed)
	}
}

func (s *AsyncWorkerScheduler) DispatchNow(jobID string) (AsyncDispatchResult, error) {
	if s == nil || s.store == nil || s.dispatcher == nil {
		return AsyncDispatchResult{}, fmt.Errorf("async scheduler unavailable")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return AsyncDispatchResult{}, fmt.Errorf("job id required")
	}

	s.schedMu.Lock()
	defer s.schedMu.Unlock()

	job, err := s.store.GetAsyncJob(jobID)
	if err != nil {
		return AsyncDispatchResult{}, err
	}
	if job.State != AsyncJobStateQueued {
		return AsyncDispatchResult{Outcome: AsyncDispatchOutcomeQueued, Reason: "job_not_queued", Job: job}, nil
	}

	runningByProbe, err := s.store.RunningAsyncJobsByProbe()
	if err != nil {
		return AsyncDispatchResult{}, err
	}
	runningTotal := 0
	for _, count := range runningByProbe {
		runningTotal += count
	}

	claimed, reason, err := s.claimQueuedJob(*job, &runningTotal, runningByProbe)
	if err != nil {
		return AsyncDispatchResult{}, err
	}
	if claimed == nil {
		return AsyncDispatchResult{Outcome: AsyncDispatchOutcomeQueued, Reason: reason, Job: job}, nil
	}

	if err := s.dispatchClaimedSync(context.Background(), *claimed); err != nil {
		return AsyncDispatchResult{Outcome: AsyncDispatchOutcomeStarted, Job: claimed}, err
	}
	return AsyncDispatchResult{Outcome: AsyncDispatchOutcomeStarted, Job: claimed}, nil
}

func (s *AsyncWorkerScheduler) claimQueuedJob(candidate AsyncJob, runningTotal *int, runningByProbe map[string]int) (*AsyncJob, string, error) {
	if runningTotal == nil {
		return nil, "", fmt.Errorf("running total pointer required")
	}
	if *runningTotal >= s.cfg.MaxInFlight {
		return nil, "global_limit", nil
	}
	if s.cfg.MaxPerProbe > 0 && runningByProbe[candidate.ProbeID] >= s.cfg.MaxPerProbe {
		return nil, "probe_limit", nil
	}

	claimed, err := s.store.TransitionAsyncJob(candidate.ID, AsyncJobStateRunning, AsyncJobTransitionOptions{})
	if err != nil {
		if IsNotFound(err) || errors.Is(err, ErrInvalidAsyncJobTransition) {
			return nil, "state_changed", nil
		}
		return nil, "", err
	}

	*runningTotal = *runningTotal + 1
	runningByProbe[claimed.ProbeID] = runningByProbe[claimed.ProbeID] + 1
	if claimed.StartedAt != nil {
		s.metrics.observeQueueLatency(claimed.StartedAt.Sub(claimed.CreatedAt))
	}

	return claimed, "", nil
}

func (s *AsyncWorkerScheduler) dispatchClaimedAsync(ctx context.Context, job AsyncJob) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.dispatchClaimedSync(ctx, job); err != nil {
			s.logger.Warn("async scheduler: dispatch failed", zap.String("job_id", job.ID), zap.String("request_id", job.RequestID), zap.Error(err))
		}
	}()
}

func (s *AsyncWorkerScheduler) dispatchClaimedSync(ctx context.Context, job AsyncJob) error {
	if ctx == nil {
		ctx = context.Background()
	}
	started := time.Now()
	err := s.dispatcher.DispatchAsyncJob(ctx, job)
	s.metrics.observeDispatchLatency(time.Since(started))
	if err == nil {
		return nil
	}

	_, transitionErr := s.store.TransitionAsyncJob(job.ID, AsyncJobStateFailed, AsyncJobTransitionOptions{
		StatusReason: err.Error(),
		Output:       err.Error(),
	})
	if transitionErr != nil && !errors.Is(transitionErr, ErrInvalidAsyncJobTransition) && !IsNotFound(transitionErr) {
		s.logger.Warn("async scheduler: transition failed after dispatch error", zap.String("job_id", job.ID), zap.Error(transitionErr))
	}
	return err
}

func (s *AsyncWorkerScheduler) AsyncJobStateCounts() map[string]int {
	counts := map[string]int{
		string(AsyncJobStateQueued):          0,
		string(AsyncJobStateRunning):         0,
		string(AsyncJobStateSucceeded):       0,
		string(AsyncJobStateFailed):          0,
		string(AsyncJobStateCancelled):       0,
		string(AsyncJobStateWaitingApproval): 0,
		string(AsyncJobStateExpired):         0,
	}
	if s == nil || s.store == nil {
		return counts
	}

	dbCounts, err := s.store.AsyncJobStateCounts()
	if err != nil {
		return counts
	}
	for state, count := range dbCounts {
		counts[string(state)] = count
	}
	return counts
}

func (s *AsyncWorkerScheduler) AsyncJobQueueLatency() ([]float64, []uint64, float64, uint64) {
	return s.metrics.queueLatencySnapshot()
}

func (s *AsyncWorkerScheduler) AsyncJobDispatchLatency() ([]float64, []uint64, float64, uint64) {
	return s.metrics.dispatchLatencySnapshot()
}

type asyncSchedulerMetrics struct {
	queueLatency    *durationHistogram
	dispatchLatency *durationHistogram
}

func newAsyncSchedulerMetrics(buckets []float64) asyncSchedulerMetrics {
	return asyncSchedulerMetrics{
		queueLatency:    newDurationHistogram(buckets),
		dispatchLatency: newDurationHistogram(buckets),
	}
}

func (m asyncSchedulerMetrics) observeQueueLatency(d time.Duration) {
	if m.queueLatency != nil {
		m.queueLatency.observe(d)
	}
}

func (m asyncSchedulerMetrics) observeDispatchLatency(d time.Duration) {
	if m.dispatchLatency != nil {
		m.dispatchLatency.observe(d)
	}
}

func (m asyncSchedulerMetrics) queueLatencySnapshot() ([]float64, []uint64, float64, uint64) {
	if m.queueLatency == nil {
		return nil, nil, 0, 0
	}
	return m.queueLatency.snapshot()
}

func (m asyncSchedulerMetrics) dispatchLatencySnapshot() ([]float64, []uint64, float64, uint64) {
	if m.dispatchLatency == nil {
		return nil, nil, 0, 0
	}
	return m.dispatchLatency.snapshot()
}

type durationHistogram struct {
	mu      sync.RWMutex
	buckets []float64
	counts  []uint64
	sum     float64
	count   uint64
}

func newDurationHistogram(buckets []float64) *durationHistogram {
	out := make([]float64, 0, len(buckets))
	for _, bucket := range buckets {
		if bucket > 0 {
			out = append(out, bucket)
		}
	}
	if len(out) == 0 {
		out = []float64{0.1, 0.25, 0.5, 1, 2.5, 5}
	}
	return &durationHistogram{buckets: out, counts: make([]uint64, len(out)+1)}
}

func (h *durationHistogram) observe(d time.Duration) {
	if h == nil {
		return
	}
	seconds := d.Seconds()
	if seconds < 0 {
		seconds = 0
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.count++
	h.sum += seconds
	for i, bucket := range h.buckets {
		if seconds <= bucket {
			h.counts[i]++
		}
	}
	h.counts[len(h.counts)-1]++
}

func (h *durationHistogram) snapshot() ([]float64, []uint64, float64, uint64) {
	if h == nil {
		return nil, nil, 0, 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()

	buckets := append([]float64(nil), h.buckets...)
	counts := append([]uint64(nil), h.counts...)
	return buckets, counts, h.sum, h.count
}
