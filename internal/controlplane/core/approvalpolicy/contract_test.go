package approvalpolicy

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
)

func TestDecodeDecideApprovalRequest(t *testing.T) {
	t.Run("invalid json", func(t *testing.T) {
		decoded, httpErr := DecodeDecideApprovalRequest(strings.NewReader("{"))
		if decoded != nil {
			t.Fatalf("expected nil decode result, got %+v", decoded)
		}
		if httpErr == nil {
			t.Fatal("expected HTTP error contract")
		}
		if httpErr.Status != http.StatusBadRequest || httpErr.Code != "invalid_request" || httpErr.Message != "invalid request body" {
			t.Fatalf("unexpected HTTP contract: %+v", httpErr)
		}
	})

	t.Run("missing fields", func(t *testing.T) {
		decoded, httpErr := DecodeDecideApprovalRequest(strings.NewReader(`{"decision":"denied"}`))
		if decoded != nil {
			t.Fatalf("expected nil decode result, got %+v", decoded)
		}
		if httpErr == nil {
			t.Fatal("expected HTTP error contract")
		}
		if httpErr.Status != http.StatusBadRequest || httpErr.Code != "invalid_request" || httpErr.Message != "decision and decided_by are required" {
			t.Fatalf("unexpected HTTP contract: %+v", httpErr)
		}
	})

	t.Run("valid payload", func(t *testing.T) {
		decoded, httpErr := DecodeDecideApprovalRequest(strings.NewReader(`{"decision":"approved","decided_by":"operator"}`))
		if httpErr != nil {
			t.Fatalf("unexpected HTTP error contract: %+v", httpErr)
		}
		if decoded == nil {
			t.Fatal("expected decoded request")
		}
		if decoded.Decision != approval.DecisionApproved {
			t.Fatalf("expected approved decision, got %q", decoded.Decision)
		}
		if decoded.DecidedBy != "operator" {
			t.Fatalf("expected decided_by=operator, got %q", decoded.DecidedBy)
		}
	})
}

func TestEncodeDecideApprovalSuccess(t *testing.T) {
	req := &approval.Request{ID: "req-1", Decision: approval.DecisionDenied}
	encoded := EncodeDecideApprovalSuccess(&ApprovalDecisionResult{Request: req})
	if encoded == nil {
		t.Fatal("expected success contract")
	}
	if encoded.Status != string(approval.DecisionDenied) {
		t.Fatalf("expected denied status, got %q", encoded.Status)
	}
	if encoded.Request != req {
		t.Fatalf("expected request passthrough, got %+v", encoded.Request)
	}
}

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
