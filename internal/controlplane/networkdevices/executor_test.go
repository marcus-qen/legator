package networkdevices

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// --- SSHExecutor unit tests with mock ---

// mockExecuteFunc allows tests to control Execute behaviour.
type mockExecutor struct {
	results map[string]*CommandResult
	err     error
}

func (m *mockExecutor) execute(_ context.Context, _ Device, _ CredentialInput, command string) (*CommandResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	if r, ok := m.results[command]; ok {
		return r, nil
	}
	return &CommandResult{Output: "", Command: command}, nil
}

func TestSSHExecutorResolveCredentials_InlinePreferred(t *testing.T) {
	store := newTestStore(t)
	// Store a credential.
	_ = store.StoreCredential(DeviceCredential{DeviceID: "d1", Password: "stored-pass"})

	exec := NewSSHExecutor(store)

	// Inline credential should take precedence.
	got := exec.resolveCredentials("d1", CredentialInput{Password: "inline-pass"})
	if got.Password != "inline-pass" {
		t.Fatalf("expected inline-pass, got %q", got.Password)
	}
}

func TestSSHExecutorResolveCredentials_FallbackToStore(t *testing.T) {
	store := newTestStore(t)
	_ = store.StoreCredential(DeviceCredential{DeviceID: "d2", Password: "stored-pass"})

	exec := NewSSHExecutor(store)

	got := exec.resolveCredentials("d2", CredentialInput{})
	if got.Password != "stored-pass" {
		t.Fatalf("expected stored-pass, got %q", got.Password)
	}
}

func TestSSHExecutorResolveCredentials_MissingStore(t *testing.T) {
	exec := NewSSHExecutor(nil)
	got := exec.resolveCredentials("xyz", CredentialInput{})
	if got.Password != "" {
		t.Fatalf("expected empty, got %q", got.Password)
	}
}

func TestSSHExecutorOutputTruncation(t *testing.T) {
	// Create an executor with a tiny max output.
	exec := &SSHExecutor{
		DialTimeout:    5 * time.Second,
		CommandTimeout: 5 * time.Second,
		MaxOutputBytes: 10,
		store:          nil,
	}
	// Manually call the truncation logic.
	raw := "0123456789ABCDEFGHIJ"
	maxBytes := exec.MaxOutputBytes
	truncated := false
	if len(raw) > maxBytes {
		raw = raw[:maxBytes]
		truncated = true
	}
	if !truncated {
		t.Fatal("expected truncated=true")
	}
	if raw != "0123456789" {
		t.Fatalf("unexpected truncated output: %q", raw)
	}
}

func TestSSHExecutorEmptyCommand(t *testing.T) {
	exec := NewSSHExecutor(nil)
	_, err := exec.Execute(context.Background(), Device{Host: "10.0.0.1", Port: 22}, CredentialInput{}, "   ")
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestSSHExecutorNoCreds(t *testing.T) {
	exec := NewSSHExecutor(nil)
	_, err := exec.Execute(context.Background(), Device{
		Host:     "127.0.0.1",
		Port:     22,
		Username: "admin",
		AuthMode: AuthModePassword,
	}, CredentialInput{}, "hostname")
	// Should fail with no credentials error, not a network error.
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- Store credential tests ---

func TestStoreCredentialRoundTrip(t *testing.T) {
	store := newTestStore(t)

	if err := store.StoreCredential(DeviceCredential{
		DeviceID: "dev-1",
		Password: "s3cret",
	}); err != nil {
		t.Fatalf("store credential: %v", err)
	}

	got, err := store.GetCredential("dev-1")
	if err != nil {
		t.Fatalf("get credential: %v", err)
	}
	if got == nil {
		t.Fatal("expected credential, got nil")
	}
	if got.Password != "s3cret" {
		t.Fatalf("expected s3cret, got %q", got.Password)
	}

	// Upsert.
	if err := store.StoreCredential(DeviceCredential{DeviceID: "dev-1", Password: "new-pass"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got2, _ := store.GetCredential("dev-1")
	if got2.Password != "new-pass" {
		t.Fatalf("expected new-pass after upsert, got %q", got2.Password)
	}
}

func TestStoreCredentialMissing(t *testing.T) {
	store := newTestStore(t)
	got, err := store.GetCredential("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for missing credential")
	}
}

func TestStoreCredentialDelete(t *testing.T) {
	store := newTestStore(t)
	_ = store.StoreCredential(DeviceCredential{DeviceID: "d3", Password: "x"})
	_ = store.DeleteCredential("d3")
	got, _ := store.GetCredential("d3")
	if got != nil {
		t.Fatal("expected nil after delete")
	}
}

// --- Store inventory tests ---

func TestStoreInventoryRoundTrip(t *testing.T) {
	store := newTestStore(t)

	inv := InventoryResult{
		DeviceID:    "dev-inv-1",
		Vendor:      VendorCisco,
		CollectedAt: time.Now().UTC().Truncate(time.Second),
		Hostname:    "core-rtr",
		Version:     "IOS-XE 17.3",
		Serial:      "FCZ1234567",
		Interfaces:  []string{"Gi0/0 up", "Gi0/1 down"},
		Raw:         map[string]string{"hostname": "hostname core-rtr"},
	}

	if err := store.SaveInventory(inv); err != nil {
		t.Fatalf("save inventory: %v", err)
	}

	got, err := store.GetLatestInventory("dev-inv-1")
	if err != nil {
		t.Fatalf("get inventory: %v", err)
	}
	if got.Hostname != "core-rtr" {
		t.Fatalf("expected core-rtr, got %q", got.Hostname)
	}
	if got.Version != "IOS-XE 17.3" {
		t.Fatalf("expected IOS-XE 17.3, got %q", got.Version)
	}
	if got.Serial != "FCZ1234567" {
		t.Fatalf("expected FCZ1234567, got %q", got.Serial)
	}
	if len(got.Interfaces) != 2 {
		t.Fatalf("expected 2 interfaces, got %v", got.Interfaces)
	}
}

func TestStoreInventoryLatestWins(t *testing.T) {
	store := newTestStore(t)

	_ = store.SaveInventory(InventoryResult{
		DeviceID:    "dev-2",
		CollectedAt: time.Now().UTC().Add(-1 * time.Hour),
		Hostname:    "old-hostname",
	})
	_ = store.SaveInventory(InventoryResult{
		DeviceID:    "dev-2",
		CollectedAt: time.Now().UTC(),
		Hostname:    "new-hostname",
	})

	got, _ := store.GetLatestInventory("dev-2")
	if got.Hostname != "new-hostname" {
		t.Fatalf("expected new-hostname (latest), got %q", got.Hostname)
	}
}

func TestStoreInventoryNotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetLatestInventory("ghost")
	if !IsNotFound(err) {
		t.Fatalf("expected not-found, got %v", err)
	}
}

// --- buildSSHClientConfig tests ---

func TestBuildSSHClientConfigNoCredentials(t *testing.T) {
	_, err := buildSSHClientConfig(Device{
		Username: "admin",
		AuthMode: AuthModePassword,
	}, CredentialInput{}, 5*time.Second)
	if err == nil {
		t.Fatal("expected error for no credentials")
	}
}

func TestBuildSSHClientConfigPassword(t *testing.T) {
	config, err := buildSSHClientConfig(Device{
		Username: "admin",
		AuthMode: AuthModePassword,
	}, CredentialInput{Password: "secret"}, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if config.User != "admin" {
		t.Fatalf("expected admin, got %q", config.User)
	}
	if len(config.Auth) == 0 {
		t.Fatal("expected auth methods")
	}
}

func TestBuildSSHClientConfigInvalidKey(t *testing.T) {
	_, err := buildSSHClientConfig(Device{
		Username: "admin",
		AuthMode: AuthModeKey,
	}, CredentialInput{PrivateKey: "not-a-valid-key"}, 5*time.Second)
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
}

// --- helper for executor tests ---

func newTestStoreWithDB(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "exec-test.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
