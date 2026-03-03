package sandbox

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

// noopPublisher discards events silently.
type noopPublisher struct{}

func (n *noopPublisher) Publish(_ BusEvent) {}

// noopAudit discards audit events silently.
type noopAudit struct{}

func (n *noopAudit) Emit(_, _, _, _ string) {}

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	store := newTestStore(t)
	return NewHandler(store, &noopPublisher{}, &noopAudit{}, zap.NewNop())
}

func mustJSON(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewBuffer(b)
}

func decodeJSON(t *testing.T, body *bytes.Buffer, dest any) {
	t.Helper()
	if err := json.NewDecoder(body).Decode(dest); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// ── HandleCreate ──────────────────────────────────────────────────────────────

func TestHandleCreate_Success(t *testing.T) {
	h := newTestHandler(t)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)

	body := mustJSON(t, map[string]any{
		"workspace_id":  "ws-test",
		"probe_id":      "probe-1",
		"runtime_class": "kata",
		"created_by":    "alice",
		"ttl_seconds":   300,
		"metadata":      map[string]string{"env": "ci"},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var sess SandboxSession
	decodeJSON(t, w.Body, &sess)
	if sess.ID == "" {
		t.Fatal("expected session ID in response")
	}
	if sess.State != StateCreated {
		t.Fatalf("expected state %q, got %q", StateCreated, sess.State)
	}
	if sess.WorkspaceID != "ws-test" {
		t.Fatalf("unexpected workspace_id: %q", sess.WorkspaceID)
	}
	if sess.Metadata["env"] != "ci" {
		t.Fatalf("metadata not preserved: %v", sess.Metadata)
	}
}

func TestHandleCreate_MissingRuntimeClass(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)

	body := mustJSON(t, map[string]any{"workspace_id": "ws-1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleCreate_InvalidJSON(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", bytes.NewBufferString("{invalid"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ── HandleList ────────────────────────────────────────────────────────────────

func TestHandleList_Empty(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sandboxes", h.HandleList)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result map[string]any
	decodeJSON(t, w.Body, &result)
	if result["total"].(float64) != 0 {
		t.Fatalf("expected 0 total")
	}
}

func TestHandleList_WorkspaceFilter(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)
	mux.HandleFunc("GET /api/v1/sandboxes", h.HandleList)

	// Create two sessions in different workspaces.
	for _, ws := range []string{"ws-a", "ws-b"} {
		body := mustJSON(t, map[string]any{
			"workspace_id":  ws,
			"runtime_class": "kata",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("create failed for %s: %d", ws, w.Code)
		}
	}

	// List with ws-a filter.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes?workspace_id=ws-a", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var result map[string]any
	decodeJSON(t, w.Body, &result)
	if result["total"].(float64) != 1 {
		t.Fatalf("expected 1 session for ws-a, got %v", result["total"])
	}
}

// ── HandleGet ─────────────────────────────────────────────────────────────────

func TestHandleGet_Success(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)
	mux.HandleFunc("GET /api/v1/sandboxes/{id}", h.HandleGet)

	// Create a session.
	body := mustJSON(t, map[string]any{"workspace_id": "ws-1", "runtime_class": "kata"})
	cReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	cW := httptest.NewRecorder()
	mux.ServeHTTP(cW, cReq)
	var created SandboxSession
	decodeJSON(t, cW.Body, &created)

	// Fetch it.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+created.ID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got SandboxSession
	decodeJSON(t, w.Body, &got)
	if got.ID != created.ID {
		t.Fatalf("wrong ID: %q != %q", got.ID, created.ID)
	}
}

func TestHandleGet_NotFound(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sandboxes/{id}", h.HandleGet)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/no-such-id", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleGet_WorkspaceIsolation(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)
	mux.HandleFunc("GET /api/v1/sandboxes/{id}", h.HandleGet)

	body := mustJSON(t, map[string]any{"workspace_id": "ws-owner", "runtime_class": "kata"})
	cReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	cW := httptest.NewRecorder()
	mux.ServeHTTP(cW, cReq)
	var created SandboxSession
	decodeJSON(t, cW.Body, &created)

	// Different workspace cannot see it.
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sandboxes/"+created.ID+"?workspace_id=ws-other", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-workspace get, got %d", w.Code)
	}
}

// ── HandleDestroy ─────────────────────────────────────────────────────────────

func TestHandleDestroy_Success(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)
	mux.HandleFunc("POST /api/v1/sandboxes/{id}/transition", h.HandleTransition)
	mux.HandleFunc("DELETE /api/v1/sandboxes/{id}", h.HandleDestroy)
	mux.HandleFunc("GET /api/v1/sandboxes/{id}", h.HandleGet)

	body := mustJSON(t, map[string]any{"workspace_id": "ws-1", "runtime_class": "kata"})
	cReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	cW := httptest.NewRecorder()
	mux.ServeHTTP(cW, cReq)
	var created SandboxSession
	decodeJSON(t, cW.Body, &created)

	// Advance to ready state so destroy is allowed.
	for _, step := range []struct{ from, to string }{
		{StateCreated, StateProvisioning},
		{StateProvisioning, StateReady},
	} {
		tb := mustJSON(t, map[string]any{"from": step.from, "to": step.to})
		tr := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+created.ID+"/transition", tb)
		if rr := httptest.NewRecorder(); func() int { mux.ServeHTTP(rr, tr); return rr.Code }() != http.StatusOK {
			t.Fatalf("advance %s→%s failed", step.from, step.to)
		}
	}

	dReq := httptest.NewRequest(http.MethodDelete, "/api/v1/sandboxes/"+created.ID, nil)
	dW := httptest.NewRecorder()
	mux.ServeHTTP(dW, dReq)

	if dW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", dW.Code, dW.Body.String())
	}

	var destroyed SandboxSession
	decodeJSON(t, dW.Body, &destroyed)
	if destroyed.State != StateDestroyed {
		t.Fatalf("expected destroyed state, got %q", destroyed.State)
	}
}

func TestHandleDestroy_AlreadyTerminal(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)
	mux.HandleFunc("POST /api/v1/sandboxes/{id}/transition", h.HandleTransition)
	mux.HandleFunc("DELETE /api/v1/sandboxes/{id}", h.HandleDestroy)

	body := mustJSON(t, map[string]any{"workspace_id": "ws-1", "runtime_class": "kata"})
	cReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	cW := httptest.NewRecorder()
	mux.ServeHTTP(cW, cReq)
	var created SandboxSession
	decodeJSON(t, cW.Body, &created)

	// Advance to ready so destroy is allowed.
	for _, step := range []struct{ from, to string }{
		{StateCreated, StateProvisioning},
		{StateProvisioning, StateReady},
	} {
		tb := mustJSON(t, map[string]any{"from": step.from, "to": step.to})
		tr := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+created.ID+"/transition", tb)
		mux.ServeHTTP(httptest.NewRecorder(), tr)
	}

	// Destroy first time.
	r1 := httptest.NewRequest(http.MethodDelete, "/api/v1/sandboxes/"+created.ID, nil)
	mux.ServeHTTP(httptest.NewRecorder(), r1)

	// Destroy second time — should conflict.
	r2 := httptest.NewRequest(http.MethodDelete, "/api/v1/sandboxes/"+created.ID, nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, r2)

	if w2.Code != http.StatusConflict {
		t.Fatalf("expected 409 for already-destroyed session, got %d", w2.Code)
	}
}

func TestHandleDestroy_NotFound(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/v1/sandboxes/{id}", h.HandleDestroy)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/sandboxes/ghost-id", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ── HandleTransition ──────────────────────────────────────────────────────────

func TestHandleTransition_Valid(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)
	mux.HandleFunc("POST /api/v1/sandboxes/{id}/transition", h.HandleTransition)

	body := mustJSON(t, map[string]any{"workspace_id": "ws-1", "runtime_class": "kata"})
	cReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	cW := httptest.NewRecorder()
	mux.ServeHTTP(cW, cReq)
	var created SandboxSession
	decodeJSON(t, cW.Body, &created)

	tBody := mustJSON(t, map[string]any{
		"from": StateCreated,
		"to":   StateProvisioning,
	})
	tReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+created.ID+"/transition", tBody)
	tW := httptest.NewRecorder()
	mux.ServeHTTP(tW, tReq)

	if tW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", tW.Code, tW.Body.String())
	}
	var updated SandboxSession
	decodeJSON(t, tW.Body, &updated)
	if updated.State != StateProvisioning {
		t.Fatalf("expected provisioning, got %q", updated.State)
	}
}

func TestHandleTransition_Invalid(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)
	mux.HandleFunc("POST /api/v1/sandboxes/{id}/transition", h.HandleTransition)

	body := mustJSON(t, map[string]any{"workspace_id": "ws-1", "runtime_class": "kata"})
	cReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	cW := httptest.NewRecorder()
	mux.ServeHTTP(cW, cReq)
	var created SandboxSession
	decodeJSON(t, cW.Body, &created)

	// Invalid: created → running
	tBody := mustJSON(t, map[string]any{"from": StateCreated, "to": StateRunning})
	tReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+created.ID+"/transition", tBody)
	tW := httptest.NewRecorder()
	mux.ServeHTTP(tW, tReq)

	if tW.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", tW.Code)
	}
}

func TestHandleTransition_MissingFields(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)
	mux.HandleFunc("POST /api/v1/sandboxes/{id}/transition", h.HandleTransition)

	body := mustJSON(t, map[string]any{"workspace_id": "ws-1", "runtime_class": "kata"})
	cReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	cW := httptest.NewRecorder()
	mux.ServeHTTP(cW, cReq)
	var created SandboxSession
	decodeJSON(t, cW.Body, &created)

	tBody := mustJSON(t, map[string]any{"from": StateCreated})
	tReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+created.ID+"/transition", tBody)
	tW := httptest.NewRecorder()
	mux.ServeHTTP(tW, tReq)

	if tW.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", tW.Code)
	}
}

func TestHandleTransition_WithErrorMessage(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)
	mux.HandleFunc("POST /api/v1/sandboxes/{id}/transition", h.HandleTransition)

	body := mustJSON(t, map[string]any{"workspace_id": "ws-1", "runtime_class": "kata"})
	cReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	cW := httptest.NewRecorder()
	mux.ServeHTTP(cW, cReq)
	var created SandboxSession
	decodeJSON(t, cW.Body, &created)

	// Move to provisioning first.
	tBody1 := mustJSON(t, map[string]any{"from": StateCreated, "to": StateProvisioning})
	mux.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+created.ID+"/transition", tBody1))

	// Then fail it with an error message.
	tBody2 := mustJSON(t, map[string]any{
		"from":          StateProvisioning,
		"to":            StateFailed,
		"error_message": "container image pull failed",
	})
	tReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+created.ID+"/transition", tBody2)
	tW := httptest.NewRecorder()
	mux.ServeHTTP(tW, tReq)

	if tW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", tW.Code, tW.Body.String())
	}
	var updated SandboxSession
	decodeJSON(t, tW.Body, &updated)
	if updated.State != StateFailed {
		t.Fatalf("expected failed, got %q", updated.State)
	}
	if updated.ErrorMessage != "container image pull failed" {
		t.Fatalf("error message not preserved: %q", updated.ErrorMessage)
	}
}

func TestHandleTransition_NotFound(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes/{id}/transition", h.HandleTransition)

	body := mustJSON(t, map[string]any{"from": StateCreated, "to": StateProvisioning})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/ghost/transition", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
