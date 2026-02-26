package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/llm"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func TestWriteJSONError_UsesStableJSONEnvelope(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSONError(rr, http.StatusBadRequest, "invalid_request", `bad input: "quoted" value`)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json content type, got %q", ct)
	}

	var payload APIError
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if payload.Code != "invalid_request" {
		t.Fatalf("expected code invalid_request, got %q", payload.Code)
	}
	if payload.Error != `bad input: "quoted" value` {
		t.Fatalf("unexpected error message: %q", payload.Error)
	}
}

func TestHandleDeletePolicy_ErrorsAreValidJSON(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/policies/missing", nil)
	req.SetPathValue("id", `bad"id`)
	rr := httptest.NewRecorder()

	srv.handleDeletePolicy(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}

	var payload APIError
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("expected valid JSON error response, decode failed: %v (body=%q)", err, rr.Body.String())
	}
	if payload.Code != "not_found" {
		t.Fatalf("expected not_found code, got %q", payload.Code)
	}
	if payload.Error == "" {
		t.Fatal("expected non-empty error message")
	}
}

type failingProvider struct{}

func (f failingProvider) Name() string { return "failing" }

func (f failingProvider) Complete(context.Context, *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return nil, errors.New(`backend "down"`)
}

func TestHandleTask_ReturnsJSONWhenLLMUnavailable(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-llm", "host", "linux", "amd64")
	srv.taskRunner = llm.NewTaskRunner(failingProvider{}, func(probeID string, cmd *protocol.CommandPayload) (*protocol.CommandResultPayload, error) {
		return nil, nil
	}, zap.NewNop())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes/probe-llm/task", strings.NewReader(`{"task":"check status"}`))
	req.SetPathValue("id", "probe-llm")
	rr := httptest.NewRecorder()

	srv.handleTask(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", rr.Code, rr.Body.String())
	}

	var payload APIError
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode task error payload: %v", err)
	}
	if payload.Code != "llm_unavailable" {
		t.Fatalf("expected llm_unavailable code, got %q", payload.Code)
	}
	if !strings.Contains(payload.Error, "LLM provider is unavailable") {
		t.Fatalf("unexpected error message: %q", payload.Error)
	}
}
