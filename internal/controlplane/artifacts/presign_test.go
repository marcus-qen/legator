package artifacts

import (
	"errors"
	"testing"
	"time"
)

func TestPresignAndValidateHappyPath(t *testing.T) {
	now := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	svc, err := NewService(Config{
		SigningKey: []byte("0123456789abcdef0123456789abcdef"),
		Now:        func() time.Time { return now },
		DefaultTTL: 45 * time.Second,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	issued, err := svc.Presign(PresignRequest{
		RunID:       "run-1",
		SessionID:   "sess-1",
		ScopePrefix: "workspace/run-1",
		Path:        "workspace/run-1/logs/stdout.txt",
		Operation:   OperationUpload,
	})
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	if issued.Token == "" {
		t.Fatal("expected non-empty token")
	}
	if issued.ExpiresAt.Sub(issued.IssuedAt) != 45*time.Second {
		t.Fatalf("expected ttl 45s, got %s", issued.ExpiresAt.Sub(issued.IssuedAt))
	}

	claims, err := svc.Validate(ValidateRequest{
		Token:     issued.Token,
		RunID:     "run-1",
		Path:      "workspace/run-1/logs/stdout.txt",
		Operation: OperationUpload,
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.RunID != "run-1" || claims.ScopePrefix != "workspace/run-1" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestValidateRejectsOutsideScope(t *testing.T) {
	svc, err := NewService(Config{SigningKey: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	issued, err := svc.Presign(PresignRequest{
		RunID:       "run-1",
		SessionID:   "sess-1",
		ScopePrefix: "workspace/run-1",
		Path:        "workspace/run-1/results/out.txt",
		Operation:   OperationUpload,
	})
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	_, err = svc.Validate(ValidateRequest{
		Token:     issued.Token,
		RunID:     "run-1",
		Path:      "workspace/run-2/escape.txt",
		Operation: OperationUpload,
	})
	if !errors.Is(err, ErrScopeRejected) {
		t.Fatalf("expected scope rejection, got %v", err)
	}
}

func TestValidateRejectsOperationMismatch(t *testing.T) {
	svc, err := NewService(Config{SigningKey: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	issued, err := svc.Presign(PresignRequest{
		RunID:     "run-1",
		SessionID: "sess-1",
		Path:      "workspace/run-1/results/out.txt",
		Operation: OperationUpload,
	})
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	_, err = svc.Validate(ValidateRequest{
		Token:     issued.Token,
		RunID:     "run-1",
		Path:      "workspace/run-1/results/out.txt",
		Operation: OperationDownload,
	})
	if !errors.Is(err, ErrScopeRejected) {
		t.Fatalf("expected scope rejection on operation mismatch, got %v", err)
	}
}

func TestValidateRejectsExpiredToken(t *testing.T) {
	now := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	svc, err := NewService(Config{
		SigningKey: []byte("0123456789abcdef0123456789abcdef"),
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	issued, err := svc.Presign(PresignRequest{
		RunID:     "run-1",
		SessionID: "sess-1",
		Path:      "workspace/run-1/results/out.txt",
		Operation: OperationUpload,
		TTL:       time.Second,
	})
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	now = now.Add(2 * time.Second)
	_, err = svc.Validate(ValidateRequest{
		Token:     issued.Token,
		RunID:     "run-1",
		Path:      "workspace/run-1/results/out.txt",
		Operation: OperationUpload,
	})
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expected token expired, got %v", err)
	}
}
