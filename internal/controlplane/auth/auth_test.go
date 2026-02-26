package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tempDB(t *testing.T) string {
	return filepath.Join(t.TempDir(), "auth.db")
}

func TestCreateAndValidate(t *testing.T) {
	ks, err := NewKeyStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ks.Close()

	key, plain, err := ks.Create("test-key", []Permission{PermFleetRead}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "lgk_") {
		t.Fatalf("key should start with lgk_, got %s", plain[:10])
	}
	if key.KeyPrefix != plain[:12] {
		t.Fatalf("prefix mismatch: %s vs %s", key.KeyPrefix, plain[:12])
	}

	validated, err := ks.Validate(plain)
	if err != nil {
		t.Fatal(err)
	}
	if validated.ID != key.ID {
		t.Fatalf("validated key ID mismatch")
	}
}

func TestValidateWrongKey(t *testing.T) {
	ks, err := NewKeyStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ks.Close()

	ks.Create("test", []Permission{PermAdmin}, nil)

	_, err = ks.Validate("lgk_00000000totallyinvalidkey")
	if err == nil {
		t.Fatal("expected error for wrong key")
	}
}

func TestValidateExpiredKey(t *testing.T) {
	ks, err := NewKeyStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ks.Close()

	past := time.Now().UTC().Add(-1 * time.Hour)
	_, plain, _ := ks.Create("expired", []Permission{PermAdmin}, &past)

	_, err = ks.Validate(plain)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired error, got %v", err)
	}
}

func TestValidateDisabledKey(t *testing.T) {
	ks, err := NewKeyStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ks.Close()

	key, plain, _ := ks.Create("disabled", []Permission{PermAdmin}, nil)
	ks.Revoke(key.ID)

	_, err = ks.Validate(plain)
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled error, got %v", err)
	}
}

func TestHasPermissionAdmin(t *testing.T) {
	key := &APIKey{Permissions: []Permission{PermAdmin}}
	if !HasPermission(key, PermFleetRead) {
		t.Fatal("admin should grant fleet:read")
	}
	if !HasPermission(key, PermCommandExec) {
		t.Fatal("admin should grant command:exec")
	}
}

func TestHasPermissionSpecific(t *testing.T) {
	key := &APIKey{Permissions: []Permission{PermFleetRead, PermAuditRead}}
	if !HasPermission(key, PermFleetRead) {
		t.Fatal("should have fleet:read")
	}
	if HasPermission(key, PermCommandExec) {
		t.Fatal("should NOT have command:exec")
	}
}

func TestListAndDelete(t *testing.T) {
	ks, err := NewKeyStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ks.Close()

	k1, _, _ := ks.Create("key1", []Permission{PermAdmin}, nil)
	ks.Create("key2", []Permission{PermFleetRead}, nil)

	list := ks.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(list))
	}

	ks.Delete(k1.ID)
	list = ks.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 key after delete, got %d", len(list))
	}
}

func TestMiddlewareBlocks(t *testing.T) {
	ks, err := NewKeyStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ks.Close()

	handler := Middleware(ks, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/v1/probes", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestMiddlewarePasses(t *testing.T) {
	ks, err := NewKeyStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ks.Close()

	_, plain, _ := ks.Create("valid", []Permission{PermAdmin}, nil)

	var gotKey *APIKey
	handler := Middleware(ks, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/v1/probes", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if gotKey == nil {
		t.Fatal("APIKey not in context")
	}
}

func TestMiddlewareSkipPaths(t *testing.T) {
	ks, err := NewKeyStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ks.Close()

	handler := Middleware(ks, []string{"/healthz", "/version"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Should skip auth for /healthz
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for skipped path, got %d", w.Code)
	}

	// Should require auth for /api/v1/probes
	req = httptest.NewRequest("GET", "/api/v1/probes", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for protected path, got %d", w.Code)
	}
}

func TestFromContextNil(t *testing.T) {
	key := FromContext(context.Background())
	if key != nil {
		t.Fatal("expected nil from empty context")
	}
}
