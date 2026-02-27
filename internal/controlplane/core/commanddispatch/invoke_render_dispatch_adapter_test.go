package commanddispatch

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
)

type commandInvokeHTTPCapture struct {
	httpErr    *HTTPErrorContract
	dispatched string
	result     *protocol.CommandResultPayload
}

type commandInvokeMCPCapture struct {
	err  error
	text string
}

func TestDispatchCommandInvokeProjection_HTTPParityWithLegacySequencing(t *testing.T) {
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
				RequestID:     "req-null",
				WaitForResult: true,
				Envelope:      &CommandResultEnvelope{},
			},
		},
		{
			name: "unsupported_surface",
			projection: &CommandInvokeProjection{
				Surface:       ProjectionDispatchSurface("bogus"),
				RequestID:     "req-unsupported",
				WaitForResult: true,
				Envelope:      &CommandResultEnvelope{},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legacy := legacyDispatchCommandInvokeProjectionHTTP(tc.projection)
			adapter := dispatchCommandInvokeProjectionHTTPCapture(tc.projection)

			if !httpErrorContractEqual(legacy.httpErr, adapter.httpErr) {
				t.Fatalf("http error mismatch: legacy=%+v adapter=%+v", legacy.httpErr, adapter.httpErr)
			}
			if legacy.dispatched != adapter.dispatched {
				t.Fatalf("dispatched request mismatch: legacy=%q adapter=%q", legacy.dispatched, adapter.dispatched)
			}
			if !reflect.DeepEqual(legacy.result, adapter.result) {
				t.Fatalf("result mismatch: legacy=%+v adapter=%+v", legacy.result, adapter.result)
			}
		})
	}
}

func TestDispatchCommandInvokeProjection_MCPParityWithLegacySequencing(t *testing.T) {
	tests := []struct {
		name       string
		projection *CommandInvokeProjection
	}{
		{name: "nil_projection", projection: nil},
		{
			name: "nil_envelope",
			projection: &CommandInvokeProjection{
				Surface:   ProjectionDispatchSurfaceMCP,
				RequestID: "req-nil-envelope",
			},
		},
		{
			name: "dispatch_error",
			projection: &CommandInvokeProjection{
				Surface:   ProjectionDispatchSurfaceMCP,
				RequestID: "req-dispatch-error",
				Envelope:  &CommandResultEnvelope{Err: errors.New("not connected")},
			},
		},
		{
			name: "timeout",
			projection: &CommandInvokeProjection{
				Surface:       ProjectionDispatchSurfaceMCP,
				RequestID:     "req-timeout",
				WaitForResult: true,
				Envelope:      &CommandResultEnvelope{Err: ErrTimeout},
			},
		},
		{
			name: "context_canceled",
			projection: &CommandInvokeProjection{
				Surface:       ProjectionDispatchSurfaceMCP,
				RequestID:     "req-canceled",
				WaitForResult: true,
				Envelope:      &CommandResultEnvelope{Err: context.Canceled},
			},
		},
		{
			name: "nil_result",
			projection: &CommandInvokeProjection{
				Surface:       ProjectionDispatchSurfaceMCP,
				RequestID:     "req-empty-result",
				WaitForResult: true,
				Envelope:      &CommandResultEnvelope{},
			},
		},
		{
			name: "success",
			projection: &CommandInvokeProjection{
				Surface:       ProjectionDispatchSurfaceMCP,
				RequestID:     "req-success",
				WaitForResult: true,
				Envelope: &CommandResultEnvelope{Result: &protocol.CommandResultPayload{
					ExitCode: 0,
					Stdout:   " ok ",
				}},
			},
		},
		{
			name: "unsupported_surface",
			projection: &CommandInvokeProjection{
				Surface:       ProjectionDispatchSurface("bogus"),
				RequestID:     "req-unsupported",
				WaitForResult: true,
				Envelope: &CommandResultEnvelope{Result: &protocol.CommandResultPayload{
					ExitCode: 0,
					Stdout:   "ok",
				}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legacy := legacyDispatchCommandInvokeProjectionMCP(tc.projection)
			adapter := dispatchCommandInvokeProjectionMCPCapture(tc.projection)

			if (legacy.err == nil) != (adapter.err == nil) {
				t.Fatalf("error presence mismatch: legacy=%v adapter=%v", legacy.err, adapter.err)
			}
			if legacy.err != nil && adapter.err != nil {
				if !errors.Is(adapter.err, legacy.err) && adapter.err.Error() != legacy.err.Error() {
					t.Fatalf("error mismatch: legacy=%v adapter=%v", legacy.err, adapter.err)
				}
			}
			if legacy.text != adapter.text {
				t.Fatalf("text mismatch: legacy=%q adapter=%q", legacy.text, adapter.text)
			}
		})
	}
}

func legacyDispatchCommandInvokeProjectionHTTP(projection *CommandInvokeProjection) commandInvokeHTTPCapture {
	capture := commandInvokeHTTPCapture{}

	if projection == nil || projection.Envelope == nil {
		capture.httpErr = &HTTPErrorContract{Status: http.StatusBadGateway, Code: "bad_gateway", Message: "command dispatch failed"}
		return capture
	}

	handled := DispatchCommandErrorsForSurface(projection.Envelope, projection.Surface, CommandProjectionDispatchWriter{
		WriteHTTPError: func(httpErr *HTTPErrorContract) {
			capture.httpErr = httpErr
		},
	})
	if handled {
		return capture
	}

	if !projection.WaitForResult {
		capture.dispatched = projection.RequestID
		return capture
	}

	DispatchCommandReadForSurface(projection.Envelope.Result, projection.Surface, CommandProjectionDispatchWriter{
		WriteHTTPResult: func(result *protocol.CommandResultPayload) {
			capture.result = result
		},
	})
	return capture
}

func dispatchCommandInvokeProjectionHTTPCapture(projection *CommandInvokeProjection) commandInvokeHTTPCapture {
	capture := commandInvokeHTTPCapture{}
	DispatchCommandInvokeProjection(projection, CommandInvokeRenderDispatchWriter{
		WriteHTTPError: func(httpErr *HTTPErrorContract) {
			capture.httpErr = httpErr
		},
		WriteHTTPDispatched: func(requestID string) {
			capture.dispatched = requestID
		},
		WriteHTTPResult: func(result *protocol.CommandResultPayload) {
			capture.result = result
		},
	})
	return capture
}

func legacyDispatchCommandInvokeProjectionMCP(projection *CommandInvokeProjection) commandInvokeMCPCapture {
	capture := commandInvokeMCPCapture{}

	if projection == nil || projection.Envelope == nil {
		capture.err = ErrEmptyResult
		return capture
	}

	handled := DispatchCommandErrorsForSurface(projection.Envelope, projection.Surface, CommandProjectionDispatchWriter{
		WriteMCPError: func(err error) {
			capture.err = err
		},
	})
	if handled {
		return capture
	}

	if projection.Envelope.Result == nil {
		capture.err = ErrEmptyResult
		return capture
	}

	DispatchCommandReadForSurface(projection.Envelope.Result, projection.Surface, CommandProjectionDispatchWriter{
		WriteMCPText: func(text string) {
			capture.text = text
		},
	})
	return capture
}

func dispatchCommandInvokeProjectionMCPCapture(projection *CommandInvokeProjection) commandInvokeMCPCapture {
	capture := commandInvokeMCPCapture{}
	DispatchCommandInvokeProjection(projection, CommandInvokeRenderDispatchWriter{
		WriteMCPError: func(err error) {
			capture.err = err
		},
		WriteMCPText: func(text string) {
			capture.text = text
		},
	})
	return capture
}

func httpErrorContractEqual(lhs, rhs *HTTPErrorContract) bool {
	switch {
	case lhs == nil && rhs == nil:
		return true
	case lhs == nil || rhs == nil:
		return false
	default:
		return *lhs == *rhs
	}
}
