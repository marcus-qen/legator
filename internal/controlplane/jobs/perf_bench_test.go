package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newBenchmarkStore(b *testing.B) *Store {
	b.Helper()
	store, err := NewStore(filepath.Join(b.TempDir(), "jobs.db"))
	if err != nil {
		b.Fatalf("new store: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })
	return store
}

func createBenchmarkJob(b *testing.B, store *Store) *Job {
	b.Helper()
	job, err := store.CreateJob(Job{
		Name:     "bench-job",
		Command:  "echo ok",
		Schedule: "* * * * *",
		Target: Target{
			Kind:  TargetKindProbe,
			Value: "probe-0",
		},
		Enabled: true,
	})
	if err != nil {
		b.Fatalf("create benchmark job: %v", err)
	}
	return job
}

func BenchmarkSQLiteWriteThroughputContention(b *testing.B) {
	for _, writers := range []int{1, 4, 16, 64} {
		b.Run(fmt.Sprintf("writers_%d", writers), func(b *testing.B) {
			store := newBenchmarkStore(b)
			job := createBenchmarkJob(b, store)

			var seq atomic.Uint64
			b.ReportAllocs()
			b.SetParallelism(writers)
			b.ResetTimer()

			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					n := seq.Add(1)
					_, err := store.RecordRunStart(JobRun{
						JobID:       job.ID,
						ProbeID:     fmt.Sprintf("probe-%d", n%uint64(writers+1)),
						RequestID:   fmt.Sprintf("bench-run-%d", n),
						Status:      RunStatusRunning,
						StartedAt:   time.Now().UTC(),
						Attempt:     1,
						MaxAttempts: 1,
					})
					if err != nil {
						panic(err)
					}
				}
			})

			b.StopTimer()
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "writes/s")
		})
	}
}

func BenchmarkAsyncJobQueueProcessingRate(b *testing.B) {
	for _, maxInFlight := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("max_in_flight_%d", maxInFlight), func(b *testing.B) {
			store := newBenchmarkStore(b)

			dispatcher := AsyncJobDispatcherFunc(func(ctx context.Context, job AsyncJob) error {
				_, err := store.TransitionAsyncJob(job.ID, AsyncJobStateSucceeded, AsyncJobTransitionOptions{})
				return err
			})
			scheduler := NewAsyncWorkerScheduler(store, dispatcher, zap.NewNop(), AsyncWorkerConfig{
				MaxInFlight:    maxInFlight,
				FetchBatchSize: maxInFlight,
				PollInterval:   time.Hour,
			})

			var seq atomic.Uint64
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				n := seq.Add(1)
				job, err := store.CreateAsyncJob(AsyncJob{
					ProbeID:   fmt.Sprintf("probe-%d", n%uint64(maxInFlight+1)),
					RequestID: fmt.Sprintf("bench-async-%d", n),
					Command:   "echo",
					Args:      []string{"ok"},
					State:     AsyncJobStateQueued,
				})
				if err != nil {
					b.Fatalf("create async job: %v", err)
				}

				result, err := scheduler.DispatchNow(job.ID)
				if err != nil {
					b.Fatalf("dispatch async job: %v", err)
				}
				if result.Outcome != AsyncDispatchOutcomeStarted {
					b.Fatalf("unexpected dispatch outcome: %s (reason=%s)", result.Outcome, result.Reason)
				}
			}

			b.StopTimer()
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "jobs/s")
		})
	}
}
