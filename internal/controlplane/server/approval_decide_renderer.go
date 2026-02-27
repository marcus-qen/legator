package server

import (
	"encoding/json"
	"io"
	"net/http"

	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
)

type approvalDecideResponseRenderer interface {
	RenderHTTP(w http.ResponseWriter, projection *coreapprovalpolicy.DecideApprovalProjection)
}

type approvalDecideHTTPRenderer struct{}

func (approvalDecideHTTPRenderer) RenderHTTP(w http.ResponseWriter, projection *coreapprovalpolicy.DecideApprovalProjection) {
	coreapprovalpolicy.DispatchDecideApprovalResponseForSurface(projection, coreapprovalpolicy.DecideApprovalRenderSurfaceHTTP, coreapprovalpolicy.DecideApprovalResponseDispatchWriter{
		WriteHTTPError: func(httpErr *coreapprovalpolicy.HTTPErrorContract) {
			writeJSONError(w, httpErr.Status, httpErr.Code, httpErr.Message)
		},
		WriteSuccess: func(success *coreapprovalpolicy.DecideApprovalSuccess) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(success)
		},
	})
}

func orchestrateDecideApprovalHTTP(body io.Reader, decide func(*coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error)) *coreapprovalpolicy.DecideApprovalProjection {
	return coreapprovalpolicy.OrchestrateDecideApprovalForSurface(body, decide, coreapprovalpolicy.DecideApprovalRenderSurfaceHTTP)
}

func renderDecideApprovalHTTP(w http.ResponseWriter, projection *coreapprovalpolicy.DecideApprovalProjection) {
	approvalDecideHTTPRenderer{}.RenderHTTP(w, projection)
}
