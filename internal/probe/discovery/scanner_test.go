package discovery

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// mockDialer records which addresses were dialed and returns a fake connection
// only for pre-configured hosts.
type mockDialer struct {
	open    map[string]string // addr → banner to send
	dialErr error
}

func (m *mockDialer) DialContext(_ context.Context, _, addr string) (net.Conn, error) {
	if m.dialErr != nil {
		return nil, m.dialErr
	}
	banner, ok := m.open[addr]
	if !ok {
		return nil, fmt.Errorf("connection refused")
	}
	// Return a fake connection that writes the banner then EOF.
	serverConn, clientConn := net.Pipe()
	go func() {
		defer serverConn.Close()
		serverConn.SetWriteDeadline(time.Now().Add(time.Second))
		fmt.Fprintf(serverConn, "%s\r\n", banner)
	}()
	return clientConn, nil
}

func TestNetworkScannerScanBasic(t *testing.T) {
	d := &mockDialer{
		open: map[string]string{
			"10.0.0.1:22": "SSH-2.0-OpenSSH_8.4p1 Ubuntu-4ubuntu0.4",
			"10.0.0.3:22": "SSH-2.0-OpenSSH_9.0",
		},
	}

	s := &NetworkScanner{
		ConnectTimeout: 200 * time.Millisecond,
		MaxConcurrency: 8,
		Ports:          []int{22},
		Dialer:         d,
	}

	hosts, err := s.Scan(context.Background(), "10.0.0.0/28")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	found := make(map[string]bool)
	for _, h := range hosts {
		found[h.IP] = true
		if h.Port != 22 {
			t.Errorf("expected port 22, got %d for %s", h.Port, h.IP)
		}
	}
	if !found["10.0.0.1"] {
		t.Error("expected 10.0.0.1 in results")
	}
	if !found["10.0.0.3"] {
		t.Error("expected 10.0.0.3 in results")
	}
	// Hosts not in the open map should NOT appear.
	if found["10.0.0.2"] {
		t.Error("10.0.0.2 should not appear (closed)")
	}
}

func TestNetworkScannerBannerGrab(t *testing.T) {
	d := &mockDialer{
		open: map[string]string{
			"192.168.1.5:22": "SSH-2.0-OpenSSH_8.4p1 Ubuntu-4ubuntu0.4",
		},
	}

	s := &NetworkScanner{
		ConnectTimeout: 200 * time.Millisecond,
		MaxConcurrency: 8,
		Ports:          []int{22},
		Dialer:         d,
	}

	hosts, err := s.Scan(context.Background(), "192.168.1.4/30")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	var target *SSHHost
	for i := range hosts {
		if hosts[i].IP == "192.168.1.5" {
			target = &hosts[i]
			break
		}
	}
	if target == nil {
		t.Fatal("expected 192.168.1.5 in results")
	}
	if !strings.Contains(target.SSHBanner, "SSH-2.0") {
		t.Errorf("expected SSH banner, got %q", target.SSHBanner)
	}
	if target.OSGuess == "" {
		t.Error("expected OS guess to be set")
	}
}

func TestNetworkScannerSorted(t *testing.T) {
	d := &mockDialer{
		open: map[string]string{
			"10.0.0.3:22": "SSH-2.0-OpenSSH_8.4",
			"10.0.0.1:22": "SSH-2.0-OpenSSH_8.4",
			"10.0.0.2:22": "SSH-2.0-OpenSSH_8.4",
		},
	}

	s := &NetworkScanner{
		ConnectTimeout: 200 * time.Millisecond,
		MaxConcurrency: 8,
		Ports:          []int{22},
		Dialer:         d,
	}

	hosts, err := s.Scan(context.Background(), "10.0.0.0/28")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	for i := 1; i < len(hosts); i++ {
		if hosts[i-1].IP > hosts[i].IP {
			t.Errorf("results not sorted: %s > %s", hosts[i-1].IP, hosts[i].IP)
		}
	}
}

func TestNetworkScannerCIDRTooLarge(t *testing.T) {
	s := NewNetworkScanner()
	_, err := s.Scan(context.Background(), "10.0.0.0/16")
	if err == nil {
		t.Fatal("expected error for /16 range")
	}
}

func TestNetworkScannerInvalidCIDR(t *testing.T) {
	s := NewNetworkScanner()
	_, err := s.Scan(context.Background(), "not-a-cidr")
	if err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

func TestNetworkScannerContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	s := NewNetworkScanner()
	// Should return without hanging.
	_, _ = s.Scan(ctx, "10.0.0.0/24")
}

func TestHostsFromCIDR(t *testing.T) {
	tests := []struct {
		cidr    string
		want    int
		wantErr bool
	}{
		{"192.168.1.0/24", 254, false},
		{"10.0.0.0/30", 2, false},
		{"10.0.0.0/32", 1, false},
		{"10.0.0.0/16", 0, true},
		{"not-valid", 0, true},
	}

	for _, tt := range tests {
		hosts, err := hostsFromCIDR(tt.cidr)
		if tt.wantErr {
			if err == nil {
				t.Errorf("cidr %s: expected error", tt.cidr)
			}
			continue
		}
		if err != nil {
			t.Errorf("cidr %s: unexpected error: %v", tt.cidr, err)
			continue
		}
		if len(hosts) != tt.want {
			t.Errorf("cidr %s: expected %d hosts, got %d", tt.cidr, tt.want, len(hosts))
		}
	}
}

func TestGuessOS(t *testing.T) {
	tests := []struct {
		banner string
		want   string
	}{
		{"SSH-2.0-OpenSSH_8.4p1 Ubuntu-4ubuntu0.4", "ubuntu"},
		{"SSH-2.0-OpenSSH_9.0 Debian", "debian"},
		{"SSH-2.0-OpenSSH_8.4", "linux"},
		{"SSH-2.0-Dropbear_2022.82", "linux"},
		{"SSH-2.0-libssh-0.9.6", "linux"},
		{"", ""},
	}
	for _, tt := range tests {
		got := guessOS(tt.banner)
		if got != tt.want {
			t.Errorf("banner %q: expected %q, got %q", tt.banner, tt.want, got)
		}
	}
}
