package approvalpolicy

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

// EncodeDecideApprovalResponseEnvelope normalizes approval-decide projections
// for HTTP/MCP writer-kernel transport rendering.
func EncodeDecideApprovalResponseEnvelope(projection *DecideApprovalProjection, surface DecideApprovalRenderSurface) *transportwriter.ResponseEnvelope {
	if projection == nil {
		projection = ProjectDecideApprovalTransport(nil)
	}

	switch surface {
	case DecideApprovalRenderSurfaceHTTP:
		if httpErr, ok := projection.HTTPError(); ok {
			return &transportwriter.ResponseEnvelope{HTTPError: &transportwriter.HTTPError{
				Status:  httpErr.Status,
				Code:    httpErr.Code,
				Message: httpErr.Message,
			}}
		}
		return &transportwriter.ResponseEnvelope{HTTPSuccess: normalizeDecideApprovalSuccess(projection.Success)}
	case DecideApprovalRenderSurfaceMCP:
		if err := projection.MCPError(); err != nil {
			return &transportwriter.ResponseEnvelope{MCPError: err}
		}
		return &transportwriter.ResponseEnvelope{MCPSuccess: normalizeDecideApprovalSuccess(projection.Success)}
	default:
		message := fmt.Sprintf("unsupported approval decide dispatch surface %q", string(surface))
		return &transportwriter.ResponseEnvelope{
			HTTPError: &transportwriter.HTTPError{
				Status:  http.StatusInternalServerError,
				Code:    "internal_error",
				Message: message,
			},
			MCPError: errors.New(message),
		}
	}
}

func normalizeDecideApprovalSuccess(success *DecideApprovalSuccess) *DecideApprovalSuccess {
	if success == nil {
		return &DecideApprovalSuccess{}
	}
	return success
}
