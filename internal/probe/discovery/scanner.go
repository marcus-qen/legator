// Package discovery provides lateral-discovery capabilities for the probe:
// subnet scanning, candidate reporting, and remote probe installation.
package discovery

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultScanTimeout    = 500 * time.Millisecond
	defaultMaxConcurrency = 64
	sshPort               = 22
	bannerReadTimeout     = 2 * time.Second
)

// SSHHost describes a host that responded on an SSH port.
type SSHHost struct {
	IP          string `json:"ip"`
	Port        int    `json:"port"`
	SSHBanner   string `json:"ssh_banner,omitempty"`
	OSGuess     string `json:"os_guess,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"` // reserved for future key exchange
}

// NetworkScanner scans a subnet for SSH-reachable hosts and grabs their
// SSH banners for OS fingerprinting.
type NetworkScanner struct {
	// ConnectTimeout is the per-host TCP connect timeout.
	ConnectTimeout time.Duration
	// MaxConcurrency limits the number of hosts probed in parallel.
	MaxConcurrency int
	// Ports lists the TCP ports to try; defaults to [22].
	Ports []int
	// Dialer overrides the default net.Dialer (for testing).
	Dialer Dialer
}

// Dialer is a testable interface around net.Dialer.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// NewNetworkScanner returns a NetworkScanner with sensible defaults.
func NewNetworkScanner() *NetworkScanner {
	return &NetworkScanner{
		ConnectTimeout: defaultScanTimeout,
		MaxConcurrency: defaultMaxConcurrency,
		Ports:          []int{sshPort},
		Dialer:         &net.Dialer{},
	}
}

// Scan probes all hosts in cidr for open SSH ports.
// It returns the list of discovered hosts sorted by IP.
func (s *NetworkScanner) Scan(ctx context.Context, cidr string) ([]SSHHost, error) {
	hosts, err := hostsFromCIDR(cidr)
	if err != nil {
		return nil, err
	}

	timeout := s.ConnectTimeout
	if timeout <= 0 {
		timeout = defaultScanTimeout
	}
	concurrency := s.MaxConcurrency
	if concurrency <= 0 {
		concurrency = defaultMaxConcurrency
	}
	ports := s.Ports
	if len(ports) == 0 {
		ports = []int{sshPort}
	}
	dialer := s.Dialer
	if dialer == nil {
		dialer = &net.Dialer{}
	}

	sem := make(chan struct{}, concurrency)
	out := make(chan SSHHost, len(hosts))
	var wg sync.WaitGroup

	for _, ip := range hosts {
		if ctx.Err() != nil {
			break
		}
		for _, port := range ports {
			wg.Add(1)
			go func(ip string, port int) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-sem }()

				hostCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()

				host, ok := s.probe(hostCtx, ip, port, dialer)
				if !ok {
					return
				}
				select {
				case out <- host:
				case <-ctx.Done():
				}
			}(ip, port)
		}
	}

	wg.Wait()
	close(out)

	result := make([]SSHHost, 0)
	for h := range out {
		result = append(result, h)
	}

	sort.Slice(result, func(i, j int) bool {
		a := net.ParseIP(result[i].IP).To4()
		b := net.ParseIP(result[j].IP).To4()
		if a == nil || b == nil {
			return result[i].IP < result[j].IP
		}
		return compareIPv4(a, b) < 0
	})

	return result, nil
}

// probe attempts a TCP connection on ip:port and, on success, reads the SSH
// banner to determine OS family.
func (s *NetworkScanner) probe(ctx context.Context, ip string, port int, dialer Dialer) (SSHHost, bool) {
	addr := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return SSHHost{}, false
	}
	defer conn.Close()

	host := SSHHost{IP: ip, Port: port}

	// Grab the SSH identification string (e.g. "SSH-2.0-OpenSSH_8.4\r\n").
	_ = conn.SetReadDeadline(time.Now().Add(bannerReadTimeout))
	scanner := bufio.NewScanner(conn)
	if scanner.Scan() {
		banner := strings.TrimRight(scanner.Text(), "\r\n ")
		host.SSHBanner = banner
		host.OSGuess = guessOS(banner)
	}

	return host, true
}

// guessOS extracts an OS hint from the SSH banner string.
func guessOS(banner string) string {
	lower := strings.ToLower(banner)
	switch {
	case strings.Contains(lower, "ubuntu"):
		return "ubuntu"
	case strings.Contains(lower, "debian"):
		return "debian"
	case strings.Contains(lower, "centos"):
		return "centos"
	case strings.Contains(lower, "fedora"):
		return "fedora"
	case strings.Contains(lower, "alpine"):
		return "alpine"
	case strings.Contains(lower, "windows"):
		return "windows"
	case strings.Contains(lower, "freebsd"):
		return "freebsd"
	case strings.Contains(lower, "openbsd"):
		return "openbsd"
	case strings.Contains(lower, "openssh"):
		return "linux" // most common
	default:
		if strings.HasPrefix(banner, "SSH-") {
			return "linux"
		}
		return ""
	}
}

// hostsFromCIDR expands a CIDR notation to a list of individual host IPs,
// skipping network and broadcast addresses for subnets ≤ /30.
func hostsFromCIDR(cidr string) ([]string, error) {
	cidr = strings.TrimSpace(cidr)
	if cidr == "" {
		return nil, fmt.Errorf("cidr is required")
	}
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid cidr: %w", err)
	}
	if ip == nil || ip.To4() == nil {
		return nil, fmt.Errorf("only IPv4 CIDR ranges are supported")
	}

	ones, bits := network.Mask.Size()
	if bits != 32 {
		return nil, fmt.Errorf("only IPv4 CIDR ranges are supported")
	}
	if ones < 24 {
		return nil, fmt.Errorf("CIDR range too large; max /24 (got /%d)", ones)
	}

	total := 1 << uint(32-ones)
	if total > 256 {
		return nil, fmt.Errorf("host count %d exceeds safety cap 256", total)
	}

	base := network.IP.Mask(network.Mask).To4()
	hosts := make([]string, 0, total)
	for i := 0; i < total; i++ {
		if ones <= 30 && (i == 0 || i == total-1) {
			continue // skip network/broadcast
		}
		cur := make(net.IP, 4)
		copy(cur, base)
		cur[3] += byte(i)
		hosts = append(hosts, cur.String())
	}
	return hosts, nil
}

func compareIPv4(a, b net.IP) int {
	for i := 0; i < 4; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}
