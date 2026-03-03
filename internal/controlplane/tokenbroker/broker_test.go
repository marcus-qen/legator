package tokenbroker

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newTestBroker(t *testing.T, now *time.Time) *Broker {
	t.Helper()

	store, err := NewStore(filepath.Join(t.TempDir(), "tokenbroker.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	broker := NewBroker(Config{
		Store:      store,
		DefaultTTL: 30 * time.Second,
		MaxScope:   4,
		Now: func() time.Time {
			return now.UTC()
		},
	})

	t.Cleanup(func() {
		_ = store.Close()
	})
	return broker
}

func issueBaseToken(t *testing.T, broker *Broker) *IssuedToken {
	t.Helper()

	issued, err := broker.Issue(IssueRequest{
		RunID:     "job-1",
		ProbeID:   "runner-1",
		Audience:  "runner:start",
		Scopes:    []string{"runner:start"},
		Issuer:    "control-plane",
		SessionID: "sess-1",
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return issued
}

func TestValidateRejectsOutOfScopeToken(t *testing.T) {
	now := time.Date(2026, 3, 2, 22, 0, 0, 0, time.UTC)
	broker := newTestBroker(t, &now)
	issued := issueBaseToken(t, broker)

	_, err := broker.Validate(ValidateRequest{
		Token:     issued.Token,
		Scope:     "runner:stop",
		Audience:  "runner:start",
		RunID:     "job-1",
		ProbeID:   "runner-1",
		SessionID: "sess-1",
		Consume:   true,
	})
	if !errors.Is(err, ErrScopeRejected) {
		t.Fatalf("expected ErrScopeRejected, got %v", err)
	}
}

func TestValidateRejectsExpiredToken(t *testing.T) {
	now := time.Date(2026, 3, 2, 22, 0, 0, 0, time.UTC)
	broker := newTestBroker(t, &now)

	issued, err := broker.Issue(IssueRequest{
		RunID:     "job-1",
		ProbeID:   "runner-1",
		Audience:  "runner:start",
		Scopes:    []string{"runner:start"},
		Issuer:    "control-plane",
		SessionID: "sess-1",
		TTL:       time.Second,
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	now = now.Add(2 * time.Second)
	_, err = broker.Validate(ValidateRequest{
		Token:     issued.Token,
		Scope:     "runner:start",
		Audience:  "runner:start",
		RunID:     "job-1",
		ProbeID:   "runner-1",
		SessionID: "sess-1",
		Consume:   true,
	})
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expected ErrTokenExpired, got %v", err)
	}
}

func TestValidateRejectsConsumedTokenReplay(t *testing.T) {
	now := time.Date(2026, 3, 2, 22, 0, 0, 0, time.UTC)
	broker := newTestBroker(t, &now)
	issued := issueBaseToken(t, broker)

	if _, err := broker.Validate(ValidateRequest{
		Token:     issued.Token,
		Scope:     "runner:start",
		Audience:  "runner:start",
		RunID:     "job-1",
		ProbeID:   "runner-1",
		SessionID: "sess-1",
		Consume:   true,
	}); err != nil {
		t.Fatalf("first consume: %v", err)
	}

	_, err := broker.Validate(ValidateRequest{
		Token:     issued.Token,
		Scope:     "runner:start",
		Audience:  "runner:start",
		RunID:     "job-1",
		ProbeID:   "runner-1",
		SessionID: "sess-1",
		Consume:   true,
	})
	if !errors.Is(err, ErrTokenConsumed) {
		t.Fatalf("expected ErrTokenConsumed, got %v", err)
	}
}

func TestValidateRejectsAudienceMismatch(t *testing.T) {
	now := time.Date(2026, 3, 2, 22, 0, 0, 0, time.UTC)
	broker := newTestBroker(t, &now)

	issued, err := broker.Issue(IssueRequest{
		RunID:     "job-1",
		ProbeID:   "runner-1",
		Audience:  "runner:start",
		Scopes:    []string{"runner:start", "runner:stop"},
		Issuer:    "control-plane",
		SessionID: "sess-1",
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	_, err = broker.Validate(ValidateRequest{
		Token:     issued.Token,
		Scope:     "runner:start",
		Audience:  "runner:stop",
		RunID:     "job-1",
		ProbeID:   "runner-1",
		SessionID: "sess-1",
		Consume:   true,
	})
	if !errors.Is(err, ErrAudienceMismatch) {
		t.Fatalf("expected ErrAudienceMismatch, got %v", err)
	}
}

func TestValidateRejectsBindingMismatch(t *testing.T) {
	now := time.Date(2026, 3, 2, 22, 0, 0, 0, time.UTC)
	broker := newTestBroker(t, &now)
	issued := issueBaseToken(t, broker)

	_, err := broker.Validate(ValidateRequest{
		Token:     issued.Token,
		Scope:     "runner:start",
		Audience:  "runner:start",
		RunID:     "job-2",
		ProbeID:   "runner-1",
		SessionID: "sess-1",
		Consume:   true,
	})
	if !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("expected ErrBindingMismatch, got %v", err)
	}
}
