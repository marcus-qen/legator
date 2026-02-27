package approvalpolicy

import (
	"net/http"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
)

func TestProjectDecideApprovalTransportParity(t *testing.T) {
	t.Run("error projection", func(t *testing.T) {
		contract := &DecideApprovalTransportContract{
			Err: &HTTPErrorContract{
				Status:  http.StatusBadGateway,
				Code:    "bad_gateway",
				Message: "approved but dispatch failed: probe p1 not connected",
			},
		}

		projection := ProjectDecideApprovalTransport(contract)
		if projection == nil {
			t.Fatal("expected projection")
		}
		httpErr, ok := projection.HTTPError()
		if !ok {
			t.Fatalf("expected projected error contract, got %+v", projection)
		}
		if *httpErr != *contract.Err {
			t.Fatalf("unexpected projected error contract: got %+v want %+v", httpErr, contract.Err)
		}
		if projection.Success != nil {
			t.Fatalf("expected nil success when error is present, got %+v", projection.Success)
		}
	})

	t.Run("success projection", func(t *testing.T) {
		req := &approval.Request{ID: "req-projection", Decision: approval.DecisionDenied}
		contract := EncodeDecideApprovalTransport(&ApprovalDecisionResult{Request: req}, nil)

		projection := ProjectDecideApprovalTransport(contract)
		if projection == nil {
			t.Fatal("expected projection")
		}
		if httpErr, ok := projection.HTTPError(); ok {
			t.Fatalf("unexpected projected error: %+v", httpErr)
		}
		if projection.Success == nil {
			t.Fatalf("expected projected success, got %+v", projection)
		}
		if projection.Success.Status != string(approval.DecisionDenied) {
			t.Fatalf("expected denied status, got %q", projection.Success.Status)
		}
		if projection.Success.Request != req {
			t.Fatalf("expected request passthrough, got %+v", projection.Success.Request)
		}
	})
}
