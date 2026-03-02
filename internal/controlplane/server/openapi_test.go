package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// repoRoot returns the absolute path to the repository root by walking up from
// this test file's location. This lets the test find docs/openapi.yaml
// regardless of where  is invoked.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	// file is .../internal/controlplane/server/openapi_test.go
	// walk up three directories: server -> controlplane -> internal -> repo root
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

func TestOpenAPISpecEndpoint(t *testing.T) {
	t.Chdir(repoRoot())

	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil)
	rr := httptest.NewRecorder()

	srv.handleOpenAPISpec(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
	}

	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "yaml") {
		t.Fatalf("expected yaml content-type, got %q", ct)
	}

	var spec map[string]interface{}
	if err := yaml.Unmarshal(rr.Body.Bytes(), &spec); err != nil {
		t.Fatalf("response is not valid YAML: %v", err)
	}

	openapiVersion, ok := spec["openapi"].(string)
	if !ok {
		t.Fatalf("openapi field missing or not a string: %v", spec["openapi"])
	}
	if openapiVersion != "3.1.0" {
		t.Fatalf("expected openapi 3.1.0, got %q", openapiVersion)
	}

	paths, ok := spec["paths"].(map[string]interface{})
	if !ok {
		t.Fatalf("paths field missing or wrong type")
	}
	if len(paths) < 50 {
		t.Fatalf("expected >= 50 paths, got %d — spec may be incomplete", len(paths))
	}

	t.Logf("OpenAPI spec: version=%s paths=%d", openapiVersion, len(paths))
}

func TestOpenAPISpecNotFoundWhenMissing(t *testing.T) {
	// Run from a temp directory where docs/openapi.yaml does not exist.
	t.Chdir(t.TempDir())

	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil)
	rr := httptest.NewRecorder()

	srv.handleOpenAPISpec(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when spec file absent, got %d", rr.Code)
	}
}
