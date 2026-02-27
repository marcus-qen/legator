package server

import (
	"encoding/json"
	"net/http"

	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
)

func renderDispatchCommandHTTP(w http.ResponseWriter, projection *corecommanddispatch.CommandInvokeProjection) {
	response := corecommanddispatch.EncodeCommandInvokeHTTPJSONResponse(projection)
	if response.SuppressWrite || !response.HasBody {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if response.Status != 0 && response.Status != http.StatusOK {
		w.WriteHeader(response.Status)
	}
	_ = json.NewEncoder(w).Encode(response.Body)
}
