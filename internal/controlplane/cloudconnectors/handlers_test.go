package cloudconnectors

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type scannerFunc func(connector Connector) ([]Asset, error)

func (f scannerFunc) Scan(_ context.Context, connector Connector) ([]Asset, error) {
	return f(connector)
}

func newTestHandler(t *testing.T, scanner Scanner) (*Handler, *Store) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "cloud.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewHandler(store, scanner), store
}

func TestHandlerCreateListAndDeleteConnector(t *testing.T) {
	h, _ := newTestHandler(t, scannerFunc(func(connector Connector) ([]Asset, error) {
		return nil, nil
	}))

	body := map[string]any{
		"name":       "AWS Prod",
		"provider":   "aws",
		"auth_mode":  "cli",
		"is_enabled": true,
	}
	data, _ := json.Marshal(body)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/cloud/connectors", bytes.NewReader(data))
	createRR := httptest.NewRecorder()
	h.HandleCreateConnector(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", createRR.Code, createRR.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/cloud/connectors", nil)
	listRR := httptest.NewRecorder()
	h.HandleListConnectors(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRR.Code)
	}
	if !strings.Contains(listRR.Body.String(), "AWS Prod") {
		t.Fatalf("expected connector in list: %s", listRR.Body.String())
	}

	var listPayload struct {
		Connectors []Connector `json:"connectors"`
	}
	if err := json.NewDecoder(listRR.Body).Decode(&listPayload); err != nil {
		t.Fatalf("decode list payload: %v", err)
	}
	if len(listPayload.Connectors) != 1 {
		t.Fatalf("expected 1 connector, got %d", len(listPayload.Connectors))
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/cloud/connectors/1", nil)
	deleteReq.SetPathValue("id", listPayload.Connectors[0].ID)
	deleteRR := httptest.NewRecorder()
	h.HandleDeleteConnector(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusOK {
		t.Fatalf("expected 200 delete, got %d: %s", deleteRR.Code, deleteRR.Body.String())
	}
}

func TestHandlerScanSuccessPersistsAssets(t *testing.T) {
	h, store := newTestHandler(t, scannerFunc(func(connector Connector) ([]Asset, error) {
		return []Asset{
			{
				Provider:    connector.Provider,
				ScopeID:     "123456789012",
				Region:      "us-east-1",
				AssetType:   "account",
				AssetID:     "123456789012",
				DisplayName: "prod-account",
				Status:      "active",
				RawJSON:     `{"account":"123456789012"}`,
			},
		}, nil
	}))

	connector, err := store.CreateConnector(Connector{Name: "AWS", Provider: ProviderAWS, AuthMode: AuthModeCLI, IsEnabled: true})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}

	scanReq := httptest.NewRequest(http.MethodPost, "/api/v1/cloud/connectors/scan", nil)
	scanReq.SetPathValue("id", connector.ID)
	scanRR := httptest.NewRecorder()
	h.HandleScanConnector(scanRR, scanReq)
	if scanRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", scanRR.Code, scanRR.Body.String())
	}

	updated, err := store.GetConnector(connector.ID)
	if err != nil {
		t.Fatalf("get connector: %v", err)
	}
	if updated.LastStatus != ScanStatusSuccess {
		t.Fatalf("expected success status, got %q", updated.LastStatus)
	}
	if updated.LastError != "" {
		t.Fatalf("expected empty last error, got %q", updated.LastError)
	}

	assets, err := store.ListAssets(AssetFilter{ConnectorID: connector.ID, Limit: 10})
	if err != nil {
		t.Fatalf("list assets: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(assets))
	}
	if assets[0].AssetType != "account" || assets[0].AssetID != "123456789012" {
		t.Fatalf("unexpected asset: %+v", assets[0])
	}
}

func TestHandlerScanFailureSetsConnectorError(t *testing.T) {
	h, store := newTestHandler(t, scannerFunc(func(connector Connector) ([]Asset, error) {
		return nil, &ScanError{Code: "auth_failed", Message: "aws CLI is not authenticated"}
	}))

	connector, err := store.CreateConnector(Connector{Name: "AWS", Provider: ProviderAWS, AuthMode: AuthModeCLI, IsEnabled: true})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}

	scanReq := httptest.NewRequest(http.MethodPost, "/api/v1/cloud/connectors/scan", nil)
	scanReq.SetPathValue("id", connector.ID)
	scanRR := httptest.NewRecorder()
	h.HandleScanConnector(scanRR, scanReq)
	if scanRR.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", scanRR.Code, scanRR.Body.String())
	}

	updated, err := store.GetConnector(connector.ID)
	if err != nil {
		t.Fatalf("get connector: %v", err)
	}
	if updated.LastStatus != ScanStatusError {
		t.Fatalf("expected error status, got %q", updated.LastStatus)
	}
	if !strings.Contains(updated.LastError, "not authenticated") {
		t.Fatalf("expected auth error in last_error, got %q", updated.LastError)
	}
}

func TestHandlerListAssetsFilterAndLimit(t *testing.T) {
	h, store := newTestHandler(t, scannerFunc(func(connector Connector) ([]Asset, error) { return nil, nil }))

	connector, err := store.CreateConnector(Connector{Name: "GCP", Provider: ProviderGCP, AuthMode: AuthModeCLI, IsEnabled: true})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}

	if err := store.ReplaceAssetsForConnector(*connector, []Asset{
		{Provider: ProviderGCP, ScopeID: "proj-1", Region: "europe-west1", AssetType: "project", AssetID: "proj-1", DisplayName: "Proj 1", RawJSON: `{}`},
		{Provider: ProviderGCP, ScopeID: "proj-1", Region: "europe-west1", AssetType: "instance", AssetID: "i-1", DisplayName: "VM 1", RawJSON: `{}`},
	}); err != nil {
		t.Fatalf("replace assets: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cloud/assets?provider=gcp&connector_id="+connector.ID+"&limit=1", nil)
	rr := httptest.NewRecorder()
	h.HandleListAssets(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Assets []Asset `json:"assets"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Assets) != 1 {
		t.Fatalf("expected 1 asset from limit=1, got %d", len(payload.Assets))
	}
}
