package websocket

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

func BenchmarkSSEFanoutLatency(b *testing.B) {
	for _, subscribers := range []int{10, 100, 500, 1000} {
		b.Run(fmt.Sprintf("subscribers_%d", subscribers), func(b *testing.B) {
			registry := newStreamRegistry()
			requestID := "bench-request"

			subs := make([]*StreamSubscriber, 0, subscribers)
			cleanupFns := make([]func(), 0, subscribers)
			for i := 0; i < subscribers; i++ {
				sub, cleanup := registry.Subscribe(requestID, 1)
				subs = append(subs, sub)
				cleanupFns = append(cleanupFns, cleanup)
			}
			defer func() {
				for _, cleanup := range cleanupFns {
					cleanup()
				}
			}()

			latenciesMS := make([]float64, 0, b.N)
			chunk := protocol.OutputChunkPayload{
				RequestID: requestID,
				Stream:    "stdout",
				Data:      "x",
			}

			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				chunk.Seq = i
				start := time.Now()
				registry.Dispatch(chunk)
				for idx, sub := range subs {
					select {
					case <-sub.Ch:
					default:
						b.Fatalf("subscriber %d did not receive dispatched chunk", idx)
					}
				}
				latenciesMS = append(latenciesMS, float64(time.Since(start).Microseconds())/1000.0)
			}

			b.StopTimer()
			sort.Float64s(latenciesMS)
			b.ReportMetric(percentile(latenciesMS, 0.50), "p50_ms")
			b.ReportMetric(percentile(latenciesMS, 0.95), "p95_ms")
			b.ReportMetric(percentile(latenciesMS, 0.99), "p99_ms")
		})
	}
}

func percentile(samples []float64, pct float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	if pct <= 0 {
		return samples[0]
	}
	if pct >= 1 {
		return samples[len(samples)-1]
	}
	idx := int(float64(len(samples)-1) * pct)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(samples) {
		idx = len(samples) - 1
	}
	return samples[idx]
}
