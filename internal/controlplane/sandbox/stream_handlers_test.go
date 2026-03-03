package sandbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
)

// ── Test helpers ──────────────────────────────────────────────────────────────

func newTestStreamHandler(t *testing.T) (*Handler, *StreamHandler, *StreamHub) {
	t.Helper()
	store := newTestStore(t)
	streamStore, err := NewStreamStore(store.DB())
	if err != nil {
		t.Fatalf("NewStreamStore: %v", err)
	}
	hub := NewStreamHub()
	t.Cleanup(hub.Close)
	h := NewHandler(store, &noopPublisher{}, &noopAudit{}, zap.NewNop())
	sh := NewStreamHandler(store, streamStore, hub, &noopPublisher{}, &noopAudit{}, zap.NewNop())
	return h, sh, hub
}

func newStreamTestMux(h *Handler, sh *StreamHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)
	mux.HandleFunc("GET /api/v1/sandboxes/{id}", h.HandleGet)
	mux.HandleFunc("POST /api/v1/sandboxes/{id}/transition", h.HandleTransition)
	mux.HandleFunc("DELETE /api/v1/sandboxes/{id}", h.HandleDestroy)
	mux.HandleFunc("POST /api/v1/sandboxes/{id}/output", sh.HandleIngestOutput)
	mux.HandleFunc("GET /api/v1/sandboxes/{id}/output", sh.HandleGetOutput)
	return mux
}

// advanceSandboxToRunning creates a sandbox and advances it to the running state.
func advanceSandboxToRunning(t *testing.T, mux *http.ServeMux) SandboxSession {
	t.Helper()

	// Create
	body := mustJSON(t, map[string]any{"runtime_class": "kata"})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("create sandbox: %d %s", w.Code, w.Body.String())
	}
	var sess SandboxSession
	decodeJSON(t, w.Body, &sess)

	steps := []struct{ from, to string }{
		{StateCreated, StateProvisioning},
		{StateProvisioning, StateReady},
		{StateReady, StateRunning},
	}
	for _, step := range steps {
		tb := mustJSON(t, map[string]any{"from": step.from, "to": step.to})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost,
			"/api/v1/sandboxes/"+sess.ID+"/transition", tb))
		if rr.Code != http.StatusOK {
			t.Fatalf("transition %s→%s: %d", step.from, step.to, rr.Code)
		}
	}
	return sess
}

// ── HandleIngestOutput ────────────────────────────────────────────────────────

func TestHandleIngestOutput_Success(t *testing.T) {
	h, sh, _ := newTestStreamHandler(t)
	mux := newStreamTestMux(h, sh)

	sess := advanceSandboxToRunning(t, mux)

	chunks := []map[string]any{
		{"task_id": "t1", "sequence": 1, "stream": "stdout", "data": "hello", "timestamp": time.Now()},
		{"task_id": "t1", "sequence": 2, "stream": "stderr", "data": "world", "timestamp": time.Now()},
	}
	body, _ := json.Marshal(chunks)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/output",
		bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	decodeJSON(t, w.Body, &resp)
	nextSeq, ok := resp["next_sequence"].(float64)
	if !ok {
		t.Fatal("expected next_sequence in response")
	}
	if int(nextSeq) != 3 {
		t.Errorf("expected next_sequence=3, got %v", nextSeq)
	}
}

func TestHandleIngestOutput_SandboxNotFound(t *testing.T) {
	h, sh, _ := newTestStreamHandler(t)
	mux := newStreamTestMux(h, sh)

	body, _ := json.Marshal([]map[string]any{{"sequence": 1, "stream": "stdout", "data": "x"}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/nonexistent/output",
		bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleIngestOutput_SandboxNotRunning(t *testing.T) {
	h, sh, _ := newTestStreamHandler(t)
	mux := newStreamTestMux(h, sh)

	// Create sandbox but do NOT advance to running.
	body := mustJSON(t, map[string]any{"runtime_class": "kata"})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body))
	var sess SandboxSession
	decodeJSON(t, w.Body, &sess)

	chunks, _ := json.Marshal([]map[string]any{{"sequence": 1, "stream": "stdout", "data": "x"}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/output",
		bytes.NewReader(chunks))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rr.Code)
	}
}

func TestHandleIngestOutput_EmptyBatch(t *testing.T) {
	h, sh, _ := newTestStreamHandler(t)
	mux := newStreamTestMux(h, sh)

	sess := advanceSandboxToRunning(t, mux)

	body, _ := json.Marshal([]map[string]any{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/output",
		bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleIngestOutput_InvalidJSON(t *testing.T) {
	h, sh, _ := newTestStreamHandler(t)
	mux := newStreamTestMux(h, sh)

	sess := advanceSandboxToRunning(t, mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/output",
		bytes.NewBufferString("not json"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleIngestOutput_BroadcastsToHub(t *testing.T) {
	h, sh, hub := newTestStreamHandler(t)
	mux := newStreamTestMux(h, sh)

	sess := advanceSandboxToRunning(t, mux)

	ch, unsub := hub.Subscribe(sess.ID)
	defer unsub()

	chunks := []map[string]any{
		{"task_id": "t1", "sequence": 1, "stream": "stdout", "data": "broadcast-test"},
	}
	body, _ := json.Marshal(chunks)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/output",
		bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("ingest failed: %d", w.Code)
	}

	select {
	case got := <-ch:
		if got.Data != "broadcast-test" {
			t.Errorf("expected 'broadcast-test', got %q", got.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broadcast")
	}
}

// ── HandleGetOutput ───────────────────────────────────────────────────────────

func TestHandleGetOutput_Success(t *testing.T) {
	h, sh, _ := newTestStreamHandler(t)
	mux := newStreamTestMux(h, sh)

	sess := advanceSandboxToRunning(t, mux)

	// Ingest some chunks.
	chunks := make([]map[string]any, 5)
	for i := range chunks {
		chunks[i] = map[string]any{
			"task_id":  "t1",
			"sequence": i + 1,
			"stream":   "stdout",
			"data":     fmt.Sprintf("line%d", i+1),
		}
	}
	body, _ := json.Marshal(chunks)
	mux.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/output",
			bytes.NewReader(body)))

	// Retrieve all.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+sess.ID+"/output", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Chunks []OutputChunk `json:"chunks"`
		Total  int           `json:"total"`
	}
	decodeJSON(t, w.Body, &resp)
	if resp.Total != 5 {
		t.Errorf("expected 5 chunks, got %d", resp.Total)
	}
}

func TestHandleGetOutput_SinceFilter(t *testing.T) {
	h, sh, _ := newTestStreamHandler(t)
	mux := newStreamTestMux(h, sh)

	sess := advanceSandboxToRunning(t, mux)

	chunks := make([]map[string]any, 10)
	for i := range chunks {
		chunks[i] = map[string]any{
			"task_id":  "t1",
			"sequence": i + 1,
			"stream":   "stdout",
			"data":     "x",
		}
	}
	body, _ := json.Marshal(chunks)
	mux.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/output",
			bytes.NewReader(body)))

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sandboxes/"+sess.ID+"/output?since=7", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp struct {
		Total int `json:"total"`
	}
	decodeJSON(t, w.Body, &resp)
	if resp.Total != 3 {
		t.Errorf("expected 3 chunks (seq 8,9,10), got %d", resp.Total)
	}
}

func TestHandleGetOutput_TaskIDFilter(t *testing.T) {
	h, sh, _ := newTestStreamHandler(t)
	mux := newStreamTestMux(h, sh)

	sess := advanceSandboxToRunning(t, mux)

	chunks := []map[string]any{
		{"task_id": "taskA", "sequence": 1, "stream": "stdout", "data": "a"},
		{"task_id": "taskB", "sequence": 2, "stream": "stdout", "data": "b"},
		{"task_id": "taskA", "sequence": 3, "stream": "stdout", "data": "c"},
	}
	body, _ := json.Marshal(chunks)
	mux.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/output",
			bytes.NewReader(body)))

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sandboxes/"+sess.ID+"/output?task_id=taskA", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp struct {
		Total int `json:"total"`
	}
	decodeJSON(t, w.Body, &resp)
	if resp.Total != 2 {
		t.Errorf("expected 2 chunks for taskA, got %d", resp.Total)
	}
}

func TestHandleGetOutput_NotFound(t *testing.T) {
	h, sh, _ := newTestStreamHandler(t)
	mux := newStreamTestMux(h, sh)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/nonexistent/output", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleGetOutput_EmptyResult(t *testing.T) {
	h, sh, _ := newTestStreamHandler(t)
	mux := newStreamTestMux(h, sh)

	sess := advanceSandboxToRunning(t, mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+sess.ID+"/output", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Chunks []OutputChunk `json:"chunks"`
		Total  int           `json:"total"`
	}
	decodeJSON(t, w.Body, &resp)
	if resp.Total != 0 {
		t.Errorf("expected 0 chunks, got %d", resp.Total)
	}
	if resp.Chunks == nil {
		t.Error("expected non-nil chunks slice")
	}
}
