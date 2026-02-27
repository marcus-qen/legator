package approvalpolicy

import (
	"errors"
	"net/http"
)

// DecideApprovalProjection is the transport-agnostic decide response projection.
// HTTP and MCP renderers can consume this shared envelope.
type DecideApprovalProjection struct {
	Success *DecideApprovalSuccess
	Err     *HTTPErrorContract
}

// HTTPError returns a projected HTTP error, when present.
func (p *DecideApprovalProjection) HTTPError() (*HTTPErrorContract, bool) {
	if p == nil || p.Err == nil {
		return nil, false
	}
	return p.Err, true
}

// MCPError returns a projected MCP error, when present.
func (p *DecideApprovalProjection) MCPError() error {
	if httpErr, ok := p.HTTPError(); ok {
		return errors.New(httpErr.Message)
	}
	return nil
}

// ProjectDecideApprovalTransport maps the decide transport contract to a reusable
// success/error projection that concrete renderers can write to HTTP or MCP.
func ProjectDecideApprovalTransport(contract *DecideApprovalTransportContract) *DecideApprovalProjection {
	if contract == nil {
		return &DecideApprovalProjection{
			Err: &HTTPErrorContract{
				Status:  http.StatusInternalServerError,
				Code:    "internal_error",
				Message: "approval decide adapter returned empty contract",
			},
		}
	}

	if httpErr, ok := contract.HTTPError(); ok {
		return &DecideApprovalProjection{Err: httpErr}
	}

	success := contract.Success
	if success == nil {
		success = &DecideApprovalSuccess{}
	}

	return &DecideApprovalProjection{Success: success}
}
