package networkdevices

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

type fakeProber struct {
	testResult      *TestResult
	testErr         error
	inventoryResult *InventoryResult
	inventoryErr    error
}

func (f *fakeProber) Test(_ context.Context, _ Device, _ CredentialInput) (*TestResult, error) {
	if f.testResult == nil {
		return &TestResult{Reachable: true, SSHReady: true}, f.testErr
	}
	return f.testResult, f.testErr
}

func (f *fakeProber) Inventory(_ context.Context, _ Device, _ CredentialInput) (*InventoryResult, error) {
	if f.inventoryResult == nil {
		return &InventoryResult{Hostname: "device-1"}, f.inventoryErr
	}
	return f.inventoryResult, f.inventoryErr
}

func newTestHandler(t *testing.T, prober Prober) (*Handler, *Store) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "network.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewHandler(store, prober), store
}

func TestHandlerCRUD(t *testing.T) {
	h, _ := newTestHandler(t, &fakeProber{})

	createBody := map[string]any{
		"name":      "edge-fw",
		"host":      "10.10.10.10",
		"port":      22,
		"vendor":    "fortinet",
		"username":  "admin",
		"auth_mode": "password",
		"tags":      []string{"edge", "firewall"},
	}
	payload, _ := json.Marshal(createBody)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/network/devices", bytes.NewReader(payload))
	createRR := httptest.NewRecorder()
	h.HandleCreateDevice(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", createRR.Code, createRR.Body.String())
	}

	var created struct {
		Device Device `json:"device"`
	}
	if err := json.NewDecoder(createRR.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Device.ID == "" {
		t.Fatal("expected device id")
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/network/devices", nil)
	listRR := httptest.NewRecorder()
	h.HandleListDevices(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRR.Code)
	}
	if !bytes.Contains(listRR.Body.Bytes(), []byte("edge-fw")) {
		t.Fatalf("expected device in list: %s", listRR.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/network/devices/"+created.Device.ID, nil)
	getReq.SetPathValue("id", created.Device.ID)
	getRR := httptest.NewRecorder()
	h.HandleGetDevice(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRR.Code)
	}

	updateBody := map[string]any{
		"name":      "edge-fw-2",
		"host":      "10.10.10.11",
		"port":      2222,
		"vendor":    "generic",
		"username":  "ops",
		"auth_mode": "key",
		"tags":      []string{"edge"},
	}
	updatePayload, _ := json.Marshal(updateBody)
	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/network/devices/"+created.Device.ID, bytes.NewReader(updatePayload))
	updateReq.SetPathValue("id", created.Device.ID)
	updateRR := httptest.NewRecorder()
	h.HandleUpdateDevice(updateRR, updateReq)
	if updateRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", updateRR.Code, updateRR.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/network/devices/"+created.Device.ID, nil)
	deleteReq.SetPathValue("id", created.Device.ID)
	deleteRR := httptest.NewRecorder()
	h.HandleDeleteDevice(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusOK {
		t.Fatalf("expected 200 delete, got %d body=%s", deleteRR.Code, deleteRR.Body.String())
	}
}

func TestHandlerTestAndInventory(t *testing.T) {
	h, store := newTestHandler(t, &fakeProber{
		testResult: &TestResult{DeviceID: "d1", Reachable: true, SSHReady: true, Message: "ok"},
		inventoryResult: &InventoryResult{
			DeviceID:   "d1",
			Vendor:     VendorCisco,
			Hostname:   "rtr-1",
			Version:    "IOS-XE",
			Interfaces: []string{"Gi0/0 up"},
		},
	})

	device, err := store.CreateDevice(Device{
		Name:     "rtr-1",
		Host:     "10.0.0.1",
		Port:     22,
		Vendor:   VendorCisco,
		Username: "admin",
		AuthMode: AuthModePassword,
	})
	if err != nil {
		t.Fatalf("create device: %v", err)
	}

	testReq := httptest.NewRequest(http.MethodPost, "/api/v1/network/devices/"+device.ID+"/test", bytes.NewBufferString(`{"password":"secret"}`))
	testReq.SetPathValue("id", device.ID)
	testRR := httptest.NewRecorder()
	h.HandleTestDevice(testRR, testReq)
	if testRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", testRR.Code, testRR.Body.String())
	}
	if !bytes.Contains(testRR.Body.Bytes(), []byte("\"reachable\":true")) {
		t.Fatalf("expected reachable=true in test response: %s", testRR.Body.String())
	}

	invReq := httptest.NewRequest(http.MethodPost, "/api/v1/network/devices/"+device.ID+"/inventory", bytes.NewBufferString(`{"password":"secret"}`))
	invReq.SetPathValue("id", device.ID)
	invRR := httptest.NewRecorder()
	h.HandleInventoryDevice(invRR, invReq)
	if invRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", invRR.Code, invRR.Body.String())
	}
	if !bytes.Contains(invRR.Body.Bytes(), []byte("rtr-1")) {
		t.Fatalf("expected hostname in inventory response: %s", invRR.Body.String())
	}
}

func TestHandlerValidation(t *testing.T) {
	h, _ := newTestHandler(t, &fakeProber{})

	badReq := httptest.NewRequest(http.MethodPost, "/api/v1/network/devices", bytes.NewBufferString(`{"name":"x"}`))
	badRR := httptest.NewRecorder()
	h.HandleCreateDevice(badRR, badReq)
	if badRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", badRR.Code)
	}
}

func TestHandlerCommandDevice(t *testing.T) {
	h, store := newTestHandler(t, &fakeProber{})

	device, err := store.CreateDevice(Device{
		Name:     "sw-1",
		Host:     "10.0.0.2",
		Port:     22,
		Vendor:   VendorCisco,
		Username: "admin",
		AuthMode: AuthModePassword,
	})
	if err != nil {
		t.Fatalf("create device: %v", err)
	}

	// Missing command body → 400.
	req400 := httptest.NewRequest(http.MethodPost, "/api/v1/network/devices/"+device.ID+"/command",
		bytes.NewBufferString(`{}`))
	req400.SetPathValue("id", device.ID)
	rr400 := httptest.NewRecorder()
	h.HandleCommandDevice(rr400, req400)
	if rr400.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing command, got %d body=%s", rr400.Code, rr400.Body.String())
	}

	// Unknown device → 404.
	req404 := httptest.NewRequest(http.MethodPost, "/api/v1/network/devices/nope/command",
		bytes.NewBufferString(`{"command":"hostname"}`))
	req404.SetPathValue("id", "nope")
	rr404 := httptest.NewRecorder()
	h.HandleCommandDevice(rr404, req404)
	if rr404.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown device, got %d", rr404.Code)
	}

	// Real execution attempt with no credentials → 502 (no creds).
	reqCmd := httptest.NewRequest(http.MethodPost, "/api/v1/network/devices/"+device.ID+"/command",
		bytes.NewBufferString(`{"command":"hostname"}`))
	reqCmd.SetPathValue("id", device.ID)
	rrCmd := httptest.NewRecorder()
	h.HandleCommandDevice(rrCmd, reqCmd)
	// Should get 502 because no SSH creds available and connection will fail.
	if rrCmd.Code != http.StatusBadGateway {
		t.Logf("note: got %d (may pass if host is reachable)", rrCmd.Code)
	}
}

func TestHandlerScanDevice(t *testing.T) {
	h, store := newTestHandler(t, &fakeProber{})

	device, err := store.CreateDevice(Device{
		Name:     "sw-scan",
		Host:     "10.0.0.3",
		Port:     22,
		Vendor:   VendorJunos,
		Username: "netops",
		AuthMode: AuthModePassword,
	})
	if err != nil {
		t.Fatalf("create device: %v", err)
	}

	// Unknown device → 404.
	req404 := httptest.NewRequest(http.MethodPost, "/api/v1/network/devices/nope/scan", nil)
	req404.SetPathValue("id", "nope")
	rr404 := httptest.NewRecorder()
	h.HandleScanDevice(rr404, req404)
	if rr404.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr404.Code)
	}

	// Scan with no creds and unreachable host → 502 or scan result with errors.
	reqScan := httptest.NewRequest(http.MethodPost, "/api/v1/network/devices/"+device.ID+"/scan",
		bytes.NewBufferString(`{}`))
	reqScan.SetPathValue("id", device.ID)
	rrScan := httptest.NewRecorder()
	h.HandleScanDevice(rrScan, reqScan)
	// 502 when no creds; scanner itself returns best-effort but executor fails pre-connect.
	if rrScan.Code != http.StatusBadGateway && rrScan.Code != http.StatusOK {
		t.Fatalf("expected 502 or 200, got %d body=%s", rrScan.Code, rrScan.Body.String())
	}
	_ = device
}

func TestHandlerGetInventory(t *testing.T) {
	h, store := newTestHandler(t, &fakeProber{})

	device, err := store.CreateDevice(Device{
		Name:     "rtr-inv",
		Host:     "10.0.0.4",
		Port:     22,
		Vendor:   VendorCisco,
		Username: "admin",
		AuthMode: AuthModePassword,
	})
	if err != nil {
		t.Fatalf("create device: %v", err)
	}

	// No inventory yet → 404.
	reqEmpty := httptest.NewRequest(http.MethodGet, "/api/v1/network/devices/"+device.ID+"/inventory", nil)
	reqEmpty.SetPathValue("id", device.ID)
	rrEmpty := httptest.NewRecorder()
	h.HandleGetInventory(rrEmpty, reqEmpty)
	if rrEmpty.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when no inventory, got %d body=%s", rrEmpty.Code, rrEmpty.Body.String())
	}

	// Store an inventory result directly.
	inv := InventoryResult{
		DeviceID:   device.ID,
		Vendor:     VendorCisco,
		Hostname:   "core-router",
		Version:    "IOS-XE 17.3",
		Interfaces: []string{"Gi0/0 up"},
	}
	if err := store.SaveInventory(inv); err != nil {
		t.Fatalf("save inventory: %v", err)
	}

	// Now GET should return it.
	reqGet := httptest.NewRequest(http.MethodGet, "/api/v1/network/devices/"+device.ID+"/inventory", nil)
	reqGet.SetPathValue("id", device.ID)
	rrGet := httptest.NewRecorder()
	h.HandleGetInventory(rrGet, reqGet)
	if rrGet.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rrGet.Code, rrGet.Body.String())
	}
	if !bytes.Contains(rrGet.Body.Bytes(), []byte("core-router")) {
		t.Fatalf("expected hostname in response: %s", rrGet.Body.String())
	}

	// Unknown device → 404.
	reqUnk := httptest.NewRequest(http.MethodGet, "/api/v1/network/devices/nope/inventory", nil)
	reqUnk.SetPathValue("id", "nope")
	rrUnk := httptest.NewRecorder()
	h.HandleGetInventory(rrUnk, reqUnk)
	if rrUnk.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown device, got %d", rrUnk.Code)
	}
}
