package transportwriter_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
	"github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestResolveTransportSurface_ApprovalAndCommandParity(t *testing.T) {
	tests := []struct {
		name            string
		approvalSurface approvalpolicy.DecideApprovalRenderSurface
		commandSurface  commanddispatch.ProjectionDispatchSurface
		want            transportwriter.Surface
		ok              bool
	}{
		{
			name:            "http",
			approvalSurface: approvalpolicy.DecideApprovalRenderSurfaceHTTP,
			commandSurface:  commanddispatch.ProjectionDispatchSurfaceHTTP,
			want:            transportwriter.SurfaceHTTP,
			ok:              true,
		},
		{
			name:            "mcp",
			approvalSurface: approvalpolicy.DecideApprovalRenderSurfaceMCP,
			commandSurface:  commanddispatch.ProjectionDispatchSurfaceMCP,
			want:            transportwriter.SurfaceMCP,
			ok:              true,
		},
		{
			name:            "unsupported",
			approvalSurface: approvalpolicy.DecideApprovalRenderSurface("bogus"),
			commandSurface:  commanddispatch.ProjectionDispatchSurface("bogus"),
			ok:              false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			approvalResolved, approvalOK := approvalpolicy.ResolveDecideApprovalTransportSurface(tt.approvalSurface)
			commandResolved, commandOK := commanddispatch.ResolveCommandInvokeTransportSurface(tt.commandSurface)

			if approvalOK != tt.ok || commandOK != tt.ok {
				t.Fatalf("unexpected resolver presence: approval=%v command=%v want=%v", approvalOK, commandOK, tt.ok)
			}
			if !tt.ok {
				return
			}
			if approvalResolved != tt.want || commandResolved != tt.want {
				t.Fatalf("unexpected transport resolution: approval=%q command=%q want=%q", approvalResolved, commandResolved, tt.want)
			}
		})
	}
}

func TestUnsupportedSurfaceFallbackPrecedence_ApprovalAndCommand(t *testing.T) {
	t.Run("http_writer_precedes_mcp_writer", func(t *testing.T) {
		approvalHTTP, approvalMCP := false, false
		approvalpolicy.DispatchDecideApprovalResponseForSurface(nil, approvalpolicy.DecideApprovalRenderSurface("bogus"), approvalpolicy.DecideApprovalResponseDispatchWriter{
			WriteHTTPError: func(err *approvalpolicy.HTTPErrorContract) {
				if err == nil || err.Status != http.StatusInternalServerError {
					t.Fatalf("unexpected approval http error: %+v", err)
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

		commandHTTP, commandMCP := false, false
		commanddispatch.DispatchCommandInvokeProjection(&commanddispatch.CommandInvokeProjection{Surface: commanddispatch.ProjectionDispatchSurface("bogus")}, commanddispatch.CommandInvokeRenderDispatchWriter{
			WriteHTTPError: func(err *commanddispatch.HTTPErrorContract) {
				if err == nil || err.Status != http.StatusInternalServerError {
					t.Fatalf("unexpected command http error: %+v", err)
				}
				commandHTTP = true
			},
			WriteMCPError: func(err error) {
				commandMCP = err != nil
			},
		})
		if !commandHTTP || commandMCP {
			t.Fatalf("command fallback precedence mismatch: http=%v mcp=%v", commandHTTP, commandMCP)
		}
	})

	t.Run("mcp_writer_used_when_http_writer_absent", func(t *testing.T) {
		var approvalErr error
		approvalpolicy.DispatchDecideApprovalResponseForSurface(nil, approvalpolicy.DecideApprovalRenderSurface("bogus"), approvalpolicy.DecideApprovalResponseDispatchWriter{
			WriteMCPError: func(err error) {
				approvalErr = err
			},
		})
		if approvalErr == nil || !strings.Contains(approvalErr.Error(), "unsupported approval decide dispatch surface") {
			t.Fatalf("unexpected approval mcp fallback error: %v", approvalErr)
		}

		var commandErr error
		commanddispatch.DispatchCommandInvokeProjection(&commanddispatch.CommandInvokeProjection{Surface: commanddispatch.ProjectionDispatchSurface("bogus")}, commanddispatch.CommandInvokeRenderDispatchWriter{
			WriteMCPError: func(err error) {
				commandErr = err
			},
		})
		if commandErr == nil || !strings.Contains(commandErr.Error(), "unsupported command dispatch surface") {
			t.Fatalf("unexpected command mcp fallback error: %v", commandErr)
		}
	})
}
