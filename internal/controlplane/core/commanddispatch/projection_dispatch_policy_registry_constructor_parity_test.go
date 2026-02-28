package commanddispatch

import (
	"fmt"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
	"github.com/marcus-qen/legator/internal/protocol"
)

func TestNewCommandDispatchErrorPolicyRegistry_ParityWithLegacyInlineSetup(t *testing.T) {
	policies := map[ProjectionDispatchSurface]commandDispatchErrorPolicy{
		ProjectionDispatchSurfaceHTTP: projectiondispatch.PolicyFunc[*CommandResultEnvelope, commandDispatchAdapterWriter](func(_ *CommandResultEnvelope, writer commandDispatchAdapterWriter) {
			if writer.handled != nil {
				*writer.handled = true
			}
			if writer.writer.WriteMCPText != nil {
				writer.writer.WriteMCPText("http")
			}
		}),
		ProjectionDispatchSurfaceMCP: projectiondispatch.PolicyFunc[*CommandResultEnvelope, commandDispatchAdapterWriter](func(_ *CommandResultEnvelope, writer commandDispatchAdapterWriter) {
			if writer.handled != nil {
				*writer.handled = true
			}
			if writer.writer.WriteMCPText != nil {
				writer.writer.WriteMCPText("mcp")
			}
		}),
	}

	newRegistry := newCommandDispatchErrorPolicyRegistry(policies)
	legacyRegistry := projectiondispatch.NewPolicyRegistry(policies)

	policies[ProjectionDispatchSurfaceHTTP] = projectiondispatch.PolicyFunc[*CommandResultEnvelope, commandDispatchAdapterWriter](func(_ *CommandResultEnvelope, writer commandDispatchAdapterWriter) {
		if writer.handled != nil {
			*writer.handled = true
		}
		if writer.writer.WriteMCPText != nil {
			writer.writer.WriteMCPText("mutated")
		}
	})

	tests := []ProjectionDispatchSurface{
		ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP,
		ProjectionDispatchSurface("bogus"),
	}

	for _, surface := range tests {
		t.Run(string(surface), func(t *testing.T) {
			newCapture := dispatchCommandDispatchErrorPolicyRegistryForSurface(newRegistry, surface)
			legacyCapture := dispatchCommandDispatchErrorPolicyRegistryForSurface(legacyRegistry, surface)
			if newCapture != legacyCapture {
				t.Fatalf("constructor parity mismatch for %q: new=%+v legacy=%+v", surface, newCapture, legacyCapture)
			}
		})
	}
}

func TestNewCommandReadPolicyRegistry_ParityWithLegacyInlineSetup(t *testing.T) {
	policies := map[ProjectionDispatchSurface]commandReadPolicy{
		ProjectionDispatchSurfaceHTTP: projectiondispatch.PolicyFunc[*protocol.CommandResultPayload, commandDispatchAdapterWriter](func(_ *protocol.CommandResultPayload, writer commandDispatchAdapterWriter) {
			if writer.writer.WriteMCPText != nil {
				writer.writer.WriteMCPText("http")
			}
		}),
		ProjectionDispatchSurfaceMCP: projectiondispatch.PolicyFunc[*protocol.CommandResultPayload, commandDispatchAdapterWriter](func(_ *protocol.CommandResultPayload, writer commandDispatchAdapterWriter) {
			if writer.writer.WriteMCPText != nil {
				writer.writer.WriteMCPText("mcp")
			}
		}),
	}

	newRegistry := newCommandReadPolicyRegistry(policies)
	legacyRegistry := projectiondispatch.NewPolicyRegistry(policies)

	policies[ProjectionDispatchSurfaceHTTP] = projectiondispatch.PolicyFunc[*protocol.CommandResultPayload, commandDispatchAdapterWriter](func(_ *protocol.CommandResultPayload, writer commandDispatchAdapterWriter) {
		if writer.writer.WriteMCPText != nil {
			writer.writer.WriteMCPText("mutated")
		}
	})

	tests := []ProjectionDispatchSurface{
		ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP,
		ProjectionDispatchSurface("bogus"),
	}

	for _, surface := range tests {
		t.Run(string(surface), func(t *testing.T) {
			newCapture := dispatchCommandReadPolicyRegistryForSurface(newRegistry, surface)
			legacyCapture := dispatchCommandReadPolicyRegistryForSurface(legacyRegistry, surface)
			if newCapture != legacyCapture {
				t.Fatalf("constructor parity mismatch for %q: new=%+v legacy=%+v", surface, newCapture, legacyCapture)
			}
		})
	}
}

func TestNewCommandInvokeProjectionDispatchPolicyRegistry_ParityWithLegacyInlineSetup(t *testing.T) {
	policies := map[ProjectionDispatchSurface]commandInvokeProjectionDispatchPolicy{
		ProjectionDispatchSurfaceHTTP: projectiondispatch.PolicyFunc[*CommandInvokeProjection, CommandInvokeRenderDispatchWriter](func(_ *CommandInvokeProjection, writer CommandInvokeRenderDispatchWriter) {
			if writer.WriteMCPText != nil {
				writer.WriteMCPText("http")
			}
		}),
		ProjectionDispatchSurfaceMCP: projectiondispatch.PolicyFunc[*CommandInvokeProjection, CommandInvokeRenderDispatchWriter](func(_ *CommandInvokeProjection, writer CommandInvokeRenderDispatchWriter) {
			if writer.WriteMCPText != nil {
				writer.WriteMCPText("mcp")
			}
		}),
	}

	newRegistry := newCommandInvokeProjectionDispatchPolicyRegistry(policies)
	legacyRegistry := projectiondispatch.NewPolicyRegistry(policies)

	policies[ProjectionDispatchSurfaceHTTP] = projectiondispatch.PolicyFunc[*CommandInvokeProjection, CommandInvokeRenderDispatchWriter](func(_ *CommandInvokeProjection, writer CommandInvokeRenderDispatchWriter) {
		if writer.WriteMCPText != nil {
			writer.WriteMCPText("mutated")
		}
	})

	tests := []ProjectionDispatchSurface{
		ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP,
		ProjectionDispatchSurface("bogus"),
	}

	for _, surface := range tests {
		t.Run(string(surface), func(t *testing.T) {
			newCapture := dispatchCommandInvokePolicyRegistryForSurface(newRegistry, surface)
			legacyCapture := dispatchCommandInvokePolicyRegistryForSurface(legacyRegistry, surface)
			if newCapture != legacyCapture {
				t.Fatalf("constructor parity mismatch for %q: new=%+v legacy=%+v", surface, newCapture, legacyCapture)
			}
		})
	}
}

type commandDispatchPolicyRegistryCapture struct {
	text    string
	handled bool
}

func dispatchCommandDispatchErrorPolicyRegistryForSurface(registry projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, commandDispatchErrorPolicy], surface ProjectionDispatchSurface) commandDispatchPolicyRegistryCapture {
	capture := commandDispatchPolicyRegistryCapture{}
	projectiondispatch.DispatchForSurface(
		registry,
		surface,
		(*CommandResultEnvelope)(nil),
		commandDispatchAdapterWriter{
			handled: &capture.handled,
			writer: CommandProjectionDispatchWriter{
				WriteMCPText: func(text string) {
					capture.text = text
				},
			},
		},
		func(surface ProjectionDispatchSurface, writer commandDispatchAdapterWriter) {
			if writer.handled != nil {
				*writer.handled = true
			}
			if writer.writer.WriteMCPText != nil {
				writer.writer.WriteMCPText(fmt.Sprintf("unsupported:%s", surface))
			}
		},
	)
	return capture
}

func dispatchCommandReadPolicyRegistryForSurface(registry projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, commandReadPolicy], surface ProjectionDispatchSurface) commandDispatchPolicyRegistryCapture {
	capture := commandDispatchPolicyRegistryCapture{}
	projectiondispatch.DispatchForSurface(
		registry,
		surface,
		(*protocol.CommandResultPayload)(nil),
		commandDispatchAdapterWriter{
			handled: nil,
			writer: CommandProjectionDispatchWriter{
				WriteMCPText: func(text string) {
					capture.text = text
				},
			},
		},
		func(surface ProjectionDispatchSurface, writer commandDispatchAdapterWriter) {
			if writer.writer.WriteMCPText != nil {
				writer.writer.WriteMCPText(fmt.Sprintf("unsupported:%s", surface))
			}
		},
	)
	return capture
}

func dispatchCommandInvokePolicyRegistryForSurface(registry projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, commandInvokeProjectionDispatchPolicy], surface ProjectionDispatchSurface) string {
	text := ""
	projectiondispatch.DispatchForSurface(
		registry,
		surface,
		(*CommandInvokeProjection)(nil),
		CommandInvokeRenderDispatchWriter{
			WriteMCPText: func(value string) {
				text = value
			},
		},
		func(surface ProjectionDispatchSurface, writer CommandInvokeRenderDispatchWriter) {
			if writer.WriteMCPText != nil {
				writer.WriteMCPText(fmt.Sprintf("unsupported:%s", surface))
			}
		},
	)
	return text
}
