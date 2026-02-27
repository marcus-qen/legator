package commanddispatch

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
	"github.com/marcus-qen/legator/internal/protocol"
)

func TestCommandInvokeResponseEnvelopeBuilder_ParityWithLegacyHTTP(t *testing.T) {
	tests := []struct {
		name       string
		projection *CommandInvokeProjection
	}{
		{name: "nil_projection", projection: nil},
		{name: "nil_envelope", projection: &CommandInvokeProjection{Surface: ProjectionDispatchSurfaceHTTP, RequestID: "req-nil"}},
		{name: "dispatch_error", projection: &CommandInvokeProjection{Surface: ProjectionDispatchSurfaceHTTP, RequestID: "req-dispatch", Envelope: &CommandResultEnvelope{Err: errors.New("not connected")}}},
		{name: "timeout", projection: &CommandInvokeProjection{Surface: ProjectionDispatchSurfaceHTTP, RequestID: "req-timeout", WaitForResult: true, Envelope: &CommandResultEnvelope{Err: ErrTimeout}}},
		{name: "context_canceled", projection: &CommandInvokeProjection{Surface: ProjectionDispatchSurfaceHTTP, RequestID: "req-canceled", WaitForResult: true, Envelope: &CommandResultEnvelope{Err: context.Canceled}}},
		{name: "dispatch_success", projection: &CommandInvokeProjection{Surface: ProjectionDispatchSurfaceHTTP, RequestID: "req-dispatched", Envelope: &CommandResultEnvelope{Dispatched: true}}},
		{name: "wait_success", projection: &CommandInvokeProjection{Surface: ProjectionDispatchSurfaceHTTP, RequestID: "req-result", WaitForResult: true, Envelope: &CommandResultEnvelope{Result: &protocol.CommandResultPayload{RequestID: "req-result", ExitCode: 0, Stdout: "ok"}}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legacy := legacyEncodeCommandInvokeHTTPJSONResponse(tc.projection)
			builder := CommandInvokeResponseEnvelopeBuilder{Projection: tc.projection}
			envelope := builder.BuildResponseEnvelope(transportwriter.SurfaceHTTP)
			codec := commandInvokeHTTPResponseFromEnvelope(envelope)

			if legacy.Status != codec.Status || legacy.HasBody != codec.HasBody || legacy.SuppressWrite != codec.SuppressWrite {
				t.Fatalf("status/meta mismatch: legacy=%+v codec=%+v", legacy, codec)
			}
			if !reflect.DeepEqual(legacy.Body, codec.Body) {
				t.Fatalf("body mismatch: legacy=%#v codec=%#v", legacy.Body, codec.Body)
			}
		})
	}
}

func TestCommandInvokeResponseEnvelopeBuilder_ParityWithLegacyMCP(t *testing.T) {
	tests := []struct {
		name       string
		projection *CommandInvokeProjection
	}{
		{name: "nil_projection", projection: nil},
		{name: "nil_envelope", projection: &CommandInvokeProjection{Surface: ProjectionDispatchSurfaceMCP}},
		{name: "dispatch_error", projection: &CommandInvokeProjection{Surface: ProjectionDispatchSurfaceMCP, Envelope: &CommandResultEnvelope{Err: errors.New("not connected")}}},
		{name: "timeout", projection: &CommandInvokeProjection{Surface: ProjectionDispatchSurfaceMCP, Envelope: &CommandResultEnvelope{Err: ErrTimeout}}},
		{name: "context_canceled", projection: &CommandInvokeProjection{Surface: ProjectionDispatchSurfaceMCP, Envelope: &CommandResultEnvelope{Err: context.Canceled}}},
		{name: "nil_result", projection: &CommandInvokeProjection{Surface: ProjectionDispatchSurfaceMCP, Envelope: &CommandResultEnvelope{}}},
		{name: "success", projection: &CommandInvokeProjection{Surface: ProjectionDispatchSurfaceMCP, Envelope: &CommandResultEnvelope{Result: &protocol.CommandResultPayload{ExitCode: 2, Stderr: " boom "}}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legacyText, legacyErr := legacyEncodeCommandInvokeMCPTextResponse(tc.projection)
			builder := CommandInvokeResponseEnvelopeBuilder{Projection: tc.projection}
			envelope := builder.BuildResponseEnvelope(transportwriter.SurfaceMCP)
			codecText, codecErr := commandInvokeMCPResponseFromEnvelope(envelope)

			if (legacyErr == nil) != (codecErr == nil) {
				t.Fatalf("error presence mismatch: legacy=%v codec=%v", legacyErr, codecErr)
			}
			if legacyErr != nil && codecErr != nil {
				if !errors.Is(codecErr, legacyErr) && codecErr.Error() != legacyErr.Error() {
					t.Fatalf("error mismatch: legacy=%v codec=%v", legacyErr, codecErr)
				}
			}
			if legacyText != codecText {
				t.Fatalf("text mismatch: legacy=%q codec=%q", legacyText, codecText)
			}
		})
	}
}

func commandInvokeHTTPResponseFromEnvelope(envelope *transportwriter.ResponseEnvelope) CommandInvokeHTTPJSONResponse {
	response := CommandInvokeHTTPJSONResponse{Status: http.StatusOK}
	transportwriter.WriteForSurface(transportwriter.SurfaceHTTP, envelope, transportwriter.WriterKernel{
		WriteHTTPError: func(httpErr *transportwriter.HTTPError) {
			response.Status = httpErr.Status
			response.Body = HTTPJSONErrorPayload{Error: httpErr.Message, Code: httpErr.Code}
			response.HasBody = true
		},
		WriteHTTPSuccess: func(payload any) {
			response.Status = http.StatusOK
			response.Body = payload
			response.HasBody = true
		},
	})
	if envelope != nil && envelope.HTTPError != nil && envelope.HTTPError.SuppressWrite {
		response.SuppressWrite = true
		response.HasBody = false
		response.Body = nil
	}
	return response
}

func commandInvokeMCPResponseFromEnvelope(envelope *transportwriter.ResponseEnvelope) (string, error) {
	text := ""
	var err error
	transportwriter.WriteForSurface(transportwriter.SurfaceMCP, envelope, transportwriter.WriterKernel{
		WriteMCPError: func(dispatchErr error) {
			err = dispatchErr
		},
		WriteMCPSuccess: func(payload any) {
			value, _ := payload.(string)
			text = value
		},
	})
	if err != nil {
		return "", err
	}
	return text, nil
}

func TestEncodeCommandInvokeResponseEnvelope_UnsupportedSurface(t *testing.T) {
	const wantMessage = "unsupported command invoke surface \"bogus\""
	envelope := EncodeCommandInvokeResponseEnvelope(nil, ProjectionDispatchSurface("bogus"))
	if envelope.HTTPError == nil {
		t.Fatal("expected unsupported-surface HTTP envelope error")
	}
	if envelope.HTTPError.Status != http.StatusInternalServerError || envelope.HTTPError.Code != "internal_error" || envelope.HTTPError.Message != wantMessage {
		t.Fatalf("unexpected unsupported-surface HTTP envelope error: %+v", envelope.HTTPError)
	}
	if envelope.MCPError == nil {
		t.Fatal("expected unsupported-surface MCP envelope error")
	}
	if envelope.MCPError.Error() != wantMessage {
		t.Fatalf("unexpected unsupported-surface MCP envelope error: %v", envelope.MCPError)
	}
}
