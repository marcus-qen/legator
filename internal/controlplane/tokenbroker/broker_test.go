package tokenbroker

import (
	"errors"
	"testing"
	"time"
)

func TestIssueAndValidateScopedToken(t *testing.T) {
	now := time.Date(2026, 3, 2, 22, 0, 0, 0, time.UTC)
	broker := NewBroker(Config{
		DefaultTTL: 30 * time.Second,
		Now:        func() time.Time { return now },
		TokenGenerator: func() (string, error) {
			return "tok-1", nil
		},
	})

	issued, err := broker.Issue(IssueRequest{
		RunID:     "run-1",
		ProbeID:   "probe-1",
		Scopes:    []string{"runner:start"},
		Issuer:    "control-plane",
		SessionID: "sess-1",
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if issued.Token == "" {
		t.Fatalf("expected token")
	}
	if issued.RunID != "run-1" || issued.ProbeID != "probe-1" {
		t.Fatalf("unexpected claims: %+v", issued.Claims)
	}
	if issued.Issuer != "control-plane" {
		t.Fatalf("expected issuer control-plane, got %s", issued.Issuer)
	}
	if issued.ExpiresAt.Sub(issued.IssuedAt) != 30*time.Second {
		t.Fatalf("expected ttl 30s, got %s", issued.ExpiresAt.Sub(issued.IssuedAt))
	}

	claims, err := broker.Validate(ValidateRequest{
		Token:     issued.Token,
		Scope:     "runner:start",
		RunID:     "run-1",
		ProbeID:   "probe-1",
		SessionID: "sess-1",
	})
	if err != nil {
		t.Fatalf("validate token: %v", err)
	}
	if claims == nil || claims.RunID != "run-1" {
		t.Fatalf("unexpected claims from validate: %+v", claims)
	}
}

func TestValidateRejectsOutOfScopeToken(t *testing.T) {
	broker := NewBroker(Config{TokenGenerator: func() (string, error) { return "tok-2", nil }})
	issued, err := broker.Issue(IssueRequest{
		RunID:   "run-1",
		ProbeID: "probe-1",
		Scopes:  []string{"runner:start"},
		Issuer:  "cp",
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	_, err = broker.Validate(ValidateRequest{Token: issued.Token, Scope: "runner:destroy"})
	if !errors.Is(err, ErrScopeRejected) {
		t.Fatalf("expected scope rejected, got %v", err)
	}
}

func TestValidateRejectsExpiredToken(t *testing.T) {
	now := time.Date(2026, 3, 2, 22, 0, 0, 0, time.UTC)
	broker := NewBroker(Config{
		Now:            func() time.Time { return now },
		TokenGenerator: func() (string, error) { return "tok-3", nil },
	})
	issued, err := broker.Issue(IssueRequest{
		RunID:   "run-1",
		ProbeID: "probe-1",
		Scopes:  []string{"runner:start"},
		Issuer:  "cp",
		TTL:     time.Second,
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	now = now.Add(2 * time.Second)
	_, err = broker.Validate(ValidateRequest{Token: issued.Token, Scope: "runner:start"})
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expected token expired, got %v", err)
	}
}

func TestValidateRejectsRevokedToken(t *testing.T) {
	broker := NewBroker(Config{TokenGenerator: func() (string, error) { return "tok-4", nil }})
	issued, err := broker.Issue(IssueRequest{
		RunID:   "run-1",
		ProbeID: "probe-1",
		Scopes:  []string{"runner:start"},
		Issuer:  "cp",
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if err := broker.Revoke(issued.Token); err != nil {
		t.Fatalf("revoke token: %v", err)
	}

	_, err = broker.Validate(ValidateRequest{Token: issued.Token, Scope: "runner:start"})
	if !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("expected token revoked, got %v", err)
	}
}

func TestValidateRejectsConsumedToken(t *testing.T) {
	broker := NewBroker(Config{TokenGenerator: func() (string, error) { return "tok-5", nil }})
	issued, err := broker.Issue(IssueRequest{
		RunID:   "run-1",
		ProbeID: "probe-1",
		Scopes:  []string{"runner:start"},
		Issuer:  "cp",
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	if _, err := broker.Validate(ValidateRequest{Token: issued.Token, Scope: "runner:start", Consume: true}); err != nil {
		t.Fatalf("first consume validate: %v", err)
	}
	if _, err := broker.Validate(ValidateRequest{Token: issued.Token, Scope: "runner:start", Consume: true}); !errors.Is(err, ErrTokenConsumed) {
		t.Fatalf("expected token consumed, got %v", err)
	}
}
