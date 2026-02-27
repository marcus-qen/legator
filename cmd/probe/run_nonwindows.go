//go:build !windows

package main

import (
	"context"

	"github.com/marcus-qen/legator/internal/probe/agent"
	"go.uber.org/zap"
)

func runAgent(ctx context.Context, cfg *agent.Config, logger *zap.Logger) error {
	a := agent.New(cfg, logger)
	return a.Run(ctx)
}
