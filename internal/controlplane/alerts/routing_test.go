package alerts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// newTestRoutingStore creates a temporary RoutingStore for testing.
func newTestRoutingStore(t *testing.T) *RoutingStore {
	t.Helper()
	rs, err := NewRoutingStore(filepath.Join(t.TempDir(), "routing.db"))
	if err != nil {
		t.Fatalf("NewRoutingStore: %v", err)
	}
	t.Cleanup(func() { _ = rs.Close() })
	return rs
}

// -------------------------------------------------------------------
// Matcher logic
// -------------------------------------------------------------------

func TestMatchOp(t *testing.T) {
	cases := []struct {
		op        string
		candidate string
		value     string
		want      bool
	}{
		{"eq", "critical", "critical", true},
		{"eq", "critical", "warning", false},
		{"eq", "Critical", "critical", true},
		{"contains", "probe_offline_disk", "offline", true},
		{"contains", "probe_online", "offline", false},
		{"prefix", "probe_offline", "probe", true},
		{"prefix", "disk_threshold", "probe", false},
		{"", "critical", "critical", true},
	}
	for _, tc := range cases {
		got := matchOp(tc.op, tc.candidate, tc.value)
		if got != tc.want {
			t.Errorf("matchOp(%q,%q,%q) = %v, want %v", tc.op, tc.candidate, tc.value, got, tc.want)
		}
	}
}

func TestMatcherMatches_ConditionType(t *testing.T) {
	m := RoutingMatcher{Field: "condition_type", Op: "eq", Value: "probe_offline"}
	ctx := RoutingContext{ConditionType: "probe_offline"}
	if !matcherMatches(m, ctx) {
		t.Fatal("expected match for condition_type=probe_offline")
	}
	ctx.ConditionType = "disk_threshold"
	if matcherMatches(m, ctx) {
		t.Fatal("expected no match for condition_type=disk_threshold")
	}
}

func TestMatcherMatches_Severity(t *testing.T) {
	m := RoutingMatcher{Field: "severity", Op: "eq", Value: "critical"}
	ctx := RoutingContext{Severity: "critical"}
	if !matcherMatches(m, ctx) {
		t.Fatal("expected match")
	}
	ctx.Severity = "warning"
	if matcherMatches(m, ctx) {
		t.Fatal("expected no match")
	}
}

func TestMatcherMatches_Tag(t *testing.T) {
	m := RoutingMatcher{Field: "tag", Op: "eq", Value: "database"}
	ctx := RoutingContext{Tags: []string{"production", "database"}}
	if !matcherMatches(m, ctx) {
		t.Fatal("expected match on tag=database")
	}
	ctx.Tags = []string{"production", "frontend"}
	if matcherMatches(m, ctx) {
		t.Fatal("expected no match without database tag")
	}
}

func TestMatcherMatches_UnknownField(t *testing.T) {
	m := RoutingMatcher{Field: "unknown_field", Op: "eq", Value: "x"}
	if matcherMatches(m, RoutingContext{}) {
		t.Fatal("unknown field should not match")
	}
}

func TestPolicyMatches_EmptyMatchers(t *testing.T) {
	p := RoutingPolicy{Matchers: []RoutingMatcher{}}
	if !policyMatches(p, RoutingContext{ConditionType: "anything"}) {
		t.Fatal("empty matchers should match everything")
	}
}

func TestPolicyMatches_AllMatchersRequired(t *testing.T) {
	p := RoutingPolicy{Matchers: []RoutingMatcher{
		{Field: "condition_type", Op: "eq", Value: "probe_offline"},
		{Field: "severity", Op: "eq", Value: "critical"},
	}}
	ctx := RoutingContext{ConditionType: "probe_offline", Severity: "critical"}
	if !policyMatches(p, ctx) {
		t.Fatal("all matchers match, expected overall match")
	}
	ctx.Severity = "warning"
	if policyMatches(p, ctx) {
		t.Fatal("second matcher fails, expected no match")
	}
}

// -------------------------------------------------------------------
// RoutingPolicy CRUD
// -------------------------------------------------------------------

func TestRoutingPolicyCRUD(t *testing.T) {
	rs := newTestRoutingStore(t)

	created, err := rs.CreateRoutingPolicy(RoutingPolicy{
		Name:         "ops team",
		OwnerLabel:   "team-ops",
		OwnerContact: "ops@example.com",
		Priority:     10,
		Matchers: []RoutingMatcher{
			{Field: "condition_type", Op: "eq", Value: "probe_offline"},
		},
		RunbookURL: "https://runbooks.example.com/probe-offline",
	})
	if err != nil {
		t.Fatalf("CreateRoutingPolicy: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected non-empty ID")
	}

	fetched, err := rs.GetRoutingPolicy(created.ID)
	if err != nil {
		t.Fatalf("GetRoutingPolicy: %v", err)
	}
	if fetched.Name != "ops team" {
		t.Fatalf("name mismatch: %q", fetched.Name)
	}
	if fetched.RunbookURL != "https://runbooks.example.com/probe-offline" {
		t.Fatalf("runbook_url mismatch: %s", fetched.RunbookURL)
	}

	list, err := rs.ListRoutingPolicies()
	if err != nil {
		t.Fatalf("ListRoutingPolicies: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(list))
	}

	upd := *fetched
	upd.Name = "ops team v2"
	upd.Priority = 20
	updated, err := rs.UpdateRoutingPolicy(upd)
	if err != nil {
		t.Fatalf("UpdateRoutingPolicy: %v", err)
	}
	if updated.Name != "ops team v2" {
		t.Fatalf("expected updated name, got %q", updated.Name)
	}
	if !updated.UpdatedAt.After(updated.CreatedAt) {
		t.Fatal("updated_at should be after created_at")
	}

	if err := rs.DeleteRoutingPolicy(created.ID); err != nil {
		t.Fatalf("DeleteRoutingPolicy: %v", err)
	}
	if _, err := rs.GetRoutingPolicy(created.ID); !IsNotFound(err) {
		t.Fatal("expected not found after deletion")
	}
}

// -------------------------------------------------------------------
// EscalationPolicy CRUD
// -------------------------------------------------------------------

func TestEscalationPolicyCRUD(t *testing.T) {
	rs := newTestRoutingStore(t)

	created, err := rs.CreateEscalationPolicy(EscalationPolicy{
		Name:        "page-on-call",
		Description: "PagerDuty escalation",
		Steps: []EscalationStep{
			{Order: 1, Target: "on-call-engineer", TargetType: "oncall", DelayMin: 0},
			{Order: 2, Target: "team-lead", TargetType: "email", DelayMin: 15, RunbookURL: "https://runbooks.example.com/critical"},
			{Order: 3, Target: "#incidents", TargetType: "webhook", DelayMin: 30},
		},
	})
	if err != nil {
		t.Fatalf("CreateEscalationPolicy: %v", err)
	}
	if len(created.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(created.Steps))
	}

	fetched, err := rs.GetEscalationPolicy(created.ID)
	if err != nil {
		t.Fatalf("GetEscalationPolicy: %v", err)
	}
	if fetched.Steps[1].DelayMin != 15 {
		t.Fatalf("step 2 delay expected 15, got %d", fetched.Steps[1].DelayMin)
	}

	upd := *fetched
	upd.Name = "page-on-call v2"
	upd.Steps = append(upd.Steps, EscalationStep{Order: 4, Target: "management", TargetType: "email", DelayMin: 60})
	updated, err := rs.UpdateEscalationPolicy(upd)
	if err != nil {
		t.Fatalf("UpdateEscalationPolicy: %v", err)
	}
	if len(updated.Steps) != 4 {
		t.Fatalf("expected 4 steps after update, got %d", len(updated.Steps))
	}

	if err := rs.DeleteEscalationPolicy(created.ID); err != nil {
		t.Fatalf("DeleteEscalationPolicy: %v", err)
	}
	if _, err := rs.GetEscalationPolicy(created.ID); !IsNotFound(err) {
		t.Fatal("expected not found after deletion")
	}
}

// -------------------------------------------------------------------
// Routing resolution
// -------------------------------------------------------------------

func TestResolve_NoPolicies(t *testing.T) {
	rs := newTestRoutingStore(t)
	out, err := rs.Resolve(RoutingContext{RuleID: "r1", ConditionType: "probe_offline"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.PolicyName != "none" {
		t.Fatalf("expected 'none', got %q", out.PolicyName)
	}
	if !out.Explain.FallbackUsed {
		t.Fatal("expected fallback_used=true when no policies")
	}
}

func TestResolve_ExactMatch(t *testing.T) {
	rs := newTestRoutingStore(t)
	_, _ = rs.CreateRoutingPolicy(RoutingPolicy{
		Name:       "disk-ops",
		OwnerLabel: "team-storage",
		Priority:   5,
		Matchers:   []RoutingMatcher{{Field: "condition_type", Op: "eq", Value: "disk_threshold"}},
		RunbookURL: "https://runbooks.example.com/disk",
	})
	out, err := rs.Resolve(RoutingContext{RuleID: "r1", ConditionType: "disk_threshold"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.PolicyName != "disk-ops" {
		t.Fatalf("expected 'disk-ops', got %q", out.PolicyName)
	}
	if out.OwnerLabel != "team-storage" {
		t.Fatalf("expected 'team-storage', got %q", out.OwnerLabel)
	}
	if out.RunbookURL != "https://runbooks.example.com/disk" {
		t.Fatalf("runbook_url mismatch: %s", out.RunbookURL)
	}
	if out.Explain.FallbackUsed {
		t.Fatal("expected fallback_used=false on exact match")
	}
}

func TestResolve_PriorityPrecedence(t *testing.T) {
	rs := newTestRoutingStore(t)
	_, _ = rs.CreateRoutingPolicy(RoutingPolicy{
		Name: "low-priority", OwnerLabel: "team-low", Priority: 1,
		Matchers: []RoutingMatcher{{Field: "condition_type", Op: "eq", Value: "probe_offline"}},
	})
	_, _ = rs.CreateRoutingPolicy(RoutingPolicy{
		Name: "high-priority", OwnerLabel: "team-high", Priority: 100,
		Matchers: []RoutingMatcher{{Field: "condition_type", Op: "eq", Value: "probe_offline"}},
	})
	out, err := rs.Resolve(RoutingContext{RuleID: "r1", ConditionType: "probe_offline"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.PolicyName != "high-priority" {
		t.Fatalf("high-priority should win, got %q", out.PolicyName)
	}
}

func TestResolve_FallbackToDefault(t *testing.T) {
	rs := newTestRoutingStore(t)
	_, _ = rs.CreateRoutingPolicy(RoutingPolicy{
		Name: "disk-specific", OwnerLabel: "team-storage", Priority: 10,
		Matchers: []RoutingMatcher{{Field: "condition_type", Op: "eq", Value: "disk_threshold"}},
	})
	_, _ = rs.CreateRoutingPolicy(RoutingPolicy{
		Name: "default-catchall", OwnerLabel: "team-ops", Priority: 0,
		IsDefault:  true,
		Matchers:   []RoutingMatcher{},
		RunbookURL: "https://runbooks.example.com/general",
	})
	out, err := rs.Resolve(RoutingContext{RuleID: "r1", ConditionType: "probe_offline"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.PolicyName != "default-catchall" {
		t.Fatalf("expected default-catchall, got %q", out.PolicyName)
	}
	if !out.Explain.FallbackUsed {
		t.Fatal("expected fallback_used=true")
	}
	if out.OwnerLabel != "team-ops" {
		t.Fatalf("expected team-ops, got %q", out.OwnerLabel)
	}
}

func TestResolve_SpecificBeatsDefault(t *testing.T) {
	rs := newTestRoutingStore(t)
	// Specific policy (not default) wins even when its priority number is lower.
	_, _ = rs.CreateRoutingPolicy(RoutingPolicy{
		Name: "probe-offline-specific", OwnerLabel: "team-infra", Priority: 5, IsDefault: false,
		Matchers: []RoutingMatcher{{Field: "condition_type", Op: "eq", Value: "probe_offline"}},
	})
	_, _ = rs.CreateRoutingPolicy(RoutingPolicy{
		Name: "default", OwnerLabel: "team-ops", Priority: 100, IsDefault: true,
	})
	out, err := rs.Resolve(RoutingContext{RuleID: "r1", ConditionType: "probe_offline"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.PolicyName != "probe-offline-specific" {
		t.Fatalf("specific policy should win over default, got %q", out.PolicyName)
	}
	if out.Explain.FallbackUsed {
		t.Fatal("expected fallback_used=false")
	}
}

func TestResolve_EscalationStepsResolved(t *testing.T) {
	rs := newTestRoutingStore(t)
	ep, _ := rs.CreateEscalationPolicy(EscalationPolicy{
		Name: "page-on-call",
		Steps: []EscalationStep{
			{Order: 1, Target: "on-call", TargetType: "oncall", DelayMin: 0},
			{Order: 2, Target: "manager", TargetType: "email", DelayMin: 30},
		},
	})
	_, _ = rs.CreateRoutingPolicy(RoutingPolicy{
		Name: "critical-ops", OwnerLabel: "team-ops", Priority: 10,
		EscalationPolicyID: ep.ID,
		Matchers:           []RoutingMatcher{{Field: "severity", Op: "eq", Value: "critical"}},
	})
	out, err := rs.Resolve(RoutingContext{RuleID: "r1", Severity: "critical"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.EscalationPolicyID != ep.ID {
		t.Fatalf("escalation policy ID mismatch")
	}
	if len(out.EscalationSteps) != 2 {
		t.Fatalf("expected 2 escalation steps, got %d", len(out.EscalationSteps))
	}
	if out.EscalationSteps[0].Target != "on-call" {
		t.Fatalf("first step target mismatch: %s", out.EscalationSteps[0].Target)
	}
}

func TestResolve_ExplainFields(t *testing.T) {
	rs := newTestRoutingStore(t)
	_, _ = rs.CreateRoutingPolicy(RoutingPolicy{
		Name: "cpu-ops", OwnerLabel: "team-cpu", Priority: 5,
		Matchers: []RoutingMatcher{{Field: "condition_type", Op: "eq", Value: "cpu_threshold"}},
	})
	out, err := rs.Resolve(RoutingContext{RuleID: "r1", ConditionType: "cpu_threshold"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.Explain.MatchedBy == "" {
		t.Fatal("expected non-empty matched_by")
	}
	if out.Explain.Reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestResolve_MultiTagMatch(t *testing.T) {
	rs := newTestRoutingStore(t)
	_, _ = rs.CreateRoutingPolicy(RoutingPolicy{
		Name: "db-team", OwnerLabel: "team-db", Priority: 10,
		Matchers: []RoutingMatcher{{Field: "tag", Op: "eq", Value: "database"}},
	})

	out, err := rs.Resolve(RoutingContext{
		RuleID: "r1", ConditionType: "probe_offline",
		Tags: []string{"production", "database", "postgres"},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.PolicyName != "db-team" {
		t.Fatalf("expected db-team, got %q", out.PolicyName)
	}

	out, err = rs.Resolve(RoutingContext{RuleID: "r1", ConditionType: "probe_offline", Tags: []string{"frontend"}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.PolicyName != "none" {
		t.Fatalf("expected none, got %q", out.PolicyName)
	}
}

// -------------------------------------------------------------------
// HTTP handler tests
// -------------------------------------------------------------------

func TestHandleListRoutingPolicies_Empty(t *testing.T) {
	rs := newTestRoutingStore(t)
	req := httptest.NewRequest("GET", "/api/v1/alerts/routing/policies", nil)
	w := httptest.NewRecorder()
	rs.HandleListRoutingPolicies(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["count"].(float64) != 0 {
		t.Fatalf("expected count=0")
	}
}

func TestHandleCreateRoutingPolicy_Valid(t *testing.T) {
	rs := newTestRoutingStore(t)
	body := `{"name":"ops","owner_label":"team-ops","priority":5,"matchers":[{"field":"condition_type","op":"eq","value":"probe_offline"}],"runbook_url":"https://runbooks/probe"}`
	req := httptest.NewRequest("POST", "/api/v1/alerts/routing/policies", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	rs.HandleCreateRoutingPolicy(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var p RoutingPolicy
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if p.ID == "" {
		t.Fatal("expected ID in response")
	}
}

func TestHandleCreateRoutingPolicy_MissingName(t *testing.T) {
	rs := newTestRoutingStore(t)
	body := `{"owner_label":"team-ops"}`
	req := httptest.NewRequest("POST", "/api/v1/alerts/routing/policies", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	rs.HandleCreateRoutingPolicy(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleCreateRoutingPolicy_MissingOwnerLabel(t *testing.T) {
	rs := newTestRoutingStore(t)
	body := `{"name":"ops"}`
	req := httptest.NewRequest("POST", "/api/v1/alerts/routing/policies", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	rs.HandleCreateRoutingPolicy(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleGetRoutingPolicy_NotFound(t *testing.T) {
	rs := newTestRoutingStore(t)
	req := httptest.NewRequest("GET", "/api/v1/alerts/routing/policies/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	rs.HandleGetRoutingPolicy(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleDeleteRoutingPolicy_NotFound(t *testing.T) {
	rs := newTestRoutingStore(t)
	req := httptest.NewRequest("DELETE", "/api/v1/alerts/routing/policies/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	rs.HandleDeleteRoutingPolicy(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleResolveRouting(t *testing.T) {
	rs := newTestRoutingStore(t)
	_, _ = rs.CreateRoutingPolicy(RoutingPolicy{
		Name: "offline-route", OwnerLabel: "team-infra", Priority: 10,
		Matchers:   []RoutingMatcher{{Field: "condition_type", Op: "eq", Value: "probe_offline"}},
		RunbookURL: "https://runbooks.example.com/offline",
	})

	body := `{"rule_id":"r1","rule_name":"Probe Offline","condition_type":"probe_offline","probe_id":"p1"}`
	req := httptest.NewRequest("POST", "/api/v1/alerts/routing/resolve", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	rs.HandleResolveRouting(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var out RoutingOutcome
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.PolicyName != "offline-route" {
		t.Fatalf("expected offline-route, got %q", out.PolicyName)
	}
	if out.RunbookURL != "https://runbooks.example.com/offline" {
		t.Fatalf("runbook URL mismatch: %s", out.RunbookURL)
	}
	if out.Explain.Reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestHandleCreateEscalationPolicy_Valid(t *testing.T) {
	rs := newTestRoutingStore(t)
	body := `{"name":"page-oncall","steps":[{"order":1,"target":"on-call","target_type":"oncall","delay_minutes":0}]}`
	req := httptest.NewRequest("POST", "/api/v1/alerts/escalation/policies", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	rs.HandleCreateEscalationPolicy(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleCreateEscalationPolicy_InvalidStep(t *testing.T) {
	rs := newTestRoutingStore(t)
	body := `{"name":"bad","steps":[{"order":0,"target":"x","target_type":"email","delay_minutes":0}]}`
	req := httptest.NewRequest("POST", "/api/v1/alerts/escalation/policies", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	rs.HandleCreateEscalationPolicy(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleListEscalationPolicies(t *testing.T) {
	rs := newTestRoutingStore(t)
	_, _ = rs.CreateEscalationPolicy(EscalationPolicy{
		Name:  "ep1",
		Steps: []EscalationStep{{Order: 1, Target: "x", TargetType: "email", DelayMin: 0}},
	})
	req := httptest.NewRequest("GET", "/api/v1/alerts/escalation/policies", nil)
	w := httptest.NewRecorder()
	rs.HandleListEscalationPolicies(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["count"].(float64) != 1 {
		t.Fatalf("expected count=1, got %v", resp["count"])
	}
}

func TestListRoutingPoliciesSortedByPriority(t *testing.T) {
	rs := newTestRoutingStore(t)
	for i, pri := range []int{3, 1, 5, 2} {
		_, _ = rs.CreateRoutingPolicy(RoutingPolicy{
			Name:       fmt.Sprintf("p%d", i),
			OwnerLabel: "team",
			Priority:   pri,
		})
	}
	list, err := rs.ListRoutingPolicies()
	if err != nil {
		t.Fatalf("ListRoutingPolicies: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("expected 4, got %d", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i].Priority > list[i-1].Priority {
			t.Fatalf("not sorted desc: [%d].Priority=%d > [%d].Priority=%d",
				i, list[i].Priority, i-1, list[i-1].Priority)
		}
	}
}
