package server

import (
	"encoding/json"
	"net/http"

	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
)

type approvalDecideResponseRenderer interface {
	RenderHTTP(w http.ResponseWriter, contract *coreapprovalpolicy.DecideApprovalTransportContract)
}

type approvalDecideHTTPRenderer struct{}

func (approvalDecideHTTPRenderer) RenderHTTP(w http.ResponseWriter, contract *coreapprovalpolicy.DecideApprovalTransportContract) {
	projection := coreapprovalpolicy.ProjectDecideApprovalTransport(contract)
	if httpErr, ok := projection.HTTPError(); ok {
		writeJSONError(w, httpErr.Status, httpErr.Code, httpErr.Message)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(projection.Success)
}

func renderDecideApprovalHTTP(w http.ResponseWriter, contract *coreapprovalpolicy.DecideApprovalTransportContract) {
	approvalDecideHTTPRenderer{}.RenderHTTP(w, contract)
}
