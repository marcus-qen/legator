package alerts

import "time"

// AlertRule defines one alerting rule.
type AlertRule struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Enabled     bool           `json:"enabled"`
	Condition   AlertCondition `json:"condition"`
	Actions     []AlertAction  `json:"actions"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// AlertCondition defines what to evaluate.
type AlertCondition struct {
	Type      string   `json:"type"`      // "probe_offline", "disk_threshold", "cpu_threshold"
	Threshold float64  `json:"threshold"` // e.g., 90.0 for 90% disk
	Duration  string   `json:"duration"`  // e.g., "2m" — condition must persist
	Tags      []string `json:"tags,omitempty"`
	// Severity is an optional routing hint consumed by alert routing policies.
	// Valid values: "critical", "warning", "info". Omitting it leaves routing
	// to condition-type and tag matchers. Backward-compatible: old rules without
	// this field deserialise with Severity == "".
	Severity string `json:"severity,omitempty"`
}

// AlertAction defines what to do when a rule fires.
type AlertAction struct {
	Type      string `json:"type"`       // "webhook"
	WebhookID string `json:"webhook_id"` // reference to existing webhook
}

// AlertEvent is one alert transition (firing/resolved).
type AlertEvent struct {
	ID         string     `json:"id"`
	RuleID     string     `json:"rule_id"`
	RuleName   string     `json:"rule_name"`
	ProbeID    string     `json:"probe_id"`
	Status     string     `json:"status"` // "firing", "resolved"
	Message    string     `json:"message"`
	FiredAt    time.Time  `json:"fired_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// FiringKey uniquely identifies one rule/probe firing.
type FiringKey struct {
	RuleID  string
	ProbeID string
}
