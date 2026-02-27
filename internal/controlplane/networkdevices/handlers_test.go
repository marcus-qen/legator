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
