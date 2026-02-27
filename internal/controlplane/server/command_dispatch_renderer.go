package server

import (
	"encoding/json"
	"net/http"

	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/protocol"
)

func renderDispatchCommandHTTP(w http.ResponseWriter, requestID string, envelope *corecommanddispatch.CommandResultEnvelope, wantWait bool) {
	if envelope == nil {
		writeJSONError(w, http.StatusBadGateway, "bad_gateway", "command dispatch failed")
		return
	}

	handled := corecommanddispatch.DispatchCommandErrorsForSurface(envelope, corecommanddispatch.ProjectionDispatchSurfaceHTTP, corecommanddispatch.CommandProjectionDispatchWriter{
		WriteHTTPError: func(httpErr *corecommanddispatch.HTTPErrorContract) {
			if !httpErr.SuppressWrite {
				writeJSONError(w, httpErr.Status, httpErr.Code, httpErr.Message)
			}
		},
	})
	if handled {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if !wantWait {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":     "dispatched",
			"request_id": requestID,
		})
		return
	}

	corecommanddispatch.DispatchCommandReadForSurface(envelope.Result, corecommanddispatch.ProjectionDispatchSurfaceHTTP, corecommanddispatch.CommandProjectionDispatchWriter{
		WriteHTTPResult: func(result *protocol.CommandResultPayload) {
			_ = json.NewEncoder(w).Encode(result)
		},
	})
}
