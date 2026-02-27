package approvalpolicy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DecideApprovalInvokeInput carries the transport-normalized approval id + body
// used to invoke the shared decide orchestration seam.
type DecideApprovalInvokeInput struct {
	ApprovalID string
	Body       io.Reader
}

// AssembleDecideApprovalInvokeHTTP normalizes the HTTP shell input into the
// shared invoke adapter contract.
func AssembleDecideApprovalInvokeHTTP(approvalID string, body io.Reader) *DecideApprovalInvokeInput {
	return &DecideApprovalInvokeInput{ApprovalID: approvalID, Body: body}
}

// AssembleDecideApprovalInvokeMCP normalizes the MCP tool input into the
// shared invoke adapter contract.
func AssembleDecideApprovalInvokeMCP(approvalID, decision, decidedBy string) (*DecideApprovalInvokeInput, error) {
	normalizedApprovalID := strings.TrimSpace(approvalID)
	if normalizedApprovalID == "" {
		return nil, fmt.Errorf("approval_id is required")
	}

	body, err := json.Marshal(struct {
		Decision  string `json:"decision"`
		DecidedBy string `json:"decided_by"`
	}{
		Decision:  decision,
		DecidedBy: decidedBy,
	})
	if err != nil {
		return nil, fmt.Errorf("encode decide approval input: %w", err)
	}

	return &DecideApprovalInvokeInput{ApprovalID: normalizedApprovalID, Body: bytes.NewReader(body)}, nil
}

// InvokeDecideApproval runs the shared decide orchestration using normalized
// shell input while wiring id + request invocation through one closure seam.
func InvokeDecideApproval(input *DecideApprovalInvokeInput, invoke func(id string, request *DecideApprovalRequest) (*ApprovalDecisionResult, error), target DecideApprovalRenderTarget) *DecideApprovalProjection {
	if input == nil {
		return &DecideApprovalProjection{
			Err: &HTTPErrorContract{
				Status:  http.StatusInternalServerError,
				Code:    "internal_error",
				Message: "approval decide invoke adapter returned empty input",
			},
		}
	}
	if input.Body == nil {
		return &DecideApprovalProjection{
			Err: &HTTPErrorContract{
				Status:  http.StatusInternalServerError,
				Code:    "internal_error",
				Message: "approval decide invoke adapter returned empty body",
			},
		}
	}
	if invoke == nil {
		return &DecideApprovalProjection{
			Err: &HTTPErrorContract{
				Status:  http.StatusInternalServerError,
				Code:    "internal_error",
				Message: "approval decide invoke adapter is missing shell invoke handler",
			},
		}
	}

	return OrchestrateDecideApproval(input.Body, func(request *DecideApprovalRequest) (*ApprovalDecisionResult, error) {
		return invoke(input.ApprovalID, request)
	}, target)
}
