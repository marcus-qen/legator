package approvalpolicy

import (
	"errors"
	"net/http"
	"testing"
)

func TestDecideApprovalHTTPErrorMapping(t *testing.T) {
	t.Run("nil error has no mapping", func(t *testing.T) {
		httpErr, ok := DecideApprovalHTTPError(nil)
		if ok {
			t.Fatalf("expected no mapping, got %+v", httpErr)
		}
	})

	t.Run("approved dispatch failure maps to bad gateway", func(t *testing.T) {
		httpErr, ok := DecideApprovalHTTPError(&ApprovedDispatchError{Err: errors.New("not connected")})
		if !ok {
			t.Fatal("expected HTTP mapping")
		}
		if httpErr.Status != http.StatusBadGateway || httpErr.Code != "bad_gateway" {
			t.Fatalf("unexpected HTTP mapping: %+v", httpErr)
		}
		if httpErr.Message != "approved but dispatch failed: not connected" {
			t.Fatalf("unexpected message: %q", httpErr.Message)
		}
	})

	t.Run("decision hook failure maps to internal error", func(t *testing.T) {
		httpErr, ok := DecideApprovalHTTPError(&DecisionHookError{Stage: DecisionHookStageDecisionRecorded, Err: errors.New("audit down")})
		if !ok {
			t.Fatal("expected HTTP mapping")
		}
		if httpErr.Status != http.StatusInternalServerError || httpErr.Code != "internal_error" {
			t.Fatalf("unexpected HTTP mapping: %+v", httpErr)
		}
		if httpErr.Message != "audit down" {
			t.Fatalf("unexpected message: %q", httpErr.Message)
		}
	})

	t.Run("other errors map to invalid request", func(t *testing.T) {
		httpErr, ok := DecideApprovalHTTPError(errors.New("invalid decision \"maybe\""))
		if !ok {
			t.Fatal("expected HTTP mapping")
		}
		if httpErr.Status != http.StatusBadRequest || httpErr.Code != "invalid_request" {
			t.Fatalf("unexpected HTTP mapping: %+v", httpErr)
		}
		if httpErr.Message != "invalid decision \"maybe\"" {
			t.Fatalf("unexpected message: %q", httpErr.Message)
		}
	})
}
