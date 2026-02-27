package server

import (
	"encoding/json"
	"net/http"

	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/protocol"
)

func renderDispatchCommandHTTP(w http.ResponseWriter, projection *corecommanddispatch.CommandInvokeProjection) {
	if projection == nil || projection.Envelope == nil {
		writeJSONError(w, http.StatusBadGateway, "bad_gateway", "command dispatch failed")
		return
	}

	handled := corecommanddispatch.DispatchCommandErrorsForSurface(projection.Envelope, projection.Surface, corecommanddispatch.CommandProjectionDispatchWriter{
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
	if !projection.WaitForResult {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":     "dispatched",
			"request_id": projection.RequestID,
		})
		return
	}

	corecommanddispatch.DispatchCommandReadForSurface(projection.Envelope.Result, projection.Surface, corecommanddispatch.CommandProjectionDispatchWriter{
		WriteHTTPResult: func(result *protocol.CommandResultPayload) {
			_ = json.NewEncoder(w).Encode(result)
		},
	})
}
