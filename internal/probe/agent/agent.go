package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/marcus-qen/legator/internal/probe/connection"
	"github.com/marcus-qen/legator/internal/probe/executor"
	"github.com/marcus-qen/legator/internal/probe/inventory"
	"github.com/marcus-qen/legator/internal/probe/updater"
	"github.com/marcus-qen/legator/internal/protocol"
	"github.com/marcus-qen/legator/internal/shared/signing"
	"go.uber.org/zap"
)

const (
	inventoryInterval = 15 * time.Minute
)

// Agent is the main probe agent loop.
type Agent struct {
	config   *Config
	client   *connection.Client
	executor *executor.Executor
	verifier *signing.Signer
	updater  *updater.Updater
	logger   *zap.Logger
}

// New creates a new probe agent.
func New(cfg *Config, logger *zap.Logger) *Agent {
	wsURL := cfg.ServerURL
	// Convert http(s) to ws(s)
	if len(wsURL) > 4 && wsURL[:5] == "https" {
		wsURL = "wss" + wsURL[5:]
	} else if len(wsURL) > 3 && wsURL[:4] == "http" {
		wsURL = "ws" + wsURL[4:]
	}

	client := connection.NewClient(wsURL, cfg.ProbeID, cfg.APIKey, logger.Named("ws"))

	// Default policy: observe only
	policy := executor.Policy{
		Level: protocol.CapObserve,
	}
	exec := executor.New(policy, logger.Named("exec"))

	var verifier *signing.Signer
	if cfg.SigningKey != "" {
		key := signing.DeriveProbeKey([]byte(cfg.SigningKey), cfg.ProbeID)
		verifier = signing.NewSigner(key)
		logger.Info("command signature verification enabled")
	}

	return &Agent{
		config:   cfg,
		client:   client,
		executor: exec,
		verifier: verifier,
		updater:  updater.New(logger.Named("updater")),
		logger:   logger,
	}
}

// Run starts the agent loop. Blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	a.logger.Info("starting probe agent",
		zap.String("probe_id", a.config.ProbeID),
		zap.String("server", a.config.ServerURL),
	)

	// Start WebSocket connection in background
	go func() {
		if err := a.client.Run(ctx); err != nil && ctx.Err() == nil {
			a.logger.Error("connection loop exited", zap.Error(err))
		}
	}()

	// Send initial inventory after a short delay (let connection establish)
	go func() {
		time.Sleep(2 * time.Second)
		a.sendInventory()
	}()

	// Start inventory refresh loop
	go a.inventoryLoop(ctx)

	// Process incoming messages
	for {
		select {
		case <-ctx.Done():
			a.logger.Info("agent shutting down")
			return nil
		case env := <-a.client.Inbox():
			a.handleMessage(env)
		}
	}
}

func (a *Agent) handleMessage(env protocol.Envelope) {
	switch env.Type {
	case protocol.MsgCommand:
		data, _ := json.Marshal(env.Payload)
		var cmd protocol.CommandPayload
		if err := json.Unmarshal(data, &cmd); err != nil {
			a.logger.Warn("invalid command payload", zap.Error(err))
			return
		}

		if a.verifier != nil {
			if env.Signature == "" {
				a.logger.Warn("unsigned command rejected", zap.String("request_id", cmd.RequestID))
				_ = a.client.Send(protocol.MsgCommandResult, &protocol.CommandResultPayload{
					RequestID: cmd.RequestID, ExitCode: -1, Stderr: "command rejected: missing signature",
				})
				return
			}
			if err := a.verifier.Verify(env.ID, cmd, env.Signature); err != nil {
				a.logger.Warn("invalid command signature", zap.String("request_id", cmd.RequestID), zap.Error(err))
				_ = a.client.Send(protocol.MsgCommandResult, &protocol.CommandResultPayload{
					RequestID: cmd.RequestID, ExitCode: -1, Stderr: "command rejected: invalid signature",
				})
				return
			}
			a.logger.Debug("command signature verified", zap.String("request_id", cmd.RequestID))
		}

		a.logger.Info("executing command",
			zap.String("request_id", cmd.RequestID),
			zap.String("command", cmd.Command),
			zap.String("level", string(cmd.Level)),
			zap.Bool("stream", cmd.Stream),
		)

		if cmd.Stream {
			a.executor.ExecuteStream(context.Background(), &cmd, func(chunk protocol.OutputChunkPayload) {
				if err := a.client.Send(protocol.MsgOutputChunk, chunk); err != nil {
					a.logger.Error("failed to send output chunk", zap.Error(err))
				}
			})
		} else {
			result := a.executor.Execute(context.Background(), &cmd)
			if err := a.client.Send(protocol.MsgCommandResult, result); err != nil {
				a.logger.Error("failed to send result", zap.Error(err))
			}
		}

	case protocol.MsgPolicyUpdate:
		data, _ := json.Marshal(env.Payload)
		var policy protocol.PolicyUpdatePayload
		if err := json.Unmarshal(data, &policy); err != nil {
			a.logger.Warn("invalid policy payload", zap.Error(err))
			return
		}

		a.logger.Info("policy update received",
			zap.String("policy_id", policy.PolicyID),
			zap.String("level", string(policy.Level)),
		)

		// Update executor policy
		a.executor = executor.New(executor.Policy{
			Level:   policy.Level,
			Allowed: policy.Allowed,
			Blocked: policy.Blocked,
			Paths:   policy.Paths,
		}, a.logger.Named("exec"))

	case protocol.MsgUpdate:
		data, _ := json.Marshal(env.Payload)
		var upd protocol.UpdatePayload
		if err := json.Unmarshal(data, &upd); err != nil {
			a.logger.Warn("invalid update payload", zap.Error(err))
			return
		}
		a.logger.Info("update command received",
			zap.String("version", upd.Version),
			zap.String("url", upd.URL),
		)
		result := a.updater.Apply(upd.URL, upd.Checksum, upd.Version)
		_ = a.client.Send(protocol.MsgCommandResult, &protocol.CommandResultPayload{
			RequestID: env.ID,
			ExitCode:  boolToExit(!result.Success),
			Stdout:    result.Message,
		})
		if result.Success && upd.Restart {
			a.logger.Info("restarting probe after update")
			if err := a.updater.Restart(); err != nil {
				a.logger.Error("restart failed", zap.Error(err))
			}
		}

	case protocol.MsgPing:
		_ = a.client.Send(protocol.MsgPong, nil)

	default:
		a.logger.Debug("unhandled message", zap.String("type", string(env.Type)))
	}
}

func (a *Agent) sendInventory() {
	inv, err := inventory.Scan(a.config.ProbeID)
	if err != nil {
		a.logger.Error("inventory scan failed", zap.Error(err))
		return
	}

	if err := a.client.Send(protocol.MsgInventory, inv); err != nil {
		a.logger.Error("failed to send inventory", zap.Error(err))
		return
	}

	a.logger.Info("inventory sent",
		zap.String("hostname", inv.Hostname),
		zap.Int("cpus", inv.CPUs),
		zap.Int("services", len(inv.Services)),
		zap.Int("packages", len(inv.Packages)),
	)
}

func (a *Agent) inventoryLoop(ctx context.Context) {
	ticker := time.NewTicker(inventoryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sendInventory()
		}
	}
}

func boolToExit(failed bool) int {
	if failed {
		return 1
	}
	return 0
}
