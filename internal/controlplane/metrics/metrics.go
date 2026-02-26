// Package metrics provides Prometheus-compatible metrics for the control plane.
package metrics

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

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

// Collector holds references to all stat sources.
type Collector struct {
	fleet     FleetCounter
	hub       HubStats
	approvals ApprovalCounter
	audit     AuditCounter
	startTime time.Time
}

// NewCollector creates a metrics collector.
func NewCollector(fleet FleetCounter, hub HubStats, approvals ApprovalCounter, audit AuditCounter) *Collector {
	return &Collector{
		fleet:     fleet,
		hub:       hub,
		approvals: approvals,
		audit:     audit,
		startTime: time.Now(),
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

		// Uptime
		b.WriteString("# HELP legator_uptime_seconds Control plane uptime in seconds.\n")
		b.WriteString("# TYPE legator_uptime_seconds gauge\n")
		fmt.Fprintf(&b, "legator_uptime_seconds %.0f\n", time.Since(c.startTime).Seconds())

		_, _ = w.Write([]byte(b.String()))
	}
}
