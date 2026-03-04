package discovery

import (
	"fmt"
	"sync"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

// Sender is the subset of connection.Client needed by the Reporter.
type Sender interface {
	Send(msgType protocol.MessageType, payload any) error
}

// Reporter sends discovery_report messages to the control plane and
// tracks already-reported hosts to avoid duplicate reports.
type Reporter struct {
	mu    sync.Mutex
	known map[string]struct{} // ip:port → reported
}

// NewReporter creates a Reporter with an empty dedup set.
func NewReporter() *Reporter {
	return &Reporter{known: make(map[string]struct{})}
}

// Report filters out hosts already reported in this session and sends
// the remaining ones as a discovery_report WebSocket message.
// Returns the number of new hosts reported.
func (rp *Reporter) Report(sender Sender, probeID string, hosts []SSHHost) (int, error) {
	rp.mu.Lock()
	newHosts := make([]protocol.DiscoveredHost, 0, len(hosts))
	for _, h := range hosts {
		key := hostKey(h.IP, h.Port)
		if _, seen := rp.known[key]; seen {
			continue
		}
		rp.known[key] = struct{}{}
		newHosts = append(newHosts, protocol.DiscoveredHost{
			IP:          h.IP,
			Port:        h.Port,
			SSHBanner:   h.SSHBanner,
			OSGuess:     h.OSGuess,
			Fingerprint: h.Fingerprint,
		})
	}
	rp.mu.Unlock()

	if len(newHosts) == 0 {
		return 0, nil
	}

	payload := protocol.DiscoveryReportPayload{
		ProbeID:   probeID,
		Hosts:     newHosts,
		ScannedAt: time.Now().UTC(),
	}

	if err := sender.Send(protocol.MsgDiscoveryReport, payload); err != nil {
		return 0, fmt.Errorf("send discovery report: %w", err)
	}
	return len(newHosts), nil
}

// Reset clears the dedup set so all hosts will be re-reported on the next call.
func (rp *Reporter) Reset() {
	rp.mu.Lock()
	rp.known = make(map[string]struct{})
	rp.mu.Unlock()
}

// hostKey returns a dedup key for the given ip:port pair.
func hostKey(ip string, port int) string {
	return fmt.Sprintf("%s:%d", ip, port)
}
