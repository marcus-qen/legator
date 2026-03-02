package jobs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAsyncJobHTTPContracts(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	var canceledRequestID string
	h := NewHandler(store, nil,
		WithAsyncManager(NewAsyncManager(store)),
		WithAsyncCanceler(func(requestID string) { canceledRequestID = requestID }),
	)

	createBody := `{"probe_id":"probe-1","request_id":"req-api-1","command":"hostname"}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(createBody))
	createRR := httptest.NewRecorder()
	h.HandleCreateJob(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("expected 201 create, got %d body=%s", createRR.Code, createRR.Body.String())
	}

	var created AsyncJob
	if err := json.Unmarshal(createRR.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.State != AsyncJobStateQueued {
		t.Fatalf("expected queued state, got %s", created.State)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?kind=async", nil)
	listRR := httptest.NewRecorder()
	h.HandleListJobs(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected 200 list, got %d body=%s", listRR.Code, listRR.Body.String())
	}
	var listed []AsyncJob
	if err := json.Unmarshal(listRR.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("expected listed async job %s, got %#v", created.ID, listed)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+created.ID+"?kind=async", nil)
	getReq.SetPathValue("id", created.ID)
	getRR := httptest.NewRecorder()
	h.HandleGetJob(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("expected 200 get, got %d body=%s", getRR.Code, getRR.Body.String())
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+created.ID+"/cancel?kind=async", nil)
	cancelReq.SetPathValue("id", created.ID)
	cancelRR := httptest.NewRecorder()
	h.HandleCancelJob(cancelRR, cancelReq)
	if cancelRR.Code != http.StatusOK {
		t.Fatalf("expected 200 cancel, got %d body=%s", cancelRR.Code, cancelRR.Body.String())
	}
	if canceledRequestID != "req-api-1" {
		t.Fatalf("expected canceller to receive req-api-1, got %q", canceledRequestID)
	}
}

func TestAsyncJobCreateRejectsWhenQueueSaturated(t *testing.T) {
	store := newTestStore(t)
	h := NewHandler(store, nil, WithAsyncManager(NewAsyncManager(store, WithAsyncMaxQueueDepth(1))))

	firstReq := httptest.NewRequest(http.MethodPost, "/api/v1/jobs?kind=async", strings.NewReader(`{"probe_id":"probe-1","request_id":"req-1","command":"hostname"}`))
	firstRR := httptest.NewRecorder()
	h.HandleCreateJob(firstRR, firstReq)
	if firstRR.Code != http.StatusCreated {
		t.Fatalf("expected first async create to succeed, got %d body=%s", firstRR.Code, firstRR.Body.String())
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/api/v1/jobs?kind=async", strings.NewReader(`{"probe_id":"probe-1","request_id":"req-2","command":"hostname"}`))
	secondRR := httptest.NewRecorder()
	h.HandleCreateJob(secondRR, secondReq)
	if secondRR.Code != http.StatusTooManyRequests {
		t.Fatalf("expected queue saturation 429, got %d body=%s", secondRR.Code, secondRR.Body.String())
	}
	if !strings.Contains(secondRR.Body.String(), "queue saturated") {
		t.Fatalf("expected queue saturation message, got %s", secondRR.Body.String())
	}
}
