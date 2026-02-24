package rbac

import (
	"context"
	"testing"
)

func TestRolePermissions(t *testing.T) {
	tests := []struct {
		role    Role
		action  Action
		allowed bool
	}{
		// Viewer
		{RoleViewer, ActionViewAgents, true},
		{RoleViewer, ActionViewRuns, true},
		{RoleViewer, ActionViewInventory, true},
		{RoleViewer, ActionViewAudit, true},
		{RoleViewer, ActionRunAgent, false},
		{RoleViewer, ActionApprove, false},
		{RoleViewer, ActionConfigure, false},
		{RoleViewer, ActionChat, false},

		// Operator
		{RoleOperator, ActionViewAgents, true},
		{RoleOperator, ActionRunAgent, true},
		{RoleOperator, ActionApprove, true},
		{RoleOperator, ActionChat, true},
		{RoleOperator, ActionAbortRun, true},
		{RoleOperator, ActionConfigure, false},
		{RoleOperator, ActionManageDevice, false},

		// Admin
		{RoleAdmin, ActionViewAgents, true},
		{RoleAdmin, ActionRunAgent, true},
		{RoleAdmin, ActionApprove, true},
		{RoleAdmin, ActionConfigure, true},
		{RoleAdmin, ActionManageDevice, true},
		{RoleAdmin, ActionChat, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.role)+"/"+string(tt.action), func(t *testing.T) {
			got := rolePermits(tt.role, tt.action)
			if got != tt.allowed {
				t.Errorf("rolePermits(%s, %s) = %v, want %v", tt.role, tt.action, got, tt.allowed)
			}
		})
	}
}

func TestAuthorize_NoUser(t *testing.T) {
	engine := NewEngine(nil)
	d := engine.Authorize(context.Background(), nil, ActionViewAgents, "")
	if d.Allowed {
		t.Error("expected denial for nil user")
	}
}

func TestAuthorize_NoMatchingPolicy(t *testing.T) {
	engine := NewEngine([]UserPolicy{
		{
			Name:     "admin-only",
			Subjects: []SubjectMatcher{{Claim: "email", Value: "admin@example.com"}},
			Role:     RoleAdmin,
		},
	})

	user := &UserIdentity{Email: "nobody@example.com"}
	d := engine.Authorize(context.Background(), user, ActionViewAgents, "")
	if d.Allowed {
		t.Error("expected denial for unmatched user")
	}
}

func TestAuthorize_EmailMatch(t *testing.T) {
	engine := NewEngine([]UserPolicy{
		{
			Name:     "keith-admin",
			Subjects: []SubjectMatcher{{Claim: "email", Value: "keith@example.com"}},
			Role:     RoleAdmin,
			Scope:    PolicyScope{Agents: []string{"*"}},
		},
	})

	user := &UserIdentity{Email: "keith@example.com"}
	d := engine.Authorize(context.Background(), user, ActionRunAgent, "watchman-deep")
	if !d.Allowed {
		t.Errorf("expected allow, got deny: %s", d.Reason)
	}
}

func TestAuthorize_GroupMatch(t *testing.T) {
	engine := NewEngine([]UserPolicy{
		{
			Name:     "sre-operators",
			Subjects: []SubjectMatcher{{Claim: "groups", Value: "sre-team"}},
			Role:     RoleOperator,
			Scope:    PolicyScope{Agents: []string{"watchman-*", "scout"}},
		},
	})

	user := &UserIdentity{Email: "alice@example.com", Groups: []string{"sre-team", "dev"}}

	// Should allow running a matching agent
	d := engine.Authorize(context.Background(), user, ActionRunAgent, "watchman-deep")
	if !d.Allowed {
		t.Errorf("expected allow for watchman-deep, got deny: %s", d.Reason)
	}

	// Should deny running an out-of-scope agent
	d = engine.Authorize(context.Background(), user, ActionRunAgent, "forge")
	if d.Allowed {
		t.Error("expected deny for forge (out of scope)")
	}

	// Should deny config changes (operator can't configure)
	d = engine.Authorize(context.Background(), user, ActionConfigure, "")
	if d.Allowed {
		t.Error("expected deny for configure action")
	}
}

func TestAuthorize_HighestRoleWins(t *testing.T) {
	engine := NewEngine([]UserPolicy{
		{
			Name:     "viewer-policy",
			Subjects: []SubjectMatcher{{Claim: "email", Value: "keith@example.com"}},
			Role:     RoleViewer,
		},
		{
			Name:     "admin-policy",
			Subjects: []SubjectMatcher{{Claim: "email", Value: "keith@example.com"}},
			Role:     RoleAdmin,
		},
	})

	user := &UserIdentity{Email: "keith@example.com"}
	d := engine.Authorize(context.Background(), user, ActionConfigure, "")
	if !d.Allowed {
		t.Errorf("expected admin role to win, got deny: %s", d.Reason)
	}
}

func TestAuthorize_GlobPatterns(t *testing.T) {
	tests := []struct {
		name    string
		s       string
		pattern string
		want    bool
	}{
		{"exact match", "forge", "forge", true},
		{"wildcard all", "anything", "*", true},
		{"prefix glob", "watchman-deep", "watchman-*", true},
		{"prefix glob miss", "forge", "watchman-*", false},
		{"exact miss", "forge", "scout", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchGlob(tt.s, tt.pattern)
			if got != tt.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.s, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestResolvePolicyDeterministic(t *testing.T) {
	engine := NewEngine([]UserPolicy{
		{
			Name:     "operator-policy",
			Subjects: []SubjectMatcher{{Claim: "email", Value: "keith@example.com"}},
			Role:     RoleOperator,
		},
		{
			Name:     "admin-policy",
			Subjects: []SubjectMatcher{{Claim: "email", Value: "keith@example.com"}},
			Role:     RoleAdmin,
		},
	})

	policy, ok := engine.ResolvePolicy(&UserIdentity{Email: "keith@example.com"})
	if !ok {
		t.Fatalf("expected a matching policy")
	}
	if policy.Name != "admin-policy" {
		t.Fatalf("policy = %s, want admin-policy", policy.Name)
	}
}

func TestComposeDecision_ClampsRole(t *testing.T) {
	baseDecision := Decision{Allowed: true, Reason: "base allow"}
	basePolicy := &UserPolicy{Name: "rbac-admin", Role: RoleAdmin}
	overlay := &UserPolicy{Name: "userpolicy-viewer", Role: RoleViewer}

	d := ComposeDecision(baseDecision, basePolicy, overlay, ActionConfigure, "")
	if d.Allowed {
		t.Fatalf("expected deny after role clamp")
	}
	if d.Reason == "" {
		t.Fatalf("expected explainable deny reason")
	}
}

func TestComposeDecision_OverlayCannotBypassBaseViewer(t *testing.T) {
	baseDecision := Decision{Allowed: true, Reason: "base allow"}
	basePolicy := &UserPolicy{Name: "rbac-viewer", Role: RoleViewer}
	overlay := &UserPolicy{Name: "userpolicy-admin", Role: RoleAdmin}

	d := ComposeDecision(baseDecision, basePolicy, overlay, ActionConfigure, "")
	if d.Allowed {
		t.Fatalf("expected deny: user policy must not bypass base RBAC")
	}
}

func TestComposeDecision_OverlayScopeApplies(t *testing.T) {
	baseDecision := Decision{Allowed: true, Reason: "base allow"}
	basePolicy := &UserPolicy{
		Name:  "rbac-operator",
		Role:  RoleOperator,
		Scope: PolicyScope{Agents: []string{"*"}},
	}
	overlay := &UserPolicy{
		Name:  "userpolicy-ops",
		Role:  RoleOperator,
		Scope: PolicyScope{Agents: []string{"watchman-*"}},
	}

	allowDecision := ComposeDecision(baseDecision, basePolicy, overlay, ActionRunAgent, "watchman-deep")
	if !allowDecision.Allowed {
		t.Fatalf("expected allow for in-scope agent, got: %s", allowDecision.Reason)
	}

	denyDecision := ComposeDecision(baseDecision, basePolicy, overlay, ActionRunAgent, "forge")
	if denyDecision.Allowed {
		t.Fatalf("expected deny for out-of-scope agent")
	}
}
