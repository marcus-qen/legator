package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBodySizeLimit_OversizedContentLengthRejected(t *testing.T) {
	srv := newTestServer(t) // auth disabled — body limit is auth-agnostic

	// 2 MiB body — well over the 1 MiB limit
	body := bytes.Repeat([]byte("x"), 2*1024*1024)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/cleanup", bytes.NewReader(body))
	req.ContentLength = int64(len(body))

	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 Request Entity Too Large for 2MiB body, got %d (body: %s)",
			rr.Code, rr.Body.String())
	}

	if !strings.Contains(rr.Body.String(), "request_too_large") {
		t.Errorf("expected error code 'request_too_large' in response body: %s", rr.Body.String())
	}
}

func TestBodySizeLimit_ExactlyAtLimitAccepted(t *testing.T) {
	srv := newTestServer(t)

	// Exactly at 1 MiB — should NOT be rejected by the Content-Length check
	body := bytes.Repeat([]byte("x"), int(maxBodyBytes))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/cleanup", bytes.NewReader(body))
	req.ContentLength = int64(len(body))

	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	if rr.Code == http.StatusRequestEntityTooLarge {
		t.Errorf("expected body at exactly 1MiB to NOT be rejected with 413, got %d", rr.Code)
	}
}

func TestBodySizeLimit_GetRequestNotAffected(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	// GET /healthz is always 200 — body limit must not interfere
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for GET /healthz, got %d", rr.Code)
	}
}

func TestBodySizeLimit_PutRouteEnforced(t *testing.T) {
	srv := newTestServer(t)

	body := bytes.Repeat([]byte("z"), 2*1024*1024)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/probes/probe-1/tags", bytes.NewReader(body))
	req.ContentLength = int64(len(body))

	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 for 2MiB PUT body, got %d (body: %s)", rr.Code, rr.Body.String())
	}
}
