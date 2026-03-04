package sandbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// newTestArtifactHandler creates a Handler + ArtifactHandler sharing the same SQLite db.
func newTestArtifactHandler(t *testing.T) (*Handler, *ArtifactHandler) {
	t.Helper()
	store := newTestStore(t)
	as, err := NewArtifactStore(store.DB())
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	h := NewHandler(store, &noopPublisher{}, &noopAudit{}, zap.NewNop())
	ah := NewArtifactHandler(store, as, &noopPublisher{}, &noopAudit{}, zap.NewNop())
	return h, ah
}

func newArtifactTestMux(h *Handler, ah *ArtifactHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)
	mux.HandleFunc("GET /api/v1/sandboxes/{id}", h.HandleGet)
	mux.HandleFunc("POST /api/v1/sandboxes/{id}/transition", h.HandleTransition)
	mux.HandleFunc("POST /api/v1/sandboxes/{id}/artifacts", ah.HandleUploadArtifact)
	mux.HandleFunc("GET /api/v1/sandboxes/{id}/artifacts", ah.HandleListArtifacts)
	mux.HandleFunc("GET /api/v1/sandboxes/{id}/artifacts/{artifactId}", ah.HandleGetArtifact)
	mux.HandleFunc("GET /api/v1/sandboxes/{id}/artifacts/{artifactId}/content", ah.HandleDownloadArtifact)
	return mux
}

// mustCreateReadySandbox creates a sandbox in ready state for handler tests.
func mustCreateReadySandbox(t *testing.T, mux *http.ServeMux, workspaceID string) string {
	t.Helper()
	body := mustJSON(t, map[string]any{
		"workspace_id":  workspaceID,
		"runtime_class": "kata",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create sandbox: %d: %s", w.Code, w.Body.String())
	}
	var sess SandboxSession
	decodeJSON(t, w.Body, &sess)

	steps := []struct{ from, to string }{
		{StateCreated, StateProvisioning},
		{StateProvisioning, StateReady},
	}
	for _, s := range steps {
		b := mustJSON(t, map[string]any{"from": s.from, "to": s.to})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/transition", b)
		rw := httptest.NewRecorder()
		mux.ServeHTTP(rw, r)
		if rw.Code != http.StatusOK {
			t.Fatalf("transition %s→%s: %d", s.from, s.to, rw.Code)
		}
	}
	return sess.ID
}

// buildMultipartUpload creates a multipart body for artifact upload.
func buildMultipartUpload(t *testing.T, filename, content, path, kind, taskID, mimeType string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// File field
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	if mimeType != "" {
		h.Set("Content-Type", mimeType)
	}
	part, err := w.CreatePart(h)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := io.WriteString(part, content); err != nil {
		t.Fatalf("write content: %v", err)
	}

	// Metadata fields
	_ = w.WriteField("path", path)
	if kind != "" {
		_ = w.WriteField("kind", kind)
	}
	if taskID != "" {
		_ = w.WriteField("task_id", taskID)
	}
	if mimeType != "" {
		_ = w.WriteField("mime_type", mimeType)
	}

	_ = w.Close()
	return &buf, w.FormDataContentType()
}

// ── Upload ────────────────────────────────────────────────────────────────────

func TestHandleUploadArtifact_Success(t *testing.T) {
	h, ah := newTestArtifactHandler(t)
	mux := newArtifactTestMux(h, ah)

	sbxID := mustCreateReadySandbox(t, mux, "ws-1")

	body, ct := buildMultipartUpload(t, "result.txt", "hello world", "output/result.txt", "file", "task-1", "text/plain")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sbxID+"/artifacts", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var art Artifact
	decodeJSON(t, w.Body, &art)
	if art.ID == "" {
		t.Error("expected ID")
	}
	if art.SHA256 == "" {
		t.Error("expected SHA256")
	}
	if art.Size != int64(len("hello world")) {
		t.Errorf("size: want %d got %d", len("hello world"), art.Size)
	}
	// Content must NOT be in response.
	if art.Content != nil {
		t.Error("content should not be in JSON response")
	}
}

func TestHandleUploadArtifact_MissingPath(t *testing.T) {
	h, ah := newTestArtifactHandler(t)
	mux := newArtifactTestMux(h, ah)
	sbxID := mustCreateReadySandbox(t, mux, "ws-1")

	body, ct := buildMultipartUpload(t, "result.txt", "data", "", "file", "", "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sbxID+"/artifacts", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleUploadArtifact_UnknownSandbox(t *testing.T) {
	h, ah := newTestArtifactHandler(t)
	mux := newArtifactTestMux(h, ah)

	body, ct := buildMultipartUpload(t, "f.txt", "data", "f.txt", "file", "", "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/nonexistent/artifacts", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandleUploadArtifact_SizeLimitRejection(t *testing.T) {
	h, ah := newTestArtifactHandler(t)
	mux := newArtifactTestMux(h, ah)
	sbxID := mustCreateReadySandbox(t, mux, "ws-1")

	// Build a multipart body with content just over the limit.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	filePart, _ := mw.CreateFormFile("file", "big.bin")
	_, _ = filePart.Write(make([]byte, MaxArtifactSizeBytes+1))
	_ = mw.WriteField("path", "big.bin")
	_ = mw.WriteField("kind", "file")
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sbxID+"/artifacts", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d: %s", w.Code, w.Body.String())
	}
}

// ── List ──────────────────────────────────────────────────────────────────────

func TestHandleListArtifacts(t *testing.T) {
	h, ah := newTestArtifactHandler(t)
	mux := newArtifactTestMux(h, ah)
	sbxID := mustCreateReadySandbox(t, mux, "ws-1")

	for i := 0; i < 3; i++ {
		body, ct := buildMultipartUpload(t, "f.txt", fmt.Sprintf("content-%d", i), "f.txt", "file", "", "")
		req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sbxID+"/artifacts", body)
		req.Header.Set("Content-Type", ct)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("upload %d: %d", i, w.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+sbxID+"/artifacts", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Artifacts []Artifact `json:"artifacts"`
		Total     int        `json:"total"`
	}
	decodeJSON(t, w.Body, &resp)
	if resp.Total != 3 {
		t.Errorf("want 3, got %d", resp.Total)
	}
}

func TestHandleListArtifacts_TaskFilter(t *testing.T) {
	h, ah := newTestArtifactHandler(t)
	mux := newArtifactTestMux(h, ah)
	sbxID := mustCreateReadySandbox(t, mux, "ws-1")

	for _, taskID := range []string{"task-A", "task-B"} {
		body, ct := buildMultipartUpload(t, "f.txt", "data", "f.txt", "file", taskID, "")
		req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sbxID+"/artifacts", body)
		req.Header.Set("Content-Type", ct)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("upload: %d", w.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+sbxID+"/artifacts?task_id=task-A", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp struct {
		Artifacts []Artifact `json:"artifacts"`
		Total     int        `json:"total"`
	}
	decodeJSON(t, w.Body, &resp)
	if resp.Total != 1 {
		t.Errorf("want 1, got %d", resp.Total)
	}
}

// ── Get metadata ──────────────────────────────────────────────────────────────

func TestHandleGetArtifact(t *testing.T) {
	h, ah := newTestArtifactHandler(t)
	mux := newArtifactTestMux(h, ah)
	sbxID := mustCreateReadySandbox(t, mux, "ws-1")

	body, ct := buildMultipartUpload(t, "f.txt", "hello", "f.txt", "file", "", "text/plain")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sbxID+"/artifacts", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: %d: %s", w.Code, w.Body.String())
	}

	var created Artifact
	if err := json.NewDecoder(strings.NewReader(w.Body.String())).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+sbxID+"/artifacts/"+created.ID, nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w2.Code, w2.Body.String())
	}

	var got Artifact
	decodeJSON(t, w2.Body, &got)
	if got.ID != created.ID {
		t.Errorf("ID mismatch: %s vs %s", got.ID, created.ID)
	}
	if got.Content != nil {
		t.Error("content should not be in metadata response")
	}
}

// ── Download content ──────────────────────────────────────────────────────────

func TestHandleDownloadArtifact(t *testing.T) {
	h, ah := newTestArtifactHandler(t)
	mux := newArtifactTestMux(h, ah)
	sbxID := mustCreateReadySandbox(t, mux, "ws-1")

	const fileContent = "artifact content to download"
	body, ct := buildMultipartUpload(t, "result.txt", fileContent, "output/result.txt", "file", "", "text/plain")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sbxID+"/artifacts", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: %d", w.Code)
	}

	var created Artifact
	decodeJSON(t, w.Body, &created)

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+sbxID+"/artifacts/"+created.ID+"/content", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w2.Code, w2.Body.String())
	}

	if got := w2.Body.String(); got != fileContent {
		t.Errorf("content mismatch: want %q got %q", fileContent, got)
	}

	ct2 := w2.Header().Get("Content-Type")
	if !strings.HasPrefix(ct2, "text/plain") {
		t.Errorf("expected text/plain Content-Type, got %q", ct2)
	}

	cd := w2.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") {
		t.Errorf("expected attachment Content-Disposition, got %q", cd)
	}
}

func TestHandleDownloadArtifact_NotFound(t *testing.T) {
	h, ah := newTestArtifactHandler(t)
	mux := newArtifactTestMux(h, ah)
	sbxID := mustCreateReadySandbox(t, mux, "ws-1")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+sbxID+"/artifacts/does-not-exist/content", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// ── Diff kind ─────────────────────────────────────────────────────────────────

func TestHandleUploadArtifact_DiffKind(t *testing.T) {
	h, ah := newTestArtifactHandler(t)
	mux := newArtifactTestMux(h, ah)
	sbxID := mustCreateReadySandbox(t, mux, "ws-1")

	diffContent := `--- a/foo.go
+++ b/foo.go
@@ -1,2 +1,3 @@
 context
-old
+new
+extra
`
	body, ct := buildMultipartUpload(t, "changes.diff", diffContent, "changes.diff", "diff", "task-X", "text/x-diff")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sbxID+"/artifacts", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var art Artifact
	decodeJSON(t, w.Body, &art)
	if art.Kind != ArtifactKindDiff {
		t.Errorf("kind: want diff, got %q", art.Kind)
	}
	if art.DiffSummary == "" {
		t.Error("expected diff_summary to be computed")
	}
}
