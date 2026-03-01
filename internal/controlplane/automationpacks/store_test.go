package automationpacks

import (
	"errors"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "automationpacks.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStoreCreateGetAndListDefinitions(t *testing.T) {
	store := newTestStore(t)
	def := validDefinitionFixture()

	created, err := store.CreateDefinition(def)
	if err != nil {
		t.Fatalf("create definition: %v", err)
	}
	if created.Metadata.ID == "" || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("expected persisted timestamps and metadata, got %+v", created)
	}

	got, err := store.GetDefinition(created.Metadata.ID, created.Metadata.Version)
	if err != nil {
		t.Fatalf("get definition: %v", err)
	}
	if got.Metadata.Name != created.Metadata.Name {
		t.Fatalf("expected %q, got %q", created.Metadata.Name, got.Metadata.Name)
	}

	summaries, err := store.ListDefinitions()
	if err != nil {
		t.Fatalf("list definitions: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 definition in list, got %d", len(summaries))
	}
	if summaries[0].InputCount != len(def.Inputs) || summaries[0].StepCount != len(def.Steps) {
		t.Fatalf("unexpected summary counts: %+v", summaries[0])
	}
}

func TestStoreGetDefinitionReturnsLatestWhenVersionNotProvided(t *testing.T) {
	store := newTestStore(t)
	defV1 := validDefinitionFixture()
	defV2 := validDefinitionFixture()
	defV2.Metadata.Version = "1.1.0"
	defV2.Metadata.Description = "updated description"

	if _, err := store.CreateDefinition(defV1); err != nil {
		t.Fatalf("create v1: %v", err)
	}
	if _, err := store.CreateDefinition(defV2); err != nil {
		t.Fatalf("create v2: %v", err)
	}

	latest, err := store.GetDefinition(defV1.Metadata.ID, "")
	if err != nil {
		t.Fatalf("get latest definition: %v", err)
	}
	if latest.Metadata.Version != "1.1.0" {
		t.Fatalf("expected latest version 1.1.0, got %q", latest.Metadata.Version)
	}
}

func TestStoreCreateDefinitionRejectsDuplicateIDVersion(t *testing.T) {
	store := newTestStore(t)
	def := validDefinitionFixture()

	if _, err := store.CreateDefinition(def); err != nil {
		t.Fatalf("create definition: %v", err)
	}

	_, err := store.CreateDefinition(def)
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestStoreCreateDefinitionRejectsInvalidSchema(t *testing.T) {
	store := newTestStore(t)
	invalid := Definition{Metadata: Metadata{ID: "x", Name: "", Version: "bad"}}

	_, err := store.CreateDefinition(invalid)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("expected ValidationError, got %T", err)
	}
}
