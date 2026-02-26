package status

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthzConnected(t *testing.T) {
	s := NewServer("prb-1", "http://localhost:8080", func() bool { return true })
	r := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHealthzDisconnected(t *testing.T) {
	s := NewServer("prb-1", "http://localhost:8080", func() bool { return false })
	r := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestStatusEndpoint(t *testing.T) {
	s := NewServer("prb-test", "http://cp.example.com", func() bool { return true })
	r := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var info Info
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("parse response: %v", err)
	}

	if info.ProbeID != "prb-test" {
		t.Fatalf("expected prb-test, got %s", info.ProbeID)
	}
	if !info.Connected {
		t.Fatal("expected connected")
	}
	if info.GoVersion == "" {
		t.Fatal("missing go version")
	}
	if info.Uptime == "" {
		t.Fatal("missing uptime")
	}
}
