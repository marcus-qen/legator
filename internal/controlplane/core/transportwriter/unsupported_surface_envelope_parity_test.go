package transportwriter_test

import (
	"net/http"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
	"github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestUnsupportedSurfaceEnvelopeFactory_Semantics(t *testing.T) {
	const message = "unsupported surface"
	envelope := transportwriter.UnsupportedSurfaceEnvelope(message)

	assertUnsupportedEnvelopeSemantics(t, envelope, message)
}

func TestUnsupportedSurfaceEnvelopeParity_ApprovalAndCommandCodecs(t *testing.T) {
	t.Run("approval_codec", func(t *testing.T) {
		const message = "unsupported approval decide dispatch surface \"bogus\""
		envelope := approvalpolicy.EncodeDecideApprovalResponseEnvelope(nil, approvalpolicy.DecideApprovalRenderSurface("bogus"))
		assertUnsupportedEnvelopeSemantics(t, envelope, message)
	})

	t.Run("command_invoke_codec", func(t *testing.T) {
		const message = "unsupported command invoke surface \"bogus\""
		envelope := commanddispatch.EncodeCommandInvokeResponseEnvelope(nil, commanddispatch.ProjectionDispatchSurface("bogus"))
		assertUnsupportedEnvelopeSemantics(t, envelope, message)
	})
}

func TestUnsupportedSurfaceEnvelopeParity_ApprovalAndCommandAdapters(t *testing.T) {
	t.Run("http_writer_precedes_mcp_writer", func(t *testing.T) {
		const approvalMessage = "unsupported approval decide dispatch surface \"bogus\""
		approvalHTTP, approvalMCP := false, false
		approvalpolicy.DispatchDecideApprovalResponseForSurface(nil, approvalpolicy.DecideApprovalRenderSurface("bogus"), approvalpolicy.DecideApprovalResponseDispatchWriter{
			WriteHTTPError: func(err *approvalpolicy.HTTPErrorContract) {
				if err == nil || err.Status != http.StatusInternalServerError || err.Code != "internal_error" || err.Message != approvalMessage {
					t.Fatalf("unexpected approval unsupported-surface HTTP error: %+v", err)
				}
				approvalHTTP = true
			},
			WriteMCPError: func(err error) {
				approvalMCP = err != nil
			},
		})
		if !approvalHTTP || approvalMCP {
			t.Fatalf("approval fallback precedence mismatch: http=%v mcp=%v", approvalHTTP, approvalMCP)
		}

		const commandMessage = "unsupported command dispatch surface \"bogus\""
		commandHTTP, commandMCP := false, false
		handled := commanddispatch.DispatchCommandErrorsForSurface(nil, commanddispatch.ProjectionDispatchSurface("bogus"), commanddispatch.CommandProjectionDispatchWriter{
			WriteHTTPError: func(err *commanddispatch.HTTPErrorContract) {
				if err == nil || err.Status != http.StatusInternalServerError || err.Code != "internal_error" || err.Message != commandMessage {
					t.Fatalf("unexpected command unsupported-surface HTTP error: %+v", err)
				}
				commandHTTP = true
			},
			WriteMCPError: func(err error) {
				commandMCP = err != nil
			},
		})
		if !handled {
			t.Fatal("expected handled=true for unsupported command surface")
		}
		if !commandHTTP || commandMCP {
			t.Fatalf("command fallback precedence mismatch: http=%v mcp=%v", commandHTTP, commandMCP)
		}
	})

	t.Run("mcp_writer_used_when_http_writer_absent", func(t *testing.T) {
		const approvalMessage = "unsupported approval decide dispatch surface \"bogus\""
		var approvalErr error
		approvalpolicy.DispatchDecideApprovalResponseForSurface(nil, approvalpolicy.DecideApprovalRenderSurface("bogus"), approvalpolicy.DecideApprovalResponseDispatchWriter{
			WriteMCPError: func(err error) {
				approvalErr = err
			},
		})
		if approvalErr == nil || approvalErr.Error() != approvalMessage {
			t.Fatalf("unexpected approval unsupported-surface MCP error: %v", approvalErr)
		}

		const commandMessage = "unsupported command dispatch surface \"bogus\""
		var commandErr error
		handled := commanddispatch.DispatchCommandErrorsForSurface(nil, commanddispatch.ProjectionDispatchSurface("bogus"), commanddispatch.CommandProjectionDispatchWriter{
			WriteMCPError: func(err error) {
				commandErr = err
			},
		})
		if !handled {
			t.Fatal("expected handled=true for unsupported command surface")
		}
		if commandErr == nil || commandErr.Error() != commandMessage {
			t.Fatalf("unexpected command unsupported-surface MCP error: %v", commandErr)
		}
	})
}

func assertUnsupportedEnvelopeSemantics(t *testing.T, envelope *transportwriter.ResponseEnvelope, message string) {
	t.Helper()
	if envelope == nil {
		t.Fatal("expected unsupported-surface envelope")
	}
	if envelope.HTTPError == nil {
		t.Fatal("expected unsupported-surface HTTP error")
	}
	if envelope.HTTPError.Status != http.StatusInternalServerError || envelope.HTTPError.Code != "internal_error" || envelope.HTTPError.Message != message {
		t.Fatalf("unexpected unsupported-surface HTTP error: %+v", envelope.HTTPError)
	}
	if envelope.MCPError == nil {
		t.Fatal("expected unsupported-surface MCP error")
	}
	if envelope.MCPError.Error() != message {
		t.Fatalf("unexpected unsupported-surface MCP message: got %q want %q", envelope.MCPError.Error(), message)
	}
}
