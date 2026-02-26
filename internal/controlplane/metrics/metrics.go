// Package metrics provides Prometheus-compatible metrics for the control plane.
package metrics

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var webhookDurationBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// FleetCounter returns probe counts by status.
type FleetCounter interface {
	Count() map[string]int
	TagCounts() map[string]int
}

// HubStats provides WebSocket connection info.
type HubStats interface {
	Connected() int
}

// ApprovalCounter provides approval queue stats.
type ApprovalCounter interface {
	PendingCount() int
}

// AuditCounter provides audit log stats.
type AuditCounter interface {
	Count() int
}

type webhookHistogram struct {
	BucketCounts []uint64
	Count        uint64
	Sum          float64
}

// Collector holds references to all stat sources.
type Collector struct {
	fleet     FleetCounter
	hub       HubStats
	approvals ApprovalCounter
	audit     AuditCounter
	startTime time.Time

	mu              sync.RWMutex
	webhookSent     map[string]map[string]uint64
	webhookErrors   map[string]map[string]uint64
	webhookDuration map[string]*webhookHistogram
}

// NewCollector creates a metrics collector.
func NewCollector(fleet FleetCounter, hub HubStats, approvals ApprovalCounter, audit AuditCounter) *Collector {
	return &Collector{
		fleet:           fleet,
		hub:             hub,
		approvals:       approvals,
		audit:           audit,
		startTime:       time.Now(),
		webhookSent:     make(map[string]map[string]uint64),
		webhookErrors:   make(map[string]map[string]uint64),
		webhookDuration: make(map[string]*webhookHistogram),
	}
}

// RecordWebhookDelivery records webhook delivery metrics for one dispatch attempt.
func (c *Collector) RecordWebhookDelivery(eventType string, statusCode int, duration time.Duration, err error) {
	if eventType == "" {
		eventType = "unknown"
	}

	status := "success"
	if err != nil {
		status = "failure"
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.webhookSent[eventType] == nil {
		c.webhookSent[eventType] = map[string]uint64{"success": 0, "failure": 0}
	}
	c.webhookSent[eventType][status]++

	hist := c.webhookDuration[eventType]
	if hist == nil {
		hist = &webhookHistogram{BucketCounts: make([]uint64, len(webhookDurationBuckets)+1)}
		c.webhookDuration[eventType] = hist
	}
	seconds := duration.Seconds()
	hist.Count++
	hist.Sum += seconds
	for i, bucket := range webhookDurationBuckets {
		if seconds <= bucket {
			hist.BucketCounts[i]++
		}
	}
	hist.BucketCounts[len(hist.BucketCounts)-1]++ // +Inf

	if err != nil {
		errorType := classifyWebhookError(err, statusCode)
		if c.webhookErrors[eventType] == nil {
			c.webhookErrors[eventType] = make(map[string]uint64)
		}
		c.webhookErrors[eventType][errorType]++
	}
}

// Handler returns an HTTP handler that serves Prometheus text format.
func (c *Collector) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		var b strings.Builder

		// Fleet probe counts
		b.WriteString("# HELP legator_probes_total Total number of registered probes by status.\n")
		b.WriteString("# TYPE legator_probes_total gauge\n")
		counts := c.fleet.Count()
		total := 0
		for status, count := range counts {
			fmt.Fprintf(&b, "legator_probes_total{status=%q} %d\n", status, count)
			total += count
		}
		for _, s := range []string{"online", "offline", "degraded", "pending"} {
			if _, ok := counts[s]; !ok {
				fmt.Fprintf(&b, "legator_probes_total{status=%q} 0\n", s)
			}
		}

		b.WriteString("# HELP legator_probes_registered Total number of registered probes.\n")
		b.WriteString("# TYPE legator_probes_registered gauge\n")
		fmt.Fprintf(&b, "legator_probes_registered %d\n", total)

		// WebSocket connections
		b.WriteString("# HELP legator_websocket_connections Current active WebSocket connections.\n")
		b.WriteString("# TYPE legator_websocket_connections gauge\n")
		fmt.Fprintf(&b, "legator_websocket_connections %d\n", c.hub.Connected())

		// Approval queue
		b.WriteString("# HELP legator_approvals_pending Current pending approval requests.\n")
		b.WriteString("# TYPE legator_approvals_pending gauge\n")
		fmt.Fprintf(&b, "legator_approvals_pending %d\n", c.approvals.PendingCount())

		// Audit log
		b.WriteString("# HELP legator_audit_events_total Total audit events recorded.\n")
		b.WriteString("# TYPE legator_audit_events_total counter\n")
		fmt.Fprintf(&b, "legator_audit_events_total %d\n", c.audit.Count())

		// Tag distribution
		tags := c.fleet.TagCounts()
		if len(tags) > 0 {
			b.WriteString("# HELP legator_probes_by_tag Number of probes per tag.\n")
			b.WriteString("# TYPE legator_probes_by_tag gauge\n")
			for tag, count := range tags {
				fmt.Fprintf(&b, "legator_probes_by_tag{tag=%q} %d\n", tag, count)
			}
		}

		c.renderWebhookMetrics(&b)

		// Uptime
		b.WriteString("# HELP legator_uptime_seconds Control plane uptime in seconds.\n")
		b.WriteString("# TYPE legator_uptime_seconds gauge\n")
		fmt.Fprintf(&b, "legator_uptime_seconds %.0f\n", time.Since(c.startTime).Seconds())

		_, _ = w.Write([]byte(b.String()))
	}
}

func (c *Collector) renderWebhookMetrics(b *strings.Builder) {
	sent, errs, durations := c.snapshotWebhookMetrics()

	b.WriteString("# HELP legator_webhooks_sent_total Total webhook deliveries by event type and status.\n")
	b.WriteString("# TYPE legator_webhooks_sent_total counter\n")
	for _, eventType := range sortedKeysFromUint64Nested(sent) {
		statuses := sent[eventType]
		for _, status := range []string{"success", "failure"} {
			fmt.Fprintf(b, "legator_webhooks_sent_total{event_type=%q,status=%q} %d\n", eventType, status, statuses[status])
		}
	}

	b.WriteString("# HELP legator_webhooks_errors_total Total webhook delivery errors by type.\n")
	b.WriteString("# TYPE legator_webhooks_errors_total counter\n")
	for _, eventType := range sortedKeysFromUint64Nested(errs) {
		errorTypes := errs[eventType]
		for _, errorType := range sortedKeysFromUint64Map(errorTypes) {
			fmt.Fprintf(b, "legator_webhooks_errors_total{event_type=%q,error_type=%q} %d\n", eventType, errorType, errorTypes[errorType])
		}
	}

	b.WriteString("# HELP legator_webhook_duration_seconds Webhook delivery duration in seconds.\n")
	b.WriteString("# TYPE legator_webhook_duration_seconds histogram\n")
	for _, eventType := range sortedKeysFromHistogramMap(durations) {
		hist := durations[eventType]
		for i, bucket := range webhookDurationBuckets {
			fmt.Fprintf(b, "legator_webhook_duration_seconds_bucket{event_type=%q,le=%q} %d\n", eventType, strconv.FormatFloat(bucket, 'f', -1, 64), hist.BucketCounts[i])
		}
		fmt.Fprintf(b, "legator_webhook_duration_seconds_bucket{event_type=%q,le=\"+Inf\"} %d\n", eventType, hist.BucketCounts[len(hist.BucketCounts)-1])
		fmt.Fprintf(b, "legator_webhook_duration_seconds_sum{event_type=%q} %g\n", eventType, hist.Sum)
		fmt.Fprintf(b, "legator_webhook_duration_seconds_count{event_type=%q} %d\n", eventType, hist.Count)
	}
}

func (c *Collector) snapshotWebhookMetrics() (map[string]map[string]uint64, map[string]map[string]uint64, map[string]webhookHistogram) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	sent := make(map[string]map[string]uint64, len(c.webhookSent))
	for eventType, statuses := range c.webhookSent {
		sent[eventType] = make(map[string]uint64, len(statuses))
		for status, count := range statuses {
			sent[eventType][status] = count
		}
	}

	errs := make(map[string]map[string]uint64, len(c.webhookErrors))
	for eventType, errorTypes := range c.webhookErrors {
		errs[eventType] = make(map[string]uint64, len(errorTypes))
		for errorType, count := range errorTypes {
			errs[eventType][errorType] = count
		}
	}

	durations := make(map[string]webhookHistogram, len(c.webhookDuration))
	for eventType, hist := range c.webhookDuration {
		clone := webhookHistogram{
			BucketCounts: make([]uint64, len(hist.BucketCounts)),
			Count:        hist.Count,
			Sum:          hist.Sum,
		}
		copy(clone.BucketCounts, hist.BucketCounts)
		durations[eventType] = clone
	}

	return sent, errs, durations
}

func sortedKeysFromUint64Nested(m map[string]map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysFromUint64Map(m map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysFromHistogramMap(m map[string]webhookHistogram) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func classifyWebhookError(err error, statusCode int) string {
	if statusCode >= 400 {
		return "http_status"
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout"
		}
		return "network"
	}

	return "delivery"
}
