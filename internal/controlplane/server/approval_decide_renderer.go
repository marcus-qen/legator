package server

import (
	"encoding/json"
	"net/http"

	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
)

type approvalDecideResponseRenderer interface {
	RenderHTTP(w http.ResponseWriter, projection *coreapprovalpolicy.DecideApprovalProjection)
}

type approvalDecideHTTPRenderer struct{}

func (approvalDecideHTTPRenderer) RenderHTTP(w http.ResponseWriter, projection *coreapprovalpolicy.DecideApprovalProjection) {
	if projection == nil {
		projection = coreapprovalpolicy.ProjectDecideApprovalTransport(nil)
	}

	if httpErr, ok := projection.HTTPError(); ok {
		writeJSONError(w, httpErr.Status, httpErr.Code, httpErr.Message)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(projection.Success)
}

func renderDecideApprovalHTTP(w http.ResponseWriter, projection *coreapprovalpolicy.DecideApprovalProjection) {
	approvalDecideHTTPRenderer{}.RenderHTTP(w, projection)
}
