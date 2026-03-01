package automationpacks

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	store := newTestStore(t)
	return NewHandler(store)
}

func TestHandlerCreateListAndGetDefinition(t *testing.T) {
	h := newTestHandler(t)
	def := validDefinitionFixture()
	body, _ := json.Marshal(def)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/automation-packs", bytes.NewReader(body))
	createRR := httptest.NewRecorder()
	h.HandleCreateDefinition(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", createRR.Code, createRR.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/automation-packs", nil)
	listRR := httptest.NewRecorder()
	h.HandleListDefinitions(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRR.Code)
	}

	var listPayload struct {
		AutomationPacks []DefinitionSummary `json:"automation_packs"`
	}
	if err := json.NewDecoder(listRR.Body).Decode(&listPayload); err != nil {
		t.Fatalf("decode list payload: %v", err)
	}
	if len(listPayload.AutomationPacks) != 1 {
		t.Fatalf("expected 1 pack, got %d", len(listPayload.AutomationPacks))
	}

	pack := listPayload.AutomationPacks[0]
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/automation-packs/"+pack.Metadata.ID+"?version="+pack.Metadata.Version, nil)
	getReq.SetPathValue("id", pack.Metadata.ID)
	getRR := httptest.NewRecorder()
	h.HandleGetDefinition(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", getRR.Code, getRR.Body.String())
	}
}

func TestHandlerCreateDefinitionRejectsInvalidBody(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/automation-packs", bytes.NewBufferString(`{"metadata":`))
	rr := httptest.NewRecorder()
	h.HandleCreateDefinition(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestHandlerCreateDefinitionRejectsInvalidSchema(t *testing.T) {
	h := newTestHandler(t)
	invalid := Definition{Metadata: Metadata{ID: "bad", Version: "1.0.0"}}
	body, _ := json.Marshal(invalid)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/automation-packs", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.HandleCreateDefinition(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandlerGetDefinitionNotFound(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/automation-packs/missing", nil)
	req.SetPathValue("id", "missing")
	rr := httptest.NewRecorder()
	h.HandleGetDefinition(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}
