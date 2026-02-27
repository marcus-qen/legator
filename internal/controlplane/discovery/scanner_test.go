package discovery

import (
	"errors"
	"testing"
	"time"
)

func TestValidateCIDR(t *testing.T) {
	tests := []struct {
		name    string
		cidr    string
		wantErr error
	}{
		{name: "valid /24", cidr: "192.168.1.0/24"},
		{name: "valid /32", cidr: "10.0.0.8/32"},
		{name: "empty", cidr: "", wantErr: ErrCIDRRequired},
		{name: "invalid", cidr: "not-a-cidr", wantErr: ErrInvalidCIDR},
		{name: "ipv6 unsupported", cidr: "fd00::/64", wantErr: ErrIPv4Only},
		{name: "too large", cidr: "10.0.0.0/23", wantErr: ErrCIDRTooLarge},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateCIDR(tc.cidr)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error %v, got nil", tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected error %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestHostsFromCIDRSkipsNetworkAndBroadcast(t *testing.T) {
	network, err := ValidateCIDR("192.168.10.0/30")
	if err != nil {
		t.Fatalf("validate cidr: %v", err)
	}
	hosts, err := HostsFromCIDR(network)
	if err != nil {
		t.Fatalf("hosts from cidr: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("expected 2 hosts for /30, got %d", len(hosts))
	}
	if hosts[0] != "192.168.10.1" || hosts[1] != "192.168.10.2" {
		t.Fatalf("unexpected hosts: %#v", hosts)
	}
}

func TestNormalizeHostTimeoutClamps(t *testing.T) {
	if got := NormalizeHostTimeout(0); got != defaultHostTimeout {
		t.Fatalf("expected default timeout %s, got %s", defaultHostTimeout, got)
	}
	if got := NormalizeHostTimeout(2 * time.Second); got != maxHostTimeout {
		t.Fatalf("expected max timeout %s, got %s", maxHostTimeout, got)
	}
	if got := NormalizeHostTimeout(500 * time.Millisecond); got != 500*time.Millisecond {
		t.Fatalf("expected passthrough timeout, got %s", got)
	}
}

func TestConfidenceFromPorts(t *testing.T) {
	if got := ConfidenceFromPorts([]int{22}); got != ConfidenceHigh {
		t.Fatalf("expected high, got %q", got)
	}
	if got := ConfidenceFromPorts([]int{80}); got != ConfidenceMedium {
		t.Fatalf("expected medium for 80, got %q", got)
	}
	if got := ConfidenceFromPorts([]int{443}); got != ConfidenceMedium {
		t.Fatalf("expected medium for 443, got %q", got)
	}
	if got := ConfidenceFromPorts([]int{}); got != ConfidenceLow {
		t.Fatalf("expected low, got %q", got)
	}
}
