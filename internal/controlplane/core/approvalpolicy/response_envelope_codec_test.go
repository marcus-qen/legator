package approvalpolicy

import (
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestDecideApprovalResponseEnvelopeBuilder_ParityWithLegacyHTTP(t *testing.T) {
	tests := []struct {
		name       string
		projection *DecideApprovalProjection
	}{
		{name: "nil projection", projection: nil},
		{name: "error", projection: ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(nil, &ApprovedDispatchError{Err: errors.New("probe p1 not connected")}))},
		{name: "success", projection: ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(&ApprovalDecisionResult{Request: &approval.Request{ID: "req-http", Decision: approval.DecisionDenied}}, nil))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			legacyErr, legacySuccess := legacyDecideApprovalHTTPDispatch(tt.projection)
			builder := DecideApprovalResponseEnvelopeBuilder{Projection: tt.projection}
			envelope := builder.BuildResponseEnvelope(transportwriter.SurfaceHTTP)
			codecErr, codecSuccess := approvalHTTPDispatchFromEnvelope(envelope)

			if !reflect.DeepEqual(legacyErr, codecErr) {
				t.Fatalf("http error mismatch: legacy=%+v codec=%+v", legacyErr, codecErr)
			}
			if !reflect.DeepEqual(legacySuccess, codecSuccess) {
				t.Fatalf("http success mismatch: legacy=%+v codec=%+v", legacySuccess, codecSuccess)
			}
		})
	}
}

func TestDecideApprovalResponseEnvelopeBuilder_ParityWithLegacyMCP(t *testing.T) {
	tests := []struct {
		name       string
		projection *DecideApprovalProjection
	}{
		{name: "nil projection", projection: nil},
		{name: "error", projection: ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(nil, &ApprovedDispatchError{Err: errors.New("probe p2 not connected")}))},
		{name: "success", projection: ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(&ApprovalDecisionResult{Request: &approval.Request{ID: "req-mcp", Decision: approval.DecisionApproved}}, nil))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			legacyErr, legacySuccess := legacyDecideApprovalMCPDispatch(tt.projection)
			builder := DecideApprovalResponseEnvelopeBuilder{Projection: tt.projection}
			envelope := builder.BuildResponseEnvelope(transportwriter.SurfaceMCP)
			codecErr, codecSuccess := approvalMCPDispatchFromEnvelope(envelope)

			if (legacyErr == nil) != (codecErr == nil) {
				t.Fatalf("mcp error presence mismatch: legacy=%v codec=%v", legacyErr, codecErr)
			}
			if legacyErr != nil && codecErr != nil && legacyErr.Error() != codecErr.Error() {
				t.Fatalf("mcp error mismatch: legacy=%v codec=%v", legacyErr, codecErr)
			}
			if !reflect.DeepEqual(legacySuccess, codecSuccess) {
				t.Fatalf("mcp success mismatch: legacy=%+v codec=%+v", legacySuccess, codecSuccess)
			}
		})
	}
}

func legacyDecideApprovalHTTPDispatch(projection *DecideApprovalProjection) (*HTTPErrorContract, *DecideApprovalSuccess) {
	if projection == nil {
		projection = ProjectDecideApprovalTransport(nil)
	}
	if httpErr, ok := projection.HTTPError(); ok {
		return httpErr, nil
	}
	return nil, normalizeDecideApprovalSuccess(projection.Success)
}

func legacyDecideApprovalMCPDispatch(projection *DecideApprovalProjection) (error, *DecideApprovalSuccess) {
	if projection == nil {
		projection = ProjectDecideApprovalTransport(nil)
	}
	if err := projection.MCPError(); err != nil {
		return err, nil
	}
	return nil, normalizeDecideApprovalSuccess(projection.Success)
}

func approvalHTTPDispatchFromEnvelope(envelope *transportwriter.ResponseEnvelope) (*HTTPErrorContract, *DecideApprovalSuccess) {
	var (
		httpErr *HTTPErrorContract
		success *DecideApprovalSuccess
	)
	transportwriter.WriteForSurface(transportwriter.SurfaceHTTP, envelope, transportwriter.WriterKernel{
		WriteHTTPError: func(err *transportwriter.HTTPError) {
			httpErr = &HTTPErrorContract{Status: err.Status, Code: err.Code, Message: err.Message}
		},
		WriteHTTPSuccess: func(payload any) {
			success, _ = payload.(*DecideApprovalSuccess)
		},
	})
	return httpErr, success
}

func approvalMCPDispatchFromEnvelope(envelope *transportwriter.ResponseEnvelope) (error, *DecideApprovalSuccess) {
	var (
		err     error
		success *DecideApprovalSuccess
	)
	transportwriter.WriteForSurface(transportwriter.SurfaceMCP, envelope, transportwriter.WriterKernel{
		WriteMCPError: func(dispatchErr error) {
			err = dispatchErr
		},
		WriteMCPSuccess: func(payload any) {
			success, _ = payload.(*DecideApprovalSuccess)
		},
	})
	if err != nil {
		return err, nil
	}
	return nil, success
}

func TestEncodeDecideApprovalResponseEnvelope_UnsupportedSurface(t *testing.T) {
	envelope := EncodeDecideApprovalResponseEnvelope(&DecideApprovalProjection{}, DecideApprovalRenderSurface("bogus"))
	if envelope.HTTPError == nil {
		t.Fatal("expected unsupported-surface HTTP envelope error")
	}
	if envelope.HTTPError.Status != http.StatusInternalServerError || envelope.HTTPError.Code != "internal_error" {
		t.Fatalf("unexpected unsupported-surface HTTP envelope error: %+v", envelope.HTTPError)
	}
	if envelope.MCPError == nil {
		t.Fatal("expected unsupported-surface MCP envelope error")
	}
}
