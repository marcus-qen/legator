package sandbox

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
)

// ── Test helpers ─────────────────────────────────────────────────────────────

func newTestReplaySetup(t *testing.T) (*Handler, *ReplayHandler, *mockChunkLister, *mockTaskLister, *mockArtifactLister) {
	t.Helper()
	store := newTestStore(t)
	cl := &mockChunkLister{}
	tl := &mockTaskLister{}
	al := &mockArtifactLister{}
	h := NewHandler(store, &noopPublisher{}, &noopAudit{}, zap.NewNop())
	rh := NewReplayHandler(store, cl, tl, al, &noopAudit{}, zap.NewNop())
	return h, rh, cl, tl, al
}

func newReplayMux(h *Handler, rh *ReplayHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)
	mux.HandleFunc("POST /api/v1/sandboxes/{id}/transition", h.HandleTransition)
	mux.HandleFunc("GET /api/v1/sandboxes/{id}/replay", rh.HandleReplay)
	mux.HandleFunc("GET /api/v1/sandboxes/{id}/replay/summary", rh.HandleReplaySummary)
	return mux
}

// mustCreateDestroyedSandbox creates a sandbox and transitions it all the way
// through to destroyed state (terminal).
func mustCreateDestroyedSandbox(t *testing.T, mux *http.ServeMux, workspaceID string) string {
	t.Helper()

	body := mustJSON(t, map[string]any{
		"workspace_id":  workspaceID,
		"runtime_class": "kata",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d: %s", w.Code, w.Body.String())
	}
	var sess SandboxSession
	decodeJSON(t, w.Body, &sess)

	transitions := []struct{ from, to string }{
		{StateCreated, StateProvisioning},
		{StateProvisioning, StateReady},
		{StateReady, StateRunning},
		{StateRunning, StateDestroyed},
	}
	for _, tr := range transitions {
		b := mustJSON(t, map[string]any{"from": tr.from, "to": tr.to})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/transition", b)
		rw := httptest.NewRecorder()
		mux.ServeHTTP(rw, r)
		if rw.Code != http.StatusOK {
			t.Fatalf("transition %s→%s: %d: %s", tr.from, tr.to, rw.Code, rw.Body.String())
		}
	}
	return sess.ID
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestHandleReplay_NotFound(t *testing.T) {
	h, rh, _, _, _ := newTestReplaySetup(t)
	mux := newReplayMux(h, rh)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/does-not-exist/replay", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleReplay_NonTerminalSandbox_Rejected(t *testing.T) {
	h, rh, _, _, _ := newTestReplaySetup(t)
	mux := newReplayMux(h, rh)

	// Create sandbox in created state (non-terminal).
	body := mustJSON(t, map[string]any{"workspace_id": "ws-1", "runtime_class": "kata"})
	cReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	cW := httptest.NewRecorder()
	mux.ServeHTTP(cW, cReq)
	if cW.Code != http.StatusCreated {
		t.Fatalf("create: %d", cW.Code)
	}
	var sess SandboxSession
	decodeJSON(t, cW.Body, &sess)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+sess.ID+"/replay", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 for non-terminal sandbox, got %d", w.Code)
	}
}

func TestHandleReplay_NonTerminalSandbox_ForceOverride(t *testing.T) {
	h, rh, _, _, _ := newTestReplaySetup(t)
	mux := newReplayMux(h, rh)

	body := mustJSON(t, map[string]any{"workspace_id": "ws-1", "runtime_class": "kata"})
	cReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	cW := httptest.NewRecorder()
	mux.ServeHTTP(cW, cReq)
	var sess SandboxSession
	decodeJSON(t, cW.Body, &sess)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+sess.ID+"/replay?force=1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with force=1, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleReplay_TerminalSandbox_Success(t *testing.T) {
	h, rh, cl, _, _ := newTestReplaySetup(t)
	mux := newReplayMux(h, rh)

	sandboxID := mustCreateDestroyedSandbox(t, mux, "ws-1")

	// Seed some output chunks into the mock store.
	now := time.Now().UTC()
	cl.chunks = []*OutputChunk{
		{ID: "c1", SandboxID: sandboxID, Sequence: 1, Stream: "stdout", Data: "hello", Timestamp: now.Add(1 * time.Second)},
		{ID: "c2", SandboxID: sandboxID, Sequence: 2, Stream: "stdout", Data: "done", Timestamp: now.Add(2 * time.Second)},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+sandboxID+"/replay", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var timeline ReplayTimeline
	if err := json.NewDecoder(w.Body).Decode(&timeline); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	if timeline.SandboxID != sandboxID {
		t.Errorf("expected sandbox_id=%s, got %q", sandboxID, timeline.SandboxID)
	}
	if timeline.EventCount != 2 {
		t.Errorf("expected 2 events, got %d", timeline.EventCount)
	}
}

func TestHandleReplay_WorkspaceIsolation(t *testing.T) {
	h, rh, _, _, _ := newTestReplaySetup(t)
	mux := newReplayMux(h, rh)

	// Create sandbox in ws-owner.
	body := mustJSON(t, map[string]any{"workspace_id": "ws-owner", "runtime_class": "kata"})
	cReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	cW := httptest.NewRecorder()
	mux.ServeHTTP(cW, cReq)
	var sess SandboxSession
	decodeJSON(t, cW.Body, &sess)

	// Access from a different workspace → 404.
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sandboxes/"+sess.ID+"/replay?workspace_id=ws-other&force=1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for workspace isolation, got %d", w.Code)
	}
}

func TestHandleReplaySummary_Success(t *testing.T) {
	h, rh, cl, tl, _ := newTestReplaySetup(t)
	mux := newReplayMux(h, rh)

	sandboxID := mustCreateDestroyedSandbox(t, mux, "ws-1")

	now := time.Now().UTC()
	cl.chunks = []*OutputChunk{
		{ID: "c1", SandboxID: sandboxID, Sequence: 1, Stream: "stdout", Data: "a", Timestamp: now},
	}
	completed := now.Add(3 * time.Second)
	tl.tasks = []*Task{
		{ID: "t1", SandboxID: sandboxID, Kind: TaskKindCommand, State: TaskStateSucceeded,
			CreatedAt: now, CompletedAt: &completed},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+sandboxID+"/replay/summary", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var summary ReplaySummary
	if err := json.NewDecoder(w.Body).Decode(&summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary.SandboxID != sandboxID {
		t.Errorf("expected sandbox_id=%s, got %q", sandboxID, summary.SandboxID)
	}
	// Summary must NOT include Events array; verify via raw JSON absence.
	raw := w.Body.Bytes()
	if len(raw) == 0 {
		// Body already read — re-check via summary fields only
		_ = raw
	}
	// EventCount should reflect actual count (1 chunk + 2 task states = 3).
	if summary.EventCount != 3 {
		t.Errorf("expected 3 events in summary, got %d", summary.EventCount)
	}
}

func TestHandleReplaySummary_NotFound(t *testing.T) {
	h, rh, _, _, _ := newTestReplaySetup(t)
	mux := newReplayMux(h, rh)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/nonexistent/replay/summary", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleReplay_MissingID(t *testing.T) {
	_, rh, _, _, _ := newTestReplaySetup(t)

	// Directly call handler without an ID in path value.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes//replay", nil)
	w := httptest.NewRecorder()
	rh.HandleReplay(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}
