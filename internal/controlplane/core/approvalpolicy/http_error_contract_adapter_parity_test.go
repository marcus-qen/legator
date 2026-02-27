package approvalpolicy

import (
	"errors"
	"reflect"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestAdaptApprovalHTTPErrorWriter_ParityWithLegacyInlineConversion(t *testing.T) {
	tests := []struct {
		name string
		err  *transportwriter.HTTPError
	}{
		{name: "nil error", err: nil},
		{name: "status/code/message parity", err: &transportwriter.HTTPError{Status: 502, Code: "bad_gateway", Message: "probe down", SuppressWrite: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotNew *HTTPErrorContract
			adaptedNew := adaptApprovalHTTPErrorWriter(func(contract *HTTPErrorContract) {
				gotNew = contract
			})

			var gotLegacy *HTTPErrorContract
			adaptedLegacy := legacyApprovalHTTPErrorWriter(func(contract *HTTPErrorContract) {
				gotLegacy = contract
			})

			adaptedNew(tt.err)
			adaptedLegacy(tt.err)

			if !reflect.DeepEqual(gotNew, gotLegacy) {
				t.Fatalf("conversion parity mismatch: new=%+v legacy=%+v", gotNew, gotLegacy)
			}
		})
	}

	if got := adaptApprovalHTTPErrorWriter(nil); got != nil {
		t.Fatalf("expected nil writer adapter for nil callback, got type %T", got)
	}
}

func TestAdaptApprovalSuccessPayloadWriter_ParityWithLegacyInlineConversion(t *testing.T) {
	typedSuccess := &DecideApprovalSuccess{Status: "approved"}
	var typedNilSuccess *DecideApprovalSuccess

	tests := []struct {
		name    string
		payload any
	}{
		{name: "typed success payload", payload: typedSuccess},
		{name: "typed nil success payload", payload: typedNilSuccess},
		{name: "untyped nil success payload", payload: nil},
		{name: "wrong payload type", payload: "wrong"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotNew *DecideApprovalSuccess
			adaptedNew := transportwriter.AdaptSuccessPayloadWriter(func(success *DecideApprovalSuccess) {
				gotNew = success
			}, normalizeDecideApprovalSuccess)

			var gotLegacy *DecideApprovalSuccess
			adaptedLegacy := legacyApprovalSuccessPayloadWriter(func(success *DecideApprovalSuccess) {
				gotLegacy = success
			})

			adaptedNew(tt.payload)
			adaptedLegacy(tt.payload)

			if !reflect.DeepEqual(gotNew, gotLegacy) {
				t.Fatalf("success conversion parity mismatch: new=%+v legacy=%+v", gotNew, gotLegacy)
			}
		})
	}

	if got := transportwriter.AdaptSuccessPayloadWriter[*DecideApprovalSuccess](nil, normalizeDecideApprovalSuccess); got != nil {
		t.Fatalf("expected nil success adapter for nil callback, got type %T", got)
	}
}

func TestDispatchDecideApprovalResponseForSurface_EndToEndParityWithLegacyInlineConversion(t *testing.T) {
	errorProjection := ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(nil, &ApprovedDispatchError{Err: errors.New("probe p-adapter not connected")}))

	tests := []struct {
		name       string
		projection *DecideApprovalProjection
		surface    DecideApprovalRenderSurface
	}{
		{name: "http error", projection: errorProjection, surface: DecideApprovalRenderSurfaceHTTP},
		{name: "mcp error", projection: errorProjection, surface: DecideApprovalRenderSurfaceMCP},
		{name: "unsupported fallback", projection: ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(&ApprovalDecisionResult{}, nil)), surface: DecideApprovalRenderSurface("bogus")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := approvalDispatchCapture{}
			DispatchDecideApprovalResponseForSurface(tt.projection, tt.surface, DecideApprovalResponseDispatchWriter{
				WriteHTTPError: func(err *HTTPErrorContract) { got.httpErr = err },
				WriteMCPError:  func(err error) { got.mcpErr = err },
				WriteSuccess:   func(success *DecideApprovalSuccess) { got.success = success },
			})

			want := approvalDispatchCapture{}
			legacyDispatchDecideApprovalResponseForSurface(tt.projection, tt.surface, DecideApprovalResponseDispatchWriter{
				WriteHTTPError: func(err *HTTPErrorContract) { want.httpErr = err },
				WriteMCPError:  func(err error) { want.mcpErr = err },
				WriteSuccess:   func(success *DecideApprovalSuccess) { want.success = success },
			})

			if !approvalDispatchCaptureEqual(got, want) {
				t.Fatalf("wrapper parity mismatch: new=%+v legacy=%+v", got, want)
			}
		})
	}
}

type approvalDispatchCapture struct {
	httpErr *HTTPErrorContract
	mcpErr  error
	success *DecideApprovalSuccess
}

func approvalDispatchCaptureEqual(lhs, rhs approvalDispatchCapture) bool {
	if !reflect.DeepEqual(lhs.httpErr, rhs.httpErr) {
		return false
	}
	if !reflect.DeepEqual(lhs.success, rhs.success) {
		return false
	}
	switch {
	case lhs.mcpErr == nil && rhs.mcpErr == nil:
		return true
	case lhs.mcpErr == nil || rhs.mcpErr == nil:
		return false
	default:
		return lhs.mcpErr.Error() == rhs.mcpErr.Error()
	}
}

func legacyApprovalHTTPErrorWriter(write func(*HTTPErrorContract)) func(*transportwriter.HTTPError) {
	if write == nil {
		return nil
	}
	return func(err *transportwriter.HTTPError) {
		if err == nil {
			return
		}
		write(&HTTPErrorContract{Status: err.Status, Code: err.Code, Message: err.Message})
	}
}

func legacyApprovalSuccessPayloadWriter(write func(*DecideApprovalSuccess)) func(any) {
	if write == nil {
		return nil
	}
	return func(payload any) {
		success, _ := payload.(*DecideApprovalSuccess)
		write(normalizeDecideApprovalSuccess(success))
	}
}

func legacyDispatchUnsupportedDecideApprovalSurfaceFallback(surface string, writer DecideApprovalResponseDispatchWriter) {
	fallbackWriter := transportwriter.UnsupportedSurfaceFallbackWriter{WriteMCPError: writer.WriteMCPError}
	if writer.WriteHTTPError != nil {
		fallbackWriter.WriteHTTPError = func(err *transportwriter.HTTPError) {
			if err == nil {
				return
			}
			writer.WriteHTTPError(&HTTPErrorContract{Status: err.Status, Code: err.Code, Message: err.Message})
		}
	}
	transportwriter.WriteUnsupportedSurfaceFallback(unsupportedDecideApprovalSurfaceEnvelope(surface), fallbackWriter)
}

func legacyDispatchDecideApprovalResponseForSurface(projection *DecideApprovalProjection, surface DecideApprovalRenderSurface, writer DecideApprovalResponseDispatchWriter) {
	builder := DecideApprovalResponseEnvelopeBuilder{Projection: projection}
	transportSurface, ok := ResolveDecideApprovalTransportSurface(surface)
	if !ok {
		legacyDispatchUnsupportedDecideApprovalSurfaceFallback(string(surface), writer)
		return
	}

	legacySuccessWriter := legacyApprovalSuccessPayloadWriter(writer.WriteSuccess)

	transportwriter.WriteFromBuilder(transportSurface, builder, transportwriter.WriterKernel{
		WriteHTTPError: func(err *transportwriter.HTTPError) {
			if writer.WriteHTTPError != nil {
				writer.WriteHTTPError(&HTTPErrorContract{Status: err.Status, Code: err.Code, Message: err.Message})
			}
		},
		WriteMCPError:    writer.WriteMCPError,
		WriteHTTPSuccess: legacySuccessWriter,
		WriteMCPSuccess:  legacySuccessWriter,
	})
}
