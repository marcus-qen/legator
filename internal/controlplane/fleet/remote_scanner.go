package fleet

import (
	"context"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

const defaultRemoteScanInterval = 2 * time.Minute

// RemoteScanner periodically refreshes SSH inventory for remote probes.
type remoteScannerExecutor interface {
	CollectInventory(ctx context.Context, ps *ProbeState) (*protocol.InventoryPayload, error)
}

type RemoteScanner struct {
	fleet    Fleet
	executor remoteScannerExecutor
	interval time.Duration
	logger   *zap.Logger
}

func NewRemoteScanner(fleetMgr Fleet, executor remoteScannerExecutor, logger *zap.Logger, interval time.Duration) *RemoteScanner {
	if interval <= 0 {
		interval = defaultRemoteScanInterval
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RemoteScanner{
		fleet:    fleetMgr,
		executor: executor,
		interval: interval,
		logger:   logger,
	}
}

func (s *RemoteScanner) Run(ctx context.Context) {
	if s == nil || s.fleet == nil || s.executor == nil {
		return
	}

	s.scanOnce(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scanOnce(ctx)
		}
	}
}

func (s *RemoteScanner) ScanProbe(ctx context.Context, probeID string) {
	if s == nil || s.fleet == nil || s.executor == nil {
		return
	}
	ps, ok := s.fleet.Get(probeID)
	if !ok || normalizeProbeType(ps.Type) != ProbeTypeRemote {
		return
	}
	s.scanProbe(ctx, ps)
}

func (s *RemoteScanner) scanOnce(ctx context.Context) {
	for _, ps := range s.fleet.ListRemote() {
		s.scanProbe(ctx, ps)
	}
}

func (s *RemoteScanner) scanProbe(ctx context.Context, ps *ProbeState) {
	if ps == nil {
		return
	}

	inv, err := s.executor.CollectInventory(ctx, ps)
	if err != nil {
		s.logger.Warn("remote probe scan failed",
			zap.String("probe_id", ps.ID),
			zap.String("host", remoteProbeHost(ps)),
			zap.Error(err),
		)
		_ = s.fleet.SetStatus(ps.ID, "offline")
		return
	}

	if inv != nil {
		if err := s.fleet.UpdateInventory(ps.ID, inv); err != nil {
			s.logger.Warn("failed to persist remote inventory", zap.String("probe_id", ps.ID), zap.Error(err))
		}
	}
	if err := s.fleet.SetStatus(ps.ID, "online"); err != nil {
		s.logger.Warn("failed to set remote probe online", zap.String("probe_id", ps.ID), zap.Error(err))
	}
}

func remoteProbeHost(ps *ProbeState) string {
	if ps == nil || ps.Remote == nil {
		return ""
	}
	return strings.TrimSpace(ps.Remote.Host)
}
