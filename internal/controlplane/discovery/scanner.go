package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrCIDRRequired    = errors.New("cidr is required")
	ErrInvalidCIDR     = errors.New("invalid cidr")
	ErrCIDRTooLarge    = errors.New("cidr range too large; max /24")
	ErrIPv4Only        = errors.New("only IPv4 CIDR ranges are supported")
	ErrHostCapExceeded = errors.New("host count exceeds safety cap")
)

const (
	defaultHostTimeout = 300 * time.Millisecond
	maxHostTimeout     = 800 * time.Millisecond
)

var defaultPorts = []int{22, 443, 80}

// Scanner performs bounded TCP probing + reverse DNS.
type Scanner struct {
	Dialer         *net.Dialer
	Resolver       *net.Resolver
	Ports          []int
	MaxConcurrency int
}

func NewScanner() *Scanner {
	return &Scanner{
		Dialer:         &net.Dialer{},
		Resolver:       net.DefaultResolver,
		Ports:          append([]int(nil), defaultPorts...),
		MaxConcurrency: 64,
	}
}

// ValidateCIDR enforces IPv4 and /24 max range for MVP safety.
func ValidateCIDR(raw string) (*net.IPNet, error) {
	cidr := strings.TrimSpace(raw)
	if cidr == "" {
		return nil, ErrCIDRRequired
	}
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidCIDR, err)
	}
	if ip == nil || ip.To4() == nil || network.IP.To4() == nil {
		return nil, ErrIPv4Only
	}

	ones, bits := network.Mask.Size()
	if bits != 32 {
		return nil, ErrIPv4Only
	}
	if ones < MaxPrefixRange {
		return nil, ErrCIDRTooLarge
	}

	hosts, err := HostsFromCIDR(network)
	if err != nil {
		return nil, err
	}
	if len(hosts) > MaxHostsPerScan {
		return nil, ErrHostCapExceeded
	}

	return network, nil
}

// HostsFromCIDR expands an IPv4 range, skipping network/broadcast where applicable.
func HostsFromCIDR(network *net.IPNet) ([]string, error) {
	if network == nil || network.IP == nil || network.IP.To4() == nil {
		return nil, ErrIPv4Only
	}
	ones, bits := network.Mask.Size()
	if bits != 32 {
		return nil, ErrIPv4Only
	}
	if ones < MaxPrefixRange {
		return nil, ErrCIDRTooLarge
	}

	total := 1 << uint32(32-ones)
	if total > MaxHostsPerScan {
		return nil, ErrHostCapExceeded
	}

	base := network.IP.Mask(network.Mask).To4()
	hosts := make([]string, 0, total)

	for i := 0; i < total; i++ {
		if ones <= 30 && (i == 0 || i == total-1) {
			continue // network+broadcast for traditional subnets
		}
		ip := make(net.IP, len(base))
		copy(ip, base)
		ip[3] += byte(i)
		hosts = append(hosts, ip.String())
	}

	if len(hosts) > MaxHostsPerScan {
		return nil, ErrHostCapExceeded
	}

	return hosts, nil
}

// NormalizeHostTimeout bounds caller input into safe limits.
func NormalizeHostTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultHostTimeout
	}
	if timeout > maxHostTimeout {
		return maxHostTimeout
	}
	return timeout
}

func ConfidenceFromPorts(openPorts []int) string {
	has22 := false
	hasWeb := false
	for _, p := range openPorts {
		switch p {
		case 22:
			has22 = true
		case 80, 443:
			hasWeb = true
		}
	}
	if has22 {
		return ConfidenceHigh
	}
	if hasWeb {
		return ConfidenceMedium
	}
	return ConfidenceLow
}

// Scan probes all hosts in CIDR with bounded concurrency and per-host timeout.
func (s *Scanner) Scan(ctx context.Context, cidr string, hostTimeout time.Duration) ([]Candidate, error) {
	network, err := ValidateCIDR(cidr)
	if err != nil {
		return nil, err
	}
	hosts, err := HostsFromCIDR(network)
	if err != nil {
		return nil, err
	}

	timeout := NormalizeHostTimeout(hostTimeout)
	concurrency := s.MaxConcurrency
	if concurrency <= 0 {
		concurrency = 64
	}
	ports := s.Ports
	if len(ports) == 0 {
		ports = defaultPorts
	}

	sem := make(chan struct{}, concurrency)
	out := make(chan Candidate, len(hosts))
	var wg sync.WaitGroup

	for _, hostIP := range hosts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		wg.Add(1)
		go func(ip string) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			hostCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			candidate := s.scanHost(hostCtx, ip, ports, timeout)

			select {
			case out <- candidate:
			case <-ctx.Done():
			}
		}(hostIP)
	}

	wg.Wait()
	close(out)

	candidates := make([]Candidate, 0, len(hosts))
	for candidate := range out {
		candidates = append(candidates, candidate)
	}

	sort.Slice(candidates, func(i, j int) bool {
		left := net.ParseIP(candidates[i].IP).To4()
		right := net.ParseIP(candidates[j].IP).To4()
		if left == nil || right == nil {
			return candidates[i].IP < candidates[j].IP
		}
		return bytesCompare(left, right) < 0
	})

	return candidates, nil
}

func (s *Scanner) scanHost(ctx context.Context, ip string, ports []int, timeout time.Duration) Candidate {
	openPorts := s.probePorts(ctx, ip, ports, timeout)

	hostname := ""
	if s.Resolver != nil {
		if names, err := s.Resolver.LookupAddr(ctx, ip); err == nil && len(names) > 0 {
			hostname = strings.TrimSuffix(strings.TrimSpace(names[0]), ".")
		}
	}

	return Candidate{
		IP:         ip,
		Hostname:   hostname,
		OpenPorts:  openPorts,
		Confidence: ConfidenceFromPorts(openPorts),
	}
}

func (s *Scanner) probePorts(ctx context.Context, ip string, ports []int, timeout time.Duration) []int {
	dialer := s.Dialer
	if dialer == nil {
		dialer = &net.Dialer{}
	}

	results := make(chan int, len(ports))
	var wg sync.WaitGroup

	for _, port := range ports {
		p := port
		wg.Add(1)
		go func() {
			defer wg.Done()

			connCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			addr := net.JoinHostPort(ip, fmt.Sprintf("%d", p))
			conn, err := dialer.DialContext(connCtx, "tcp", addr)
			if err == nil {
				_ = conn.Close()
				select {
				case results <- p:
				case <-ctx.Done():
				}
			}
		}()
	}

	wg.Wait()
	close(results)

	openPorts := make([]int, 0, len(ports))
	for port := range results {
		openPorts = append(openPorts, port)
	}
	sort.Ints(openPorts)
	return openPorts
}

func bytesCompare(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}
