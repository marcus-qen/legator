package cloudconnectors

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "cloud.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStoreCreateUpdateAndListConnectors(t *testing.T) {
	store := newTestStore(t)

	created, err := store.CreateConnector(Connector{
		Name:      "AWS Prod",
		Provider:  ProviderAWS,
		AuthMode:  AuthModeCLI,
		IsEnabled: true,
	})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected connector id")
	}
	if created.Provider != ProviderAWS {
		t.Fatalf("expected provider aws, got %q", created.Provider)
	}

	updated, err := store.UpdateConnector(created.ID, Connector{
		Name:      "AWS Prod Updated",
		Provider:  ProviderAWS,
		AuthMode:  AuthModeCLI,
		IsEnabled: false,
	})
	if err != nil {
		t.Fatalf("update connector: %v", err)
	}
	if updated.Name != "AWS Prod Updated" {
		t.Fatalf("unexpected updated name: %q", updated.Name)
	}
	if updated.IsEnabled {
		t.Fatal("expected connector to be disabled")
	}

	now := time.Now().UTC()
	if err := store.SetConnectorScanResult(created.ID, ScanStatusError, "auth failed", now); err != nil {
		t.Fatalf("set scan result: %v", err)
	}

	loaded, err := store.GetConnector(created.ID)
	if err != nil {
		t.Fatalf("get connector: %v", err)
	}
	if loaded.LastStatus != ScanStatusError {
		t.Fatalf("expected last status error, got %q", loaded.LastStatus)
	}
	if loaded.LastError != "auth failed" {
		t.Fatalf("unexpected last error: %q", loaded.LastError)
	}

	connectors, err := store.ListConnectors()
	if err != nil {
		t.Fatalf("list connectors: %v", err)
	}
	if len(connectors) != 1 {
		t.Fatalf("expected 1 connector, got %d", len(connectors))
	}
}

func TestStoreReplaceAssetsAndFilter(t *testing.T) {
	store := newTestStore(t)

	awsConn, err := store.CreateConnector(Connector{Name: "AWS", Provider: ProviderAWS, AuthMode: AuthModeCLI, IsEnabled: true})
	if err != nil {
		t.Fatalf("create aws connector: %v", err)
	}
	gcpConn, err := store.CreateConnector(Connector{Name: "GCP", Provider: ProviderGCP, AuthMode: AuthModeCLI, IsEnabled: true})
	if err != nil {
		t.Fatalf("create gcp connector: %v", err)
	}

	if err := store.ReplaceAssetsForConnector(*awsConn, []Asset{
		{Provider: ProviderAWS, ScopeID: "123", Region: "us-east-1", AssetType: "account", AssetID: "123", DisplayName: "acct", Status: "active", RawJSON: `{}`},
		{Provider: ProviderAWS, ScopeID: "123", Region: "us-east-1", AssetType: "instance", AssetID: "i-1", DisplayName: "vm-1", Status: "running", RawJSON: `{"id":"i-1"}`},
	}); err != nil {
		t.Fatalf("replace aws assets: %v", err)
	}
	if err := store.ReplaceAssetsForConnector(*gcpConn, []Asset{
		{Provider: ProviderGCP, ScopeID: "proj-1", Region: "europe-west1", AssetType: "project", AssetID: "proj-1", DisplayName: "Project One", Status: "ACTIVE", RawJSON: `{}`},
	}); err != nil {
		t.Fatalf("replace gcp assets: %v", err)
	}

	assetsAll, err := store.ListAssets(AssetFilter{Limit: 20})
	if err != nil {
		t.Fatalf("list all assets: %v", err)
	}
	if len(assetsAll) != 3 {
		t.Fatalf("expected 3 assets, got %d", len(assetsAll))
	}

	awsAssets, err := store.ListAssets(AssetFilter{Provider: ProviderAWS, Limit: 20})
	if err != nil {
		t.Fatalf("list aws assets: %v", err)
	}
	if len(awsAssets) != 2 {
		t.Fatalf("expected 2 aws assets, got %d", len(awsAssets))
	}

	connectorAssets, err := store.ListAssets(AssetFilter{ConnectorID: gcpConn.ID, Limit: 20})
	if err != nil {
		t.Fatalf("list connector assets: %v", err)
	}
	if len(connectorAssets) != 1 {
		t.Fatalf("expected 1 gcp asset, got %d", len(connectorAssets))
	}

	limited, err := store.ListAssets(AssetFilter{Limit: 1})
	if err != nil {
		t.Fatalf("list limited assets: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("expected 1 limited asset, got %d", len(limited))
	}

	if err := store.DeleteConnector(awsConn.ID); err != nil {
		t.Fatalf("delete connector: %v", err)
	}

	remaining, err := store.ListAssets(AssetFilter{Limit: 20})
	if err != nil {
		t.Fatalf("list remaining assets: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("expected cascading delete to keep 1 asset, got %d", len(remaining))
	}
}
