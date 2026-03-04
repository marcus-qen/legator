package compliance

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ScheduleInterval defines how often a scheduled export runs.
type ScheduleInterval string

const (
	ScheduleDaily  ScheduleInterval = "daily"
	ScheduleWeekly ScheduleInterval = "weekly"
)

// ScheduleEntry configures a recurring compliance export.
type ScheduleEntry struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Format   ExportFormat     `json:"format"`
	Interval ScheduleInterval `json:"interval"`
	Filter   ExportFilter     `json:"filter"`
	Enabled  bool             `json:"enabled"`
}

// Clock is an abstraction over time to allow test injection.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// realClock is the production clock.
type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// ExportScheduler manages periodic export generation and stored export retrieval.
type ExportScheduler struct {
	store      *Store
	fleetScope string
	schedules  []ScheduleEntry
	logger     *zap.Logger
	clock      Clock

	mu      sync.Mutex
	cancel  context.CancelFunc
	stopped chan struct{}

	// Retention period for stored exports (default 30 days).
	Retention time.Duration
}

// NewScheduler creates a new ExportScheduler.
func NewScheduler(store *Store, fleetScope string, logger *zap.Logger) *ExportScheduler {
	return &ExportScheduler{
		store:      store,
		fleetScope: fleetScope,
		clock:      realClock{},
		logger:     logger,
		Retention:  30 * 24 * time.Hour,
	}
}

// newSchedulerWithClock creates a scheduler with an injected clock (for tests).
func newSchedulerWithClock(store *Store, fleetScope string, logger *zap.Logger, clk Clock) *ExportScheduler {
	s := NewScheduler(store, fleetScope, logger)
	s.clock = clk
	return s
}

// AddSchedule registers a schedule entry. Must be called before Start.
func (es *ExportScheduler) AddSchedule(entry ScheduleEntry) {
	es.mu.Lock()
	defer es.mu.Unlock()
	es.schedules = append(es.schedules, entry)
}

// Start launches the scheduler background goroutine.
func (es *ExportScheduler) Start(ctx context.Context) {
	es.mu.Lock()
	defer es.mu.Unlock()

	if es.stopped != nil {
		return // already running
	}

	ctx, cancel := context.WithCancel(ctx)
	es.cancel = cancel
	es.stopped = make(chan struct{})

	go es.run(ctx)
}

// Stop halts the scheduler and waits for it to finish.
func (es *ExportScheduler) Stop() {
	es.mu.Lock()
	cancel := es.cancel
	stopped := es.stopped
	es.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if stopped != nil {
		<-stopped
	}
}

// run is the scheduler loop.
func (es *ExportScheduler) run(ctx context.Context) {
	defer func() {
		es.mu.Lock()
		close(es.stopped)
		es.stopped = nil
		es.mu.Unlock()
	}()

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// Run immediately on start to catch up on any missed schedules, then tick hourly.
	es.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			es.tick(ctx)
		}
	}
}

// tick evaluates all schedules and generates due exports.
func (es *ExportScheduler) tick(ctx context.Context) {
	es.mu.Lock()
	schedules := make([]ScheduleEntry, len(es.schedules))
	copy(schedules, es.schedules)
	es.mu.Unlock()

	now := es.clock.Now().UTC()

	for _, entry := range schedules {
		if !entry.Enabled {
			continue
		}

		// Check if an export is due by looking for the last export of this schedule.
		due, err := es.isDue(entry, now)
		if err != nil {
			es.logger.Warn("could not check schedule due",
				zap.String("schedule", entry.ID), zap.Error(err))
			continue
		}
		if !due {
			continue
		}

		if err := es.generateExport(ctx, entry, now); err != nil {
			es.logger.Error("scheduled export failed",
				zap.String("schedule", entry.ID), zap.Error(err))
		} else {
			es.logger.Info("scheduled export complete",
				zap.String("schedule", entry.ID),
				zap.String("format", string(entry.Format)))
		}
	}

	// Purge old exports.
	if n, err := es.store.PurgeOldExports(es.Retention); err != nil {
		es.logger.Warn("purge exports failed", zap.Error(err))
	} else if n > 0 {
		es.logger.Info("purged old exports", zap.Int64("count", n))
	}
}

// isDue returns true if the schedule should fire now (no recent export exists).
func (es *ExportScheduler) isDue(entry ScheduleEntry, now time.Time) (bool, error) {
	exports, err := es.store.ListExports(1)
	if err != nil {
		return false, err
	}

	var period time.Duration
	switch entry.Interval {
	case ScheduleWeekly:
		period = 7 * 24 * time.Hour
	default: // daily
		period = 24 * time.Hour
	}

	// Find the most recent export matching this schedule's format.
	for _, exp := range exports {
		if exp.Format == entry.Format && now.Sub(exp.CreatedAt) < period {
			return false, nil
		}
	}
	return true, nil
}

// generateExport runs the export and stores the result.
func (es *ExportScheduler) generateExport(ctx context.Context, entry ScheduleEntry, at time.Time) error {
	_ = ctx // reserved for future use (context-aware store)

	var buf bytes.Buffer
	var genErr error

	switch entry.Format {
	case ExportFormatCSV:
		genErr = WriteCSV(es.store, entry.Filter, &buf)
	case ExportFormatPDF:
		genErr = WritePDF(es.store, entry.Filter, es.fleetScope, &buf)
	default:
		return fmt.Errorf("unknown format: %s", entry.Format)
	}

	rec := ExportRecord{
		Format:    entry.Format,
		CreatedAt: at,
		ProbeIDs:  entry.Filter.ProbeIDs,
		Category:  entry.Filter.Category,
	}
	if !entry.Filter.Since.IsZero() {
		rec.Since = entry.Filter.Since.Format(time.RFC3339)
	}
	if !entry.Filter.Until.IsZero() {
		rec.Until = entry.Filter.Until.Format(time.RFC3339)
	}

	if genErr != nil {
		rec.Status = "error"
		rec.ErrorMsg = genErr.Error()
		return es.store.SaveExport(rec, nil)
	}

	rec.Status = "ok"
	return es.store.SaveExport(rec, buf.Bytes())
}

// GenerateOnDemand creates a one-off export, stores it, and returns its record ID.
func (es *ExportScheduler) GenerateOnDemand(format ExportFormat, filter ExportFilter) (string, error) {
	var buf bytes.Buffer

	switch format {
	case ExportFormatCSV:
		if err := WriteCSV(es.store, filter, &buf); err != nil {
			return "", fmt.Errorf("generate CSV: %w", err)
		}
	case ExportFormatPDF:
		if err := WritePDF(es.store, filter, es.fleetScope, &buf); err != nil {
			return "", fmt.Errorf("generate PDF: %w", err)
		}
	default:
		return "", fmt.Errorf("unknown format: %s", format)
	}

	rec := ExportRecord{
		ID:        uuid.NewString(),
		Format:    format,
		CreatedAt: es.clock.Now().UTC(),
		Status:    "ok",
		ProbeIDs:  filter.ProbeIDs,
		Category:  filter.Category,
	}
	if !filter.Since.IsZero() {
		rec.Since = filter.Since.Format(time.RFC3339)
	}
	if !filter.Until.IsZero() {
		rec.Until = filter.Until.Format(time.RFC3339)
	}

	if err := es.store.SaveExport(rec, buf.Bytes()); err != nil {
		return "", fmt.Errorf("save export: %w", err)
	}
	return rec.ID, nil
}
