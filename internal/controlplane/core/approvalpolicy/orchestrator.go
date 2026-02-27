package approvalpolicy

import (
	"fmt"
	"io"
	"net/http"
)

// DecideApprovalRenderTarget selects which transport renderer will consume
// the shared decide projection.
type DecideApprovalRenderTarget string

const (
	DecideApprovalRenderTargetHTTP DecideApprovalRenderTarget = "http"
	DecideApprovalRenderTargetMCP  DecideApprovalRenderTarget = "mcp"
)

// OrchestrateDecideApproval executes the shared decide flow seam:
// decode -> decide -> project -> render selection.
func OrchestrateDecideApproval(body io.Reader, decide func(*DecideApprovalRequest) (*ApprovalDecisionResult, error), target DecideApprovalRenderTarget) *DecideApprovalProjection {
	contract := AdaptDecideApprovalTransport(body, decide)
	return SelectDecideApprovalProjection(contract, target)
}

// SelectDecideApprovalProjection chooses the renderer-facing projection from a
// shared decide transport contract.
func SelectDecideApprovalProjection(contract *DecideApprovalTransportContract, target DecideApprovalRenderTarget) *DecideApprovalProjection {
	switch target {
	case "", DecideApprovalRenderTargetHTTP, DecideApprovalRenderTargetMCP:
		return ProjectDecideApprovalTransport(contract)
	default:
		return &DecideApprovalProjection{
			Err: &HTTPErrorContract{
				Status:  http.StatusInternalServerError,
				Code:    "internal_error",
				Message: fmt.Sprintf("unsupported approval decide render target %q", string(target)),
			},
		}
	}
}
