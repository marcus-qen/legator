package transportwriter_test

import (
	"reflect"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
	"github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestBuildUnsupportedSurfaceEnvelope_ParityWithLegacyCastPath(t *testing.T) {
	tests := []struct {
		name    string
		scope   transportwriter.UnsupportedSurfaceScope
		surface any
	}{
		{name: "string surface", scope: transportwriter.UnsupportedSurfaceScopeApprovalDecideDispatch, surface: "bogus"},
		{name: "approval typed surface", scope: transportwriter.UnsupportedSurfaceScopeApprovalDecideDispatch, surface: approvalpolicy.DecideApprovalRenderSurface("bogus")},
		{name: "command typed surface", scope: transportwriter.UnsupportedSurfaceScopeCommandDispatch, surface: commanddispatch.ProjectionDispatchSurface("bogus")},
		{name: "transport typed surface", scope: transportwriter.UnsupportedSurfaceScopeCommandInvoke, surface: transportwriter.Surface("bogus")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := transportwriter.UnsupportedSurfaceEnvelopeBuilderForScope(tt.scope)

			got := buildUnsupportedSurfaceEnvelopeWithTypedAdapter(t, builder, tt.surface)
			want := builder(legacyUnsupportedSurfaceStringCast(t, tt.surface))

			if !reflect.DeepEqual(got, want) {
				t.Fatalf("typed builder parity mismatch: got=%+v want=%+v", got, want)
			}
		})
	}
}

func TestUnsupportedSurfaceMessageForSurface_ParityWithLegacyCastPath(t *testing.T) {
	tests := []struct {
		name    string
		scope   transportwriter.UnsupportedSurfaceScope
		surface any
	}{
		{name: "string surface", scope: transportwriter.UnsupportedSurfaceScopeApprovalDecideDispatch, surface: "bogus"},
		{name: "approval typed surface", scope: transportwriter.UnsupportedSurfaceScopeApprovalDecideDispatch, surface: approvalpolicy.DecideApprovalRenderSurface("bogus")},
		{name: "command typed surface", scope: transportwriter.UnsupportedSurfaceScopeCommandDispatch, surface: commanddispatch.ProjectionDispatchSurface("bogus")},
		{name: "transport typed surface", scope: transportwriter.UnsupportedSurfaceScopeCommandInvoke, surface: transportwriter.Surface("bogus")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildUnsupportedSurfaceMessageWithTypedAdapter(t, tt.scope, tt.surface)
			want := transportwriter.UnsupportedSurfaceMessage(tt.scope, legacyUnsupportedSurfaceStringCast(t, tt.surface))
			if got != want {
				t.Fatalf("typed message parity mismatch: got=%q want=%q", got, want)
			}
		})
	}
}

func buildUnsupportedSurfaceEnvelopeWithTypedAdapter(t *testing.T, builder transportwriter.UnsupportedSurfaceEnvelopeBuilder, surface any) *transportwriter.ResponseEnvelope {
	t.Helper()
	switch v := surface.(type) {
	case string:
		return transportwriter.BuildUnsupportedSurfaceEnvelope(builder, v)
	case approvalpolicy.DecideApprovalRenderSurface:
		return transportwriter.BuildUnsupportedSurfaceEnvelope(builder, v)
	case commanddispatch.ProjectionDispatchSurface:
		return transportwriter.BuildUnsupportedSurfaceEnvelope(builder, v)
	case transportwriter.Surface:
		return transportwriter.BuildUnsupportedSurfaceEnvelope(builder, v)
	default:
		t.Fatalf("unsupported surface type %T", surface)
		return nil
	}
}

func buildUnsupportedSurfaceMessageWithTypedAdapter(t *testing.T, scope transportwriter.UnsupportedSurfaceScope, surface any) string {
	t.Helper()
	switch v := surface.(type) {
	case string:
		return transportwriter.UnsupportedSurfaceMessageForSurface(scope, v)
	case approvalpolicy.DecideApprovalRenderSurface:
		return transportwriter.UnsupportedSurfaceMessageForSurface(scope, v)
	case commanddispatch.ProjectionDispatchSurface:
		return transportwriter.UnsupportedSurfaceMessageForSurface(scope, v)
	case transportwriter.Surface:
		return transportwriter.UnsupportedSurfaceMessageForSurface(scope, v)
	default:
		t.Fatalf("unsupported surface type %T", surface)
		return ""
	}
}

func legacyUnsupportedSurfaceStringCast(t *testing.T, surface any) string {
	t.Helper()
	switch v := surface.(type) {
	case string:
		return v
	case approvalpolicy.DecideApprovalRenderSurface:
		return string(v)
	case commanddispatch.ProjectionDispatchSurface:
		return string(v)
	case transportwriter.Surface:
		return string(v)
	default:
		t.Fatalf("unsupported surface type %T", surface)
		return ""
	}
}
