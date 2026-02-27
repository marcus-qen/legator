//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/marcus-qen/legator/internal/probe/agent"
	"go.uber.org/zap"
	"golang.org/x/sys/windows/svc"
)

func runAgent(ctx context.Context, cfg *agent.Config, logger *zap.Logger) error {
	inService, err := svc.IsWindowsService()
	if err != nil {
		logger.Warn("could not determine windows service context", zap.Error(err))
		inService = false
	}

	if !inService {
		a := agent.New(cfg, logger)
		return a.Run(ctx)
	}

	h := &probeServiceHandler{
		ctx:    ctx,
		cfg:    cfg,
		logger: logger.Named("service"),
		runErr: make(chan error, 1),
	}

	if err := svc.Run("probe-agent", h); err != nil {
		return fmt.Errorf("run windows service: %w", err)
	}

	runErr := <-h.runErr
	if errors.Is(runErr, context.Canceled) {
		return nil
	}
	return runErr
}

type probeServiceHandler struct {
	ctx    context.Context
	cfg    *agent.Config
	logger *zap.Logger
	runErr chan error
}

func (h *probeServiceHandler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	runCtx, cancel := context.WithCancel(h.ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		a := agent.New(h.cfg, h.logger.Named("agent"))
		done <- a.Run(runCtx)
	}()

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case err := <-done:
			h.runErr <- err
			status <- svc.Status{State: svc.StopPending}
			return false, 0
		case r, ok := <-req:
			if !ok {
				cancel()
				h.runErr <- <-done
				return false, 0
			}
			switch r.Cmd {
			case svc.Interrogate:
				status <- r.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				h.runErr <- <-done
				return false, 0
			}
		}
	}
}
