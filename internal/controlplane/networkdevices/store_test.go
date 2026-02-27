package networkdevices

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "network-devices.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStoreCreateListUpdateDeleteDevice(t *testing.T) {
	store := newTestStore(t)

	created, err := store.CreateDevice(Device{
		Name:     "core-router-1",
		Host:     "10.0.0.1",
		Port:     22,
		Vendor:   VendorCisco,
		Username: "netops",
		AuthMode: AuthModePassword,
		Tags:     []string{"core", "dc1"},
	})
	if err != nil {
		t.Fatalf("create device: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected generated id")
	}
	if created.Port != 22 {
		t.Fatalf("expected port 22, got %d", created.Port)
	}
	if len(created.Tags) != 2 {
		t.Fatalf("expected 2 tags, got %v", created.Tags)
	}

	listed, err := store.ListDevices()
	if err != nil {
		t.Fatalf("list devices: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 device, got %d", len(listed))
	}

	updated, err := store.UpdateDevice(created.ID, Device{
		Name:     "core-router-1-renamed",
		Port:     2222,
		Vendor:   VendorJunos,
		Username: "admin",
		AuthMode: AuthModeKey,
		Tags:     []string{"core", "edge", "edge"},
	})
	if err != nil {
		t.Fatalf("update device: %v", err)
	}
	if updated.Name != "core-router-1-renamed" {
		t.Fatalf("unexpected name: %q", updated.Name)
	}
	if updated.Port != 2222 {
		t.Fatalf("expected port 2222, got %d", updated.Port)
	}
	if updated.Vendor != VendorJunos {
		t.Fatalf("expected vendor junos, got %q", updated.Vendor)
	}
	if len(updated.Tags) != 2 {
		t.Fatalf("expected deduped tags, got %v", updated.Tags)
	}

	if err := store.DeleteDevice(created.ID); err != nil {
		t.Fatalf("delete device: %v", err)
	}
	if _, err := store.GetDevice(created.ID); !IsNotFound(err) {
		t.Fatalf("expected not found after delete, got err=%v", err)
	}
}

func TestStoreDefaults(t *testing.T) {
	store := newTestStore(t)

	created, err := store.CreateDevice(Device{
		Name:     "switch-a",
		Host:     "192.168.1.10",
		Vendor:   "",
		Username: "ops",
		AuthMode: "",
		Tags:     []string{"  Core ", "", "core"},
	})
	if err != nil {
		t.Fatalf("create device: %v", err)
	}
	if created.Port != 22 {
		t.Fatalf("expected default port 22, got %d", created.Port)
	}
	if created.Vendor != VendorGeneric {
		t.Fatalf("expected generic vendor, got %q", created.Vendor)
	}
	if created.AuthMode != AuthModePassword {
		t.Fatalf("expected default auth_mode password, got %q", created.AuthMode)
	}
	if len(created.Tags) != 1 || created.Tags[0] != "core" {
		t.Fatalf("unexpected normalized tags: %#v", created.Tags)
	}
}
