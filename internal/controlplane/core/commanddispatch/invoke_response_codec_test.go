package commanddispatch

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
)

func TestEncodeCommandInvokeHTTPJSONResponse_ParityWithLegacy(t *testing.T) {
	tests := []struct {
		name       string
		projection *CommandInvokeProjection
	}{
		{name: "nil_projection", projection: nil},
		{
			name: "nil_envelope",
			projection: &CommandInvokeProjection{
				Surface:   ProjectionDispatchSurfaceHTTP,
				RequestID: "req-nil-envelope",
			},
		},
		{
			name: "dispatch_error",
			projection: &CommandInvokeProjection{
				Surface:   ProjectionDispatchSurfaceHTTP,
				RequestID: "req-dispatch-error",
				Envelope:  &CommandResultEnvelope{Err: errors.New("not connected")},
			},
		},
		{
			name: "timeout",
			projection: &CommandInvokeProjection{
				Surface:       ProjectionDispatchSurfaceHTTP,
				RequestID:     "req-timeout",
				WaitForResult: true,
				Envelope:      &CommandResultEnvelope{Err: ErrTimeout},
			},
		},
		{
			name: "context_canceled",
			projection: &CommandInvokeProjection{
				Surface:       ProjectionDispatchSurfaceHTTP,
				RequestID:     "req-canceled",
				WaitForResult: true,
				Envelope:      &CommandResultEnvelope{Err: context.Canceled},
			},
		},
		{
			name: "dispatch_success",
			projection: &CommandInvokeProjection{
				Surface:   ProjectionDispatchSurfaceHTTP,
				RequestID: "req-dispatched",
				Envelope:  &CommandResultEnvelope{Dispatched: true},
			},
		},
		{
			name: "wait_success",
			projection: &CommandInvokeProjection{
				Surface:       ProjectionDispatchSurfaceHTTP,
				RequestID:     "req-result",
				WaitForResult: true,
				Envelope: &CommandResultEnvelope{Result: &protocol.CommandResultPayload{
					RequestID: "req-result",
					ExitCode:  0,
					Stdout:    "ok",
				}},
			},
		},
		{
			name: "wait_nil_result",
			projection: &CommandInvokeProjection{
				Surface:       ProjectionDispatchSurfaceHTTP,
				RequestID:     "req-empty",
				WaitForResult: true,
				Envelope:      &CommandResultEnvelope{},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legacy := legacyEncodeCommandInvokeHTTPJSONResponse(tc.projection)
			codec := EncodeCommandInvokeHTTPJSONResponse(tc.projection)

			if legacy.Status != codec.Status {
				t.Fatalf("status mismatch: legacy=%d codec=%d", legacy.Status, codec.Status)
			}
			if legacy.HasBody != codec.HasBody {
				t.Fatalf("has-body mismatch: legacy=%v codec=%v", legacy.HasBody, codec.HasBody)
			}
			if legacy.SuppressWrite != codec.SuppressWrite {
				t.Fatalf("suppress-write mismatch: legacy=%v codec=%v", legacy.SuppressWrite, codec.SuppressWrite)
			}
			if !reflect.DeepEqual(legacy.Body, codec.Body) {
				t.Fatalf("body mismatch: legacy=%#v codec=%#v", legacy.Body, codec.Body)
			}
		})
	}
}

func TestEncodeCommandInvokeMCPTextResponse_ParityWithLegacy(t *testing.T) {
	tests := []struct {
		name       string
		projection *CommandInvokeProjection
	}{
		{name: "nil_projection", projection: nil},
		{
			name: "nil_envelope",
			projection: &CommandInvokeProjection{
				Surface: ProjectionDispatchSurfaceMCP,
			},
		},
		{
			name: "dispatch_error",
			projection: &CommandInvokeProjection{
				Surface:  ProjectionDispatchSurfaceMCP,
				Envelope: &CommandResultEnvelope{Err: errors.New("not connected")},
			},
		},
		{
			name: "timeout",
			projection: &CommandInvokeProjection{
				Surface:  ProjectionDispatchSurfaceMCP,
				Envelope: &CommandResultEnvelope{Err: ErrTimeout},
			},
		},
		{
			name: "context_canceled",
			projection: &CommandInvokeProjection{
				Surface:  ProjectionDispatchSurfaceMCP,
				Envelope: &CommandResultEnvelope{Err: context.Canceled},
			},
		},
		{
			name: "nil_result",
			projection: &CommandInvokeProjection{
				Surface:  ProjectionDispatchSurfaceMCP,
				Envelope: &CommandResultEnvelope{},
			},
		},
		{
			name: "success",
			projection: &CommandInvokeProjection{
				Surface: ProjectionDispatchSurfaceMCP,
				Envelope: &CommandResultEnvelope{Result: &protocol.CommandResultPayload{
					ExitCode: 0,
					Stdout:   " ok ",
				}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legacyText, legacyErr := legacyEncodeCommandInvokeMCPTextResponse(tc.projection)
			codecText, codecErr := EncodeCommandInvokeMCPTextResponse(tc.projection)

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

func legacyEncodeCommandInvokeHTTPJSONResponse(projection *CommandInvokeProjection) CommandInvokeHTTPJSONResponse {
	response := CommandInvokeHTTPJSONResponse{Status: http.StatusOK}
	if projection == nil || projection.Envelope == nil {
		response.Status = http.StatusBadGateway
		response.Body = HTTPJSONErrorPayload{Error: "command dispatch failed", Code: "bad_gateway"}
		response.HasBody = true
		return response
	}

	if httpErr, ok := projection.Envelope.HTTPError(); ok {
		if httpErr.SuppressWrite {
			response.SuppressWrite = true
			return response
		}
		response.Status = httpErr.Status
		response.Body = HTTPJSONErrorPayload{Error: httpErr.Message, Code: httpErr.Code}
		response.HasBody = true
		return response
	}

	if !projection.WaitForResult {
		response.Body = map[string]string{
			"status":     "dispatched",
			"request_id": projection.RequestID,
		}
		response.HasBody = true
		return response
	}

	response.Body = projection.Envelope.Result
	response.HasBody = true
	return response
}

func legacyEncodeCommandInvokeMCPTextResponse(projection *CommandInvokeProjection) (string, error) {
	if projection == nil || projection.Envelope == nil {
		return "", ErrEmptyResult
	}
	if err := projection.Envelope.MCPError(); err != nil {
		return "", err
	}
	if projection.Envelope.Result == nil {
		return "", ErrEmptyResult
	}
	return ResultText(projection.Envelope.Result), nil
}
