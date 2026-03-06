package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPISpecEndpoint(t *testing.T) {
	// Spec is now embedded at compile time; no chdir needed.
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

func TestOpenAPISpecAlwaysAvailable(t *testing.T) {
	// The spec is now embedded at compile time. It should be served from any CWD,
	// including a temp directory where no docs/ folder exists on disk.
	t.Chdir(t.TempDir())

	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil)
	rr := httptest.NewRecorder()

	srv.handleOpenAPISpec(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (spec is embedded), got %d: %s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "yaml") {
		t.Fatalf("expected yaml content-type, got %q", ct)
	}
}
