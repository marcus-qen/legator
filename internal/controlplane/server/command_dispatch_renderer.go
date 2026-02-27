package server

import (
	"encoding/json"
	"net/http"

	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/protocol"
)

func renderDispatchCommandHTTP(w http.ResponseWriter, projection *corecommanddispatch.CommandInvokeProjection) {
	corecommanddispatch.DispatchCommandInvokeProjection(projection, corecommanddispatch.CommandInvokeRenderDispatchWriter{
		WriteHTTPError: func(httpErr *corecommanddispatch.HTTPErrorContract) {
			if !httpErr.SuppressWrite {
				writeJSONError(w, httpErr.Status, httpErr.Code, httpErr.Message)
			}
		},
		WriteHTTPDispatched: func(requestID string) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status":     "dispatched",
				"request_id": requestID,
			})
		},
		WriteHTTPResult: func(result *protocol.CommandResultPayload) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(result)
		},
	})
}
