package commanddispatch

import (
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestResolveCommandDispatchProjectionSurface(t *testing.T) {
	tests := []struct {
		surface ProjectionDispatchSurface
		want    ProjectionDispatchSurface
		ok      bool
	}{
		{surface: ProjectionDispatchSurfaceHTTP, want: ProjectionDispatchSurfaceHTTP, ok: true},
		{surface: ProjectionDispatchSurfaceMCP, want: ProjectionDispatchSurfaceMCP, ok: true},
		{surface: ProjectionDispatchSurface("bogus"), ok: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.surface), func(t *testing.T) {
			got, ok := ResolveCommandDispatchProjectionSurface(tt.surface)
			if ok != tt.ok {
				t.Fatalf("unexpected resolve presence: got %v want %v", ok, tt.ok)
			}
			if tt.ok && got != tt.want {
				t.Fatalf("unexpected surface resolution: got %q want %q", got, tt.want)
			}
		})
	}
}

func TestResolveCommandReadProjectionSurface(t *testing.T) {
	tests := []struct {
		surface ProjectionDispatchSurface
		want    ProjectionDispatchSurface
		ok      bool
	}{
		{surface: ProjectionDispatchSurfaceHTTP, want: ProjectionDispatchSurfaceHTTP, ok: true},
		{surface: ProjectionDispatchSurfaceMCP, want: ProjectionDispatchSurfaceMCP, ok: true},
		{surface: ProjectionDispatchSurface("bogus"), ok: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.surface), func(t *testing.T) {
			got, ok := ResolveCommandReadProjectionSurface(tt.surface)
			if ok != tt.ok {
				t.Fatalf("unexpected resolve presence: got %v want %v", ok, tt.ok)
			}
			if tt.ok && got != tt.want {
				t.Fatalf("unexpected surface resolution: got %q want %q", got, tt.want)
			}
		})
	}
}

func TestResolveCommandInvokeProjectionDispatchSurface(t *testing.T) {
	tests := []struct {
		surface ProjectionDispatchSurface
		want    ProjectionDispatchSurface
		ok      bool
	}{
		{surface: ProjectionDispatchSurfaceHTTP, want: ProjectionDispatchSurfaceHTTP, ok: true},
		{surface: ProjectionDispatchSurfaceMCP, want: ProjectionDispatchSurfaceMCP, ok: true},
		{surface: ProjectionDispatchSurface("bogus"), ok: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.surface), func(t *testing.T) {
			got, ok := ResolveCommandInvokeProjectionDispatchSurface(tt.surface)
			if ok != tt.ok {
				t.Fatalf("unexpected resolve presence: got %v want %v", ok, tt.ok)
			}
			if tt.ok && got != tt.want {
				t.Fatalf("unexpected surface resolution: got %q want %q", got, tt.want)
			}
		})
	}
}

func TestResolveCommandInvokeTransportSurface(t *testing.T) {
	tests := []struct {
		surface ProjectionDispatchSurface
		want    transportwriter.Surface
		ok      bool
	}{
		{surface: ProjectionDispatchSurfaceHTTP, want: transportwriter.SurfaceHTTP, ok: true},
		{surface: ProjectionDispatchSurfaceMCP, want: transportwriter.SurfaceMCP, ok: true},
		{surface: ProjectionDispatchSurface("bogus"), ok: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.surface), func(t *testing.T) {
			got, ok := ResolveCommandInvokeTransportSurface(tt.surface)
			if ok != tt.ok {
				t.Fatalf("unexpected resolve presence: got %v want %v", ok, tt.ok)
			}
			if tt.ok && got != tt.want {
				t.Fatalf("unexpected transport surface resolution: got %q want %q", got, tt.want)
			}
		})
	}
}
