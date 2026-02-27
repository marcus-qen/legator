package approvalpolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
)

// HTTPErrorContract maps core approval-decision errors to API response details.
type HTTPErrorContract struct {
	Status  int
	Code    string
	Message string
}

// DecideApprovalRequest is the API-facing decode contract for approval decisions.
type DecideApprovalRequest struct {
	Decision  approval.Decision
	DecidedBy string
}

// DecideApprovalSuccess is the API-facing success envelope for approval decisions.
type DecideApprovalSuccess struct {
	Status  string            `json:"status"`
	Request *approval.Request `json:"request"`
}

// DecideApprovalTransportContract is the transport adapter contract for decide flows.
// It carries either a decoded request, a success payload, or an error contract.
type DecideApprovalTransportContract struct {
	Request *DecideApprovalRequest
	Success *DecideApprovalSuccess
	Err     *HTTPErrorContract
}

// HTTPError returns a transport-mapped HTTP error, when present.
func (c *DecideApprovalTransportContract) HTTPError() (*HTTPErrorContract, bool) {
	if c == nil || c.Err == nil {
		return nil, false
	}
	return c.Err, true
}

// DecodeDecideApprovalTransport validates and normalizes approval decision input.
func DecodeDecideApprovalTransport(body io.Reader) *DecideApprovalTransportContract {
	var payload struct {
		Decision  string `json:"decision"`
		DecidedBy string `json:"decided_by"`
	}
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return &DecideApprovalTransportContract{
			Err: &HTTPErrorContract{
				Status:  http.StatusBadRequest,
				Code:    "invalid_request",
				Message: "invalid request body",
			},
		}
	}
	if payload.Decision == "" || payload.DecidedBy == "" {
		return &DecideApprovalTransportContract{
			Err: &HTTPErrorContract{
				Status:  http.StatusBadRequest,
				Code:    "invalid_request",
				Message: "decision and decided_by are required",
			},
		}
	}

	return &DecideApprovalTransportContract{
		Request: &DecideApprovalRequest{
			Decision:  approval.Decision(payload.Decision),
			DecidedBy: payload.DecidedBy,
		},
	}
}

// EncodeDecideApprovalTransport maps core decide outcomes to the transport contract.
func EncodeDecideApprovalTransport(result *ApprovalDecisionResult, err error) *DecideApprovalTransportContract {
	if httpErr, ok := decideApprovalHTTPError(err); ok {
		return &DecideApprovalTransportContract{Err: httpErr}
	}

	success := &DecideApprovalSuccess{}
	if result != nil && result.Request != nil {
		success.Status = string(result.Request.Decision)
		success.Request = result.Request
	}
	return &DecideApprovalTransportContract{Success: success}
}

// AdaptDecideApprovalTransport executes decode + core decision + encode through one transport contract.
func AdaptDecideApprovalTransport(body io.Reader, decide func(*DecideApprovalRequest) (*ApprovalDecisionResult, error)) *DecideApprovalTransportContract {
	decoded := DecodeDecideApprovalTransport(body)
	if _, ok := decoded.HTTPError(); ok {
		return decoded
	}
	if decoded.Request == nil {
		return &DecideApprovalTransportContract{
			Err: &HTTPErrorContract{
				Status:  http.StatusInternalServerError,
				Code:    "internal_error",
				Message: "approval decide adapter returned empty request",
			},
		}
	}
	if decide == nil {
		return &DecideApprovalTransportContract{
			Err: &HTTPErrorContract{
				Status:  http.StatusInternalServerError,
				Code:    "internal_error",
				Message: "approval decide adapter is missing core handler",
			},
		}
	}

	result, err := decide(decoded.Request)
	return EncodeDecideApprovalTransport(result, err)
}

func decideApprovalHTTPError(err error) (*HTTPErrorContract, bool) {
	if err == nil {
		return nil, false
	}

	var dispatchErr *ApprovedDispatchError
	if errors.As(err, &dispatchErr) {
		return &HTTPErrorContract{
			Status:  http.StatusBadGateway,
			Code:    "bad_gateway",
			Message: fmt.Sprintf("approved but dispatch failed: %s", dispatchErr.Error()),
		}, true
	}

	var hookErr *DecisionHookError
	if errors.As(err, &hookErr) {
		return &HTTPErrorContract{
			Status:  http.StatusInternalServerError,
			Code:    "internal_error",
			Message: hookErr.Error(),
		}, true
	}

	return &HTTPErrorContract{
		Status:  http.StatusBadRequest,
		Code:    "invalid_request",
		Message: err.Error(),
	}, true
}

// DecodeDecideApprovalRequest preserves the legacy decode split contract.
func DecodeDecideApprovalRequest(body io.Reader) (*DecideApprovalRequest, *HTTPErrorContract) {
	contract := DecodeDecideApprovalTransport(body)
	if httpErr, ok := contract.HTTPError(); ok {
		return nil, httpErr
	}
	return contract.Request, nil
}

// EncodeDecideApprovalSuccess preserves the legacy success-only contract.
func EncodeDecideApprovalSuccess(result *ApprovalDecisionResult) *DecideApprovalSuccess {
	contract := EncodeDecideApprovalTransport(result, nil)
	if contract.Success == nil {
		return &DecideApprovalSuccess{}
	}
	return contract.Success
}

// DecideApprovalHTTPError preserves the legacy error-only contract.
func DecideApprovalHTTPError(err error) (*HTTPErrorContract, bool) {
	return decideApprovalHTTPError(err)
}
