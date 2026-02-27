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

// DecodeDecideApprovalRequest validates and normalizes approval decision input.
func DecodeDecideApprovalRequest(body io.Reader) (*DecideApprovalRequest, *HTTPErrorContract) {
	var payload struct {
		Decision  string `json:"decision"`
		DecidedBy string `json:"decided_by"`
	}
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return nil, &HTTPErrorContract{
			Status:  http.StatusBadRequest,
			Code:    "invalid_request",
			Message: "invalid request body",
		}
	}
	if payload.Decision == "" || payload.DecidedBy == "" {
		return nil, &HTTPErrorContract{
			Status:  http.StatusBadRequest,
			Code:    "invalid_request",
			Message: "decision and decided_by are required",
		}
	}

	return &DecideApprovalRequest{
		Decision:  approval.Decision(payload.Decision),
		DecidedBy: payload.DecidedBy,
	}, nil
}

// EncodeDecideApprovalSuccess maps core decision outcomes to the API success contract.
func EncodeDecideApprovalSuccess(result *ApprovalDecisionResult) *DecideApprovalSuccess {
	if result == nil || result.Request == nil {
		return &DecideApprovalSuccess{}
	}
	return &DecideApprovalSuccess{
		Status:  string(result.Request.Decision),
		Request: result.Request,
	}
}

// DecideApprovalHTTPError maps DecideAndDispatch errors to API-facing semantics.
func DecideApprovalHTTPError(err error) (*HTTPErrorContract, bool) {
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
