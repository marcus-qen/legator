package mcpserver

import (
	"context"
	"errors"
	"testing"

	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRenderRunCommandMCP_ParityWithLegacy(t *testing.T) {
	tests := []struct {
		name     string
		envelope *corecommanddispatch.CommandResultEnvelope
	}{
		{
			name:     "nil envelope",
			envelope: nil,
		},
		{
			name:     "dispatch error",
			envelope: &corecommanddispatch.CommandResultEnvelope{Err: errors.New("not connected")},
		},
		{
			name:     "timeout error",
			envelope: &corecommanddispatch.CommandResultEnvelope{Err: corecommanddispatch.ErrTimeout},
		},
		{
			name:     "context canceled",
			envelope: &corecommanddispatch.CommandResultEnvelope{Err: context.Canceled},
		},
		{
			name:     "nil result",
			envelope: &corecommanddispatch.CommandResultEnvelope{},
		},
		{
			name: "success",
			envelope: &corecommanddispatch.CommandResultEnvelope{Result: &protocol.CommandResultPayload{
				ExitCode: 0,
				Stdout:   " ok ",
			}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legacyResult, legacyMeta, legacyErr := legacyRenderRunCommandMCP(tc.envelope)
			adapterResult, adapterMeta, adapterErr := renderRunCommandMCP(&corecommanddispatch.CommandInvokeProjection{
				Surface:  corecommanddispatch.ProjectionDispatchSurfaceMCP,
				Envelope: tc.envelope,
			})

			if (legacyErr == nil) != (adapterErr == nil) {
				t.Fatalf("error presence mismatch: legacy=%v adapter=%v", legacyErr, adapterErr)
			}
			if legacyErr != nil && adapterErr != nil {
				if !errors.Is(adapterErr, legacyErr) && adapterErr.Error() != legacyErr.Error() {
					t.Fatalf("error mismatch: legacy=%v adapter=%v", legacyErr, adapterErr)
				}
			}

			if (legacyResult == nil) != (adapterResult == nil) {
				t.Fatalf("result presence mismatch: legacy=%#v adapter=%#v", legacyResult, adapterResult)
			}
			if legacyResult != nil {
				if gotLegacy, gotAdapter := toolText(t, legacyResult), toolText(t, adapterResult); gotLegacy != gotAdapter {
					t.Fatalf("tool text mismatch: legacy=%q adapter=%q", gotLegacy, gotAdapter)
				}
			}

			if legacyMeta != nil || adapterMeta != nil {
				t.Fatalf("expected nil meta for both renderers, legacy=%#v adapter=%#v", legacyMeta, adapterMeta)
			}
		})
	}
}

func legacyRenderRunCommandMCP(envelope *corecommanddispatch.CommandResultEnvelope) (*mcp.CallToolResult, any, error) {
	if envelope == nil {
		return nil, nil, errors.New("command failed: empty result")
	}
	if err := envelope.MCPError(); err != nil {
		return nil, nil, err
	}
	if envelope.Result == nil {
		return nil, nil, errors.New("command failed: empty result")
	}

	return textToolResult(corecommanddispatch.ResultText(envelope.Result)), nil, nil
}
