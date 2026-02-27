package commanddispatch

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
)

func TestDispatchCommandErrorsForSurface_HTTPParity(t *testing.T) {
	envelope := &CommandResultEnvelope{Err: ErrTimeout}
	var got *HTTPErrorContract

	handled := DispatchCommandErrorsForSurface(envelope, ProjectionDispatchSurfaceHTTP, CommandProjectionDispatchWriter{
		WriteHTTPError: func(contract *HTTPErrorContract) {
			got = contract
		},
	})

	if !handled {
		t.Fatal("expected handled=true for HTTP timeout error")
	}
	if got == nil || got.Status != 504 || got.Code != "timeout" || got.Message != "timeout waiting for probe response" {
		t.Fatalf("unexpected HTTP error contract: %+v", got)
	}
}

func TestDispatchCommandErrorsForSurface_MCPParity(t *testing.T) {
	envelope := &CommandResultEnvelope{Err: errors.New("not connected")}
	var got error

	handled := DispatchCommandErrorsForSurface(envelope, ProjectionDispatchSurfaceMCP, CommandProjectionDispatchWriter{
		WriteMCPError: func(err error) {
			got = err
		},
	})

	if !handled {
		t.Fatal("expected handled=true for MCP dispatch error")
	}
	if got == nil || !strings.Contains(got.Error(), "dispatch command: not connected") {
		t.Fatalf("unexpected MCP error: %v", got)
	}
}

func TestDispatchCommandErrorsForSurface_NoError(t *testing.T) {
	handled := DispatchCommandErrorsForSurface(&CommandResultEnvelope{}, ProjectionDispatchSurfaceHTTP, CommandProjectionDispatchWriter{})
	if handled {
		t.Fatal("expected handled=false for success envelope")
	}
}

func TestDispatchCommandErrorsForSurface_ContextCanceledParity(t *testing.T) {
	envelope := &CommandResultEnvelope{Err: context.Canceled}
	var got error

	handled := DispatchCommandErrorsForSurface(envelope, ProjectionDispatchSurfaceMCP, CommandProjectionDispatchWriter{
		WriteMCPError: func(err error) {
			got = err
		},
	})

	if !handled {
		t.Fatal("expected handled=true for canceled envelope")
	}
	if !errors.Is(got, context.Canceled) {
		t.Fatalf("expected context.Canceled passthrough, got %v", got)
	}
}

func TestDispatchCommandReadForSurface_Parity(t *testing.T) {
	result := &protocol.CommandResultPayload{ExitCode: 2, Stderr: " boom "}

	var gotHTTP *protocol.CommandResultPayload
	DispatchCommandReadForSurface(result, ProjectionDispatchSurfaceHTTP, CommandProjectionDispatchWriter{
		WriteHTTPResult: func(payload *protocol.CommandResultPayload) {
			gotHTTP = payload
		},
	})
	if gotHTTP != result {
		t.Fatalf("expected HTTP payload passthrough, got %+v", gotHTTP)
	}

	var gotMCPText string
	DispatchCommandReadForSurface(result, ProjectionDispatchSurfaceMCP, CommandProjectionDispatchWriter{
		WriteMCPText: func(text string) {
			gotMCPText = text
		},
	})
	if gotMCPText != "exit_code=2\nboom" {
		t.Fatalf("unexpected MCP read text: %q", gotMCPText)
	}
}

func TestDispatchCommandErrorsForSurface_UnsupportedSurfaceFallback(t *testing.T) {
	const wantMessage = "unsupported command dispatch surface \"bogus\""

	httpCalled, mcpCalled := false, false
	handled := DispatchCommandErrorsForSurface(nil, ProjectionDispatchSurface("bogus"), CommandProjectionDispatchWriter{
		WriteHTTPError: func(contract *HTTPErrorContract) {
			httpCalled = true
			if contract == nil || contract.Status != http.StatusInternalServerError || contract.Code != "internal_error" || contract.Message != wantMessage {
				t.Fatalf("unexpected unsupported-surface HTTP error: %+v", contract)
			}
		},
		WriteMCPError: func(err error) {
			mcpCalled = err != nil
		},
	})
	if !handled {
		t.Fatal("expected handled=true for unsupported surface")
	}
	if !httpCalled || mcpCalled {
		t.Fatalf("fallback precedence mismatch: http=%v mcp=%v", httpCalled, mcpCalled)
	}

	var got error
	handled = DispatchCommandErrorsForSurface(nil, ProjectionDispatchSurface("bogus"), CommandProjectionDispatchWriter{
		WriteMCPError: func(err error) {
			got = err
		},
	})
	if !handled {
		t.Fatal("expected handled=true for unsupported surface with MCP writer")
	}
	if got == nil || got.Error() != wantMessage {
		t.Fatalf("unexpected unsupported-surface MCP error: %v", got)
	}
}
