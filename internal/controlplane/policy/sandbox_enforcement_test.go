package policy

import (
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
)

func TestRequiresBreakglassConfirmation(t *testing.T) {
	tests := []struct {
		name     string
		category string
		lane     protocol.ExecutionClass
		want     bool
	}{
		{name: "mutation breakglass direct", category: "mutation", lane: protocol.ExecBreakglassDirect, want: true},
		{name: "mutation observe direct", category: "mutation", lane: protocol.ExecObserveDirect, want: true},
		{name: "mutation sandbox", category: "mutation", lane: protocol.ExecRemediateSandbox, want: false},
		{name: "observe direct", category: "observe", lane: protocol.ExecObserveDirect, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RequiresBreakglassConfirmation(tt.category, tt.lane); got != tt.want {
				t.Fatalf("RequiresBreakglassConfirmation(%q, %q) = %v, want %v", tt.category, tt.lane, got, tt.want)
			}
		})
	}
}

func TestResolveBreakglassConfirmation(t *testing.T) {
	confirmed := ResolveBreakglassConfirmation("incident_response", "")
	if !confirmed.Confirmed || confirmed.Method != BreakglassConfirmReasonField || confirmed.Reason != "incident_response" {
		t.Fatalf("unexpected reason confirmation: %+v", confirmed)
	}

	tokenOnly := ResolveBreakglassConfirmation("", "I UNDERSTAND")
	if !tokenOnly.Confirmed || tokenOnly.Method != BreakglassConfirmTokenField || tokenOnly.Reason != "I UNDERSTAND" {
		t.Fatalf("unexpected token confirmation: %+v", tokenOnly)
	}

	empty := ResolveBreakglassConfirmation("", "")
	if empty.Confirmed {
		t.Fatalf("expected empty confirmation to be false, got %+v", empty)
	}
}

func TestBreakglassReasonAllowed(t *testing.T) {
	if !BreakglassReasonAllowed("incident_response", []string{"incident_response", "service_outage"}) {
		t.Fatal("expected incident_response to be allowed")
	}
	if BreakglassReasonAllowed("security_emergency", []string{"incident_response"}) {
		t.Fatal("expected security_emergency to be rejected")
	}
	if !BreakglassReasonAllowed("anything", nil) {
		t.Fatal("expected empty allow-list to permit reason")
	}
}
