package approvalpolicy

import (
	"errors"
	"fmt"
	"net/http"
)

// HTTPErrorContract maps core approval-decision errors to API response details.
type HTTPErrorContract struct {
	Status  int
	Code    string
	Message string
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
