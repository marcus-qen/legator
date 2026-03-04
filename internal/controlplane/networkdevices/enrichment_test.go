package networkdevices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// containsStr returns true if s contains sub.
func containsStr(s, sub string) bool {
	if sub == "" {
		return true
	}
	if len(s) < len(sub) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// --- mergeInterfaces ---

func TestMergeInterfaces_EmptyBase(t *testing.T) {
	overlay := []InterfaceDetail{
		{Name: "eth0", SpeedMbps: 1000},
		{Name: "eth1", SpeedMbps: 100},
	}
	result := mergeInterfaces(nil, overlay)
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
}

func TestMergeInterfaces_EnrichExisting(t *testing.T) {
	base := []InterfaceDetail{
		{Name: "eth0", AdminUp: false, OperUp: false},
	}
	overlay := []InterfaceDetail{
		{Name: "eth0", SpeedMbps: 1000, MACAddress: "aa:bb:cc:dd:ee:ff", AdminUp: true, OperUp: true},
	}
	result := mergeInterfaces(base, overlay)
	if len(result) != 1 {
		t.Fatalf("expected 1 interface, got %d", len(result))
	}
	if result[0].SpeedMbps != 1000 {
		t.Errorf("expected SpeedMbps 1000, got %d", result[0].SpeedMbps)
	}
	if result[0].MACAddress != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("expected MAC, got %q", result[0].MACAddress)
	}
	if !result[0].AdminUp {
		t.Error("expected AdminUp after merge")
	}
}

func TestMergeInterfaces_AppendNew(t *testing.T) {
	base := []InterfaceDetail{
		{Name: "eth0"},
	}
	overlay := []InterfaceDetail{
		{Name: "eth1", SpeedMbps: 10000},
	}
	result := mergeInterfaces(base, overlay)
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
	names := map[string]bool{}
	for _, i := range result {
		names[i.Name] = true
	}
	if !names["eth0"] || !names["eth1"] {
		t.Errorf("expected eth0 and eth1, got %v", result)
	}
}

func TestMergeInterfaces_Sorted(t *testing.T) {
	base := []InterfaceDetail{
		{Name: "GigabitEthernet0/2"},
		{Name: "GigabitEthernet0/0"},
	}
	overlay := []InterfaceDetail{
		{Name: "GigabitEthernet0/1"},
	}
	result := mergeInterfaces(base, overlay)
	if len(result) != 3 {
		t.Fatalf("expected 3, got %d", len(result))
	}
	for i := 1; i < len(result); i++ {
		if result[i].Name < result[i-1].Name {
			t.Errorf("not sorted: %v", result)
			break
		}
	}
}

// --- Enricher with mocks ---

func newTestEnricher(t *testing.T, netconfCli NetconfClientInterface, snmpCli SNMPClientInterface) (*Enricher, *Store) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "enrichment-test.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	opts := EnricherOptions{
		NetconfFactory: func(_ context.Context, _ NetconfConfig, _ CredentialInput) (NetconfClientInterface, error) {
			if netconfCli == nil {
				return nil, errors.New("netconf not available")
			}
			return netconfCli, nil
		},
		SNMPFactory: func(_ SNMPConfig) (SNMPClientInterface, error) {
			if snmpCli == nil {
				return nil, errors.New("snmp not available")
			}
			return snmpCli, nil
		},
	}
	return NewEnricher(store, opts), store
}

func TestEnricher_SNMPOnly(t *testing.T) {
	snmpMock := &mockSNMPClient{
		system: &SNMPSystemInfo{
			SysDescr:    "Cisco IOS Software, Version 15.1, RELEASE SOFTWARE",
			SysName:     "core-router-1",
			SysLocation: "DC1 Rack 4",
		},
		interfaces: []InterfaceDetail{
			{Name: "GigabitEthernet0/0", SpeedMbps: 1000, AdminUp: true, OperUp: true},
			{Name: "GigabitEthernet0/1", SpeedMbps: 1000, AdminUp: true, OperUp: false},
		},
	}

	enricher, _ := newTestEnricher(t, nil, snmpMock)

	device := Device{
		ID:     "test-snmp-device",
		Name:   "core-router-1",
		Host:   "10.0.0.1",
		Vendor: VendorCisco,
	}
	req := EnrichRequest{
		SNMP: &SNMPConfig{
			Host:      "10.0.0.1",
			Version:   SNMPv2c,
			Community: "public",
		},
	}

	result, err := enricher.Enrich(context.Background(), device, req)
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}

	if result.Hostname != "core-router-1" {
		t.Errorf("expected hostname core-router-1, got %q", result.Hostname)
	}
	if result.SysDescr == "" {
		t.Error("expected non-empty SysDescr")
	}
	if result.SysLocation != "DC1 Rack 4" {
		t.Errorf("expected SysLocation, got %q", result.SysLocation)
	}
	if len(result.Interfaces) != 2 {
		t.Fatalf("expected 2 interfaces, got %d", len(result.Interfaces))
	}
	if len(result.Sources) == 0 || result.Sources[0] != "snmp" {
		t.Errorf("expected sources=[snmp], got %v", result.Sources)
	}
	if !snmpMock.closed {
		t.Error("expected SNMP client to be closed")
	}
}

func TestEnricher_NetconfOnly(t *testing.T) {
	ifaceXML := []byte(`<data>
  <interfaces xmlns="urn:ietf:params:xml:ns:yang:ietf-interfaces">
    <interface>
      <name>eth0</name>
      <admin-status>up</admin-status>
      <oper-status>up</oper-status>
    </interface>
  </interfaces>
  <version>17.3.4</version>
</data>`)

	ncMock := &mockNetconfClient{
		configData: ifaceXML,
		stateData:  []byte(`<data/>`),
	}

	enricher, _ := newTestEnricher(t, ncMock, nil)

	device := Device{
		ID:     "test-nc-device",
		Name:   "switch-1",
		Host:   "10.0.1.1",
		Vendor: VendorGeneric,
	}
	req := EnrichRequest{
		Netconf: &NetconfConfig{
			Host:     "10.0.1.1",
			Username: "admin",
			Password: "secret",
		},
	}

	result, err := enricher.Enrich(context.Background(), device, req)
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}

	if len(result.Interfaces) != 1 {
		t.Fatalf("expected 1 interface, got %d: %v", len(result.Interfaces), result.Interfaces)
	}
	if result.Interfaces[0].Name != "eth0" {
		t.Errorf("expected eth0, got %q", result.Interfaces[0].Name)
	}
	if result.Firmware == "" {
		t.Error("expected non-empty firmware")
	}
	if !ncMock.closed {
		t.Error("expected NETCONF client to be closed")
	}
}

func TestEnricher_BothSources(t *testing.T) {
	ncMock := &mockNetconfClient{
		configData: []byte(`<data>
  <interfaces xmlns="urn:ietf:params:xml:ns:yang:ietf-interfaces">
    <interface><name>GigabitEthernet0/0</name><admin-status>up</admin-status><oper-status>up</oper-status></interface>
  </interfaces>
</data>`),
		stateData: []byte(`<data/>`),
	}
	snmpMock := &mockSNMPClient{
		system: &SNMPSystemInfo{
			SysName:  "router-dual",
			SysDescr: "Cisco IOS, Version 15.2",
		},
		interfaces: []InterfaceDetail{
			{Name: "GigabitEthernet0/0", SpeedMbps: 1000, MACAddress: "aa:bb:cc:dd:ee:01", AdminUp: true, OperUp: true},
			{Name: "GigabitEthernet0/1", SpeedMbps: 100, MACAddress: "aa:bb:cc:dd:ee:02"},
		},
	}

	enricher, _ := newTestEnricher(t, ncMock, snmpMock)

	device := Device{
		ID:     "test-dual",
		Host:   "10.0.2.1",
		Vendor: VendorCisco,
	}
	req := EnrichRequest{
		Netconf: &NetconfConfig{Host: "10.0.2.1", Username: "admin", Password: "pw"},
		SNMP:    &SNMPConfig{Host: "10.0.2.1", Version: SNMPv2c, Community: "public"},
	}

	result, err := enricher.Enrich(context.Background(), device, req)
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}

	if len(result.Interfaces) < 1 {
		t.Fatalf("expected at least 1 interface, got %d", len(result.Interfaces))
	}
	// GigabitEthernet0/0 should have MAC from SNMP merged
	var ge0 *InterfaceDetail
	for i := range result.Interfaces {
		if result.Interfaces[i].Name == "GigabitEthernet0/0" {
			ge0 = &result.Interfaces[i]
			break
		}
	}
	if ge0 == nil {
		t.Fatal("GigabitEthernet0/0 not found in merged result")
	}
	if ge0.MACAddress != "aa:bb:cc:dd:ee:01" {
		t.Errorf("expected MAC from SNMP merge, got %q", ge0.MACAddress)
	}
	// Sources should include both
	sourceSet := map[string]bool{}
	for _, s := range result.Sources {
		sourceSet[s] = true
	}
	if !sourceSet["netconf"] || !sourceSet["snmp"] {
		t.Errorf("expected both sources, got %v", result.Sources)
	}
}

func TestEnricher_ConnectErrors(t *testing.T) {
	enricher, _ := newTestEnricher(t, nil, nil)

	device := Device{
		ID:   "err-device",
		Host: "10.99.99.99",
	}
	req := EnrichRequest{
		Netconf: &NetconfConfig{Host: "10.99.99.99", Username: "admin"},
		SNMP:    &SNMPConfig{Host: "10.99.99.99", Version: SNMPv2c},
	}

	result, err := enricher.Enrich(context.Background(), device, req)
	// Should not return a hard error — errors should be in result.Errors
	if err != nil {
		t.Fatalf("expected no error from Enrich, got: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Error("expected errors to be recorded in result.Errors")
	}
}

func TestEnricher_NeitherSource(t *testing.T) {
	enricher, _ := newTestEnricher(t, nil, nil)
	device := Device{ID: "test", Host: "10.0.0.1"}
	req := EnrichRequest{}

	result, err := enricher.Enrich(context.Background(), device, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.DeviceID != "test" {
		t.Errorf("expected device_id test, got %q", result.DeviceID)
	}
}

// --- Store enrichment persistence ---

func TestStoreEnrichedInventory(t *testing.T) {
	store := newTestStore(t)

	dev, err := store.CreateDevice(Device{
		Name:     "enriched-device",
		Host:     "10.0.0.5",
		Vendor:   VendorCisco,
		Username: "admin",
		AuthMode: AuthModePassword,
	})
	if err != nil {
		t.Fatalf("create device: %v", err)
	}

	inv := EnrichedInventory{
		DeviceID:    dev.ID,
		CollectedAt: time.Now().UTC(),
		Hostname:    "core-sw-1",
		Vendor:      "cisco",
		Firmware:    "15.1(4)M12a",
		Serial:      "SN123456",
		SysDescr:    "Cisco IOS Version 15.1",
		SysLocation: "DC1",
		Interfaces: []InterfaceDetail{
			{Name: "Gi0/0", SpeedMbps: 1000, AdminUp: true, OperUp: true},
			{Name: "Gi0/1", SpeedMbps: 100},
		},
		VLANs:   []VLANInfo{{ID: 1, Name: "default"}, {ID: 10, Name: "mgmt"}},
		Routes:  []RouteEntry{{Destination: "0.0.0.0", Prefix: 0, NextHop: "10.0.0.1"}},
		Sources: []string{"snmp"},
	}

	if err := store.SaveEnrichedInventory(inv); err != nil {
		t.Fatalf("save enriched: %v", err)
	}

	loaded, err := store.GetEnrichedInventory(dev.ID)
	if err != nil {
		t.Fatalf("get enriched: %v", err)
	}

	if loaded.Hostname != "core-sw-1" {
		t.Errorf("expected hostname, got %q", loaded.Hostname)
	}
	if loaded.Firmware != "15.1(4)M12a" {
		t.Errorf("expected firmware, got %q", loaded.Firmware)
	}
	if loaded.Serial != "SN123456" {
		t.Errorf("expected serial, got %q", loaded.Serial)
	}
	if len(loaded.Interfaces) != 2 {
		t.Fatalf("expected 2 interfaces, got %d", len(loaded.Interfaces))
	}
	if loaded.Interfaces[0].Name != "Gi0/0" {
		t.Errorf("expected Gi0/0, got %q", loaded.Interfaces[0].Name)
	}
	if len(loaded.VLANs) != 2 {
		t.Fatalf("expected 2 VLANs, got %d", len(loaded.VLANs))
	}
	if len(loaded.Routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(loaded.Routes))
	}
}

func TestStoreGetEnrichedInventory_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetEnrichedInventory("nonexistent-id")
	if !IsNotFound(err) {
		t.Errorf("expected not-found, got %v", err)
	}
}

func TestStoreGetInterfaceDetails(t *testing.T) {
	store := newTestStore(t)

	dev, _ := store.CreateDevice(Device{
		Name: "iface-device", Host: "10.0.0.6", Vendor: VendorGeneric, Username: "u", AuthMode: AuthModePassword,
	})

	inv := EnrichedInventory{
		DeviceID:    dev.ID,
		CollectedAt: time.Now().UTC(),
		Interfaces: []InterfaceDetail{
			{Name: "eth0", SpeedMbps: 1000, MACAddress: "aa:bb:cc:dd:ee:ff"},
		},
	}
	_ = store.SaveEnrichedInventory(inv)

	ifaces, err := store.GetInterfaceDetails(dev.ID)
	if err != nil {
		t.Fatalf("get interfaces: %v", err)
	}
	if len(ifaces) != 1 {
		t.Fatalf("expected 1, got %d", len(ifaces))
	}
	if ifaces[0].MACAddress != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("wrong MAC: %q", ifaces[0].MACAddress)
	}
}

// --- Handler tests for enrich + interfaces ---

func newTestHandlerWithEnricher(t *testing.T, opts EnricherOptions) (*Handler, *Store) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "handler-enrich.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewHandler(store, &fakeProber{}), store
}

func TestHandlerEnrichDevice_NoSource(t *testing.T) {
	h, store := newTestHandlerWithEnricher(t, EnricherOptions{})

	dev, _ := store.CreateDevice(Device{
		Name: "test-enrich", Host: "10.0.0.1", Vendor: VendorCisco, Username: "admin", AuthMode: AuthModePassword,
	})

	body, _ := json.Marshal(EnrichRequest{}) // neither netconf nor snmp
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.SetPathValue("id", dev.ID)
	rr := httptest.NewRecorder()
	h.HandleEnrichDevice(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestHandlerEnrichDevice_NotFound(t *testing.T) {
	h, _ := newTestHandlerWithEnricher(t, EnricherOptions{})

	body, _ := json.Marshal(EnrichRequest{SNMP: &SNMPConfig{Host: "10.0.0.1", Version: SNMPv2c}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.SetPathValue("id", "does-not-exist")
	rr := httptest.NewRecorder()
	h.HandleEnrichDevice(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestHandlerGetInterfaces_NotFound(t *testing.T) {
	h, _ := newTestHandlerWithEnricher(t, EnricherOptions{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetPathValue("id", "no-device")
	rr := httptest.NewRecorder()
	h.HandleGetInterfaces(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestHandlerGetInterfaces_NoEnrichment(t *testing.T) {
	h, store := newTestHandlerWithEnricher(t, EnricherOptions{})

	dev, _ := store.CreateDevice(Device{
		Name: "no-enrich", Host: "10.0.0.2", Vendor: VendorGeneric, Username: "u", AuthMode: AuthModePassword,
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetPathValue("id", dev.ID)
	rr := httptest.NewRecorder()
	h.HandleGetInterfaces(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 (no enrichment yet), got %d", rr.Code)
	}
}

func TestHandlerGetInterfaces_WithData(t *testing.T) {
	h, store := newTestHandlerWithEnricher(t, EnricherOptions{})

	dev, _ := store.CreateDevice(Device{
		Name: "with-enrich", Host: "10.0.0.3", Vendor: VendorCisco, Username: "admin", AuthMode: AuthModePassword,
	})

	_ = store.SaveEnrichedInventory(EnrichedInventory{
		DeviceID:    dev.ID,
		CollectedAt: time.Now().UTC(),
		Interfaces: []InterfaceDetail{
			{Name: "Gi0/0", SpeedMbps: 1000, AdminUp: true, OperUp: true},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetPathValue("id", dev.ID)
	rr := httptest.NewRecorder()
	h.HandleGetInterfaces(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d — body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	ifaces, ok := resp["interfaces"]
	if !ok {
		t.Fatal("expected 'interfaces' key in response")
	}
	ifaceSlice, ok := ifaces.([]interface{})
	if !ok {
		t.Fatalf("expected interfaces to be array, got %T", ifaces)
	}
	if len(ifaceSlice) != 1 {
		t.Errorf("expected 1 interface, got %d", len(ifaceSlice))
	}
}
