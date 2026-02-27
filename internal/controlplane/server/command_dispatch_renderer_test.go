package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/protocol"
)

func TestRenderDispatchCommandHTTP_ParityWithLegacy(t *testing.T) {
	tests := []struct {
		name      string
		requestID string
		wantWait  bool
		envelope  *corecommanddispatch.CommandResultEnvelope
	}{
		{
			name:      "nil envelope",
			requestID: "req-nil",
			wantWait:  false,
			envelope:  nil,
		},
		{
			name:      "dispatch error",
			requestID: "req-dispatch-error",
			wantWait:  false,
			envelope:  &corecommanddispatch.CommandResultEnvelope{Err: errors.New("not connected")},
		},
		{
			name:      "timeout error",
			requestID: "req-timeout",
			wantWait:  true,
			envelope:  &corecommanddispatch.CommandResultEnvelope{Err: corecommanddispatch.ErrTimeout},
		},
		{
			name:      "context canceled suppress write",
			requestID: "req-canceled",
			wantWait:  true,
			envelope:  &corecommanddispatch.CommandResultEnvelope{Err: context.Canceled},
		},
		{
			name:      "dispatch-only success",
			requestID: "req-dispatched",
			wantWait:  false,
			envelope:  &corecommanddispatch.CommandResultEnvelope{Dispatched: true},
		},
		{
			name:      "wait success",
			requestID: "req-result",
			wantWait:  true,
			envelope: &corecommanddispatch.CommandResultEnvelope{Result: &protocol.CommandResultPayload{
				RequestID: "req-result",
				ExitCode:  0,
				Stdout:    "ok",
			}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legacyRR := httptest.NewRecorder()
			legacyRenderDispatchCommandHTTP(legacyRR, tc.requestID, tc.envelope, tc.wantWait)

			adapterRR := httptest.NewRecorder()
			renderDispatchCommandHTTP(adapterRR, tc.requestID, tc.envelope, tc.wantWait)

			if legacyRR.Code != adapterRR.Code {
				t.Fatalf("status mismatch: legacy=%d adapter=%d", legacyRR.Code, adapterRR.Code)
			}
			if legacyRR.Body.String() != adapterRR.Body.String() {
				t.Fatalf("body mismatch:\nlegacy=%s\nadapter=%s", legacyRR.Body.String(), adapterRR.Body.String())
			}
			if !reflect.DeepEqual(legacyRR.Header(), adapterRR.Header()) {
				t.Fatalf("header mismatch: legacy=%v adapter=%v", legacyRR.Header(), adapterRR.Header())
			}
		})
	}
}

func legacyRenderDispatchCommandHTTP(w http.ResponseWriter, requestID string, envelope *corecommanddispatch.CommandResultEnvelope, wantWait bool) {
	if envelope == nil {
		writeJSONError(w, http.StatusBadGateway, "bad_gateway", "command dispatch failed")
		return
	}

	if httpErr, ok := envelope.HTTPError(); ok {
		if !httpErr.SuppressWrite {
			writeJSONError(w, httpErr.Status, httpErr.Code, httpErr.Message)
		}
		return
	}

	if !wantWait {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":     "dispatched",
			"request_id": requestID,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(envelope.Result)
}
