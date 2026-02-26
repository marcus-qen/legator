package server

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/events"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

// handleProbeMessage processes incoming WebSocket messages from probes.
func (s *Server) handleProbeMessage(probeID string, env protocol.Envelope) {
	switch env.Type {
	case protocol.MsgHeartbeat:
		data, _ := json.Marshal(env.Payload)
		var hb protocol.HeartbeatPayload
		if err := json.Unmarshal(data, &hb); err != nil {
			s.logger.Warn("bad heartbeat payload", zap.String("probe", probeID), zap.Error(err))
			return
		}
		if err := s.fleetMgr.Heartbeat(probeID, &hb); err != nil {
			s.fleetMgr.Register(probeID, "", "", "")
			_ = s.fleetMgr.Heartbeat(probeID, &hb)
			s.emitAudit(audit.EventProbeRegistered, probeID, "system", "Auto-registered via heartbeat")
		}

		s.publishEvent(events.ProbeConnected, probeID, fmt.Sprintf("Probe %s heartbeat", probeID),
			map[string]string{"status": "online", "last_seen": time.Now().UTC().Format(time.RFC3339)})

	case protocol.MsgInventory:
		data, _ := json.Marshal(env.Payload)
		var inv protocol.InventoryPayload
		if err := json.Unmarshal(data, &inv); err != nil {
			s.logger.Warn("bad inventory payload", zap.String("probe", probeID), zap.Error(err))
			return
		}
		if err := s.fleetMgr.UpdateInventory(probeID, &inv); err != nil {
			s.logger.Warn("inventory update failed", zap.String("probe", probeID), zap.Error(err))
		} else {
			s.emitAudit(audit.EventInventoryUpdate, probeID, probeID, "Inventory updated")
		}

	case protocol.MsgCommandResult:
		data, _ := json.Marshal(env.Payload)
		var result protocol.CommandResultPayload
		if err := json.Unmarshal(data, &result); err != nil {
			s.logger.Warn("bad command result payload", zap.String("probe", probeID), zap.Error(err))
			return
		}
		s.logger.Info("command result received",
			zap.String("probe", probeID),
			zap.String("request_id", result.RequestID),
			zap.Int("exit_code", result.ExitCode),
		)
		s.recordAudit(audit.Event{
			Type:    audit.EventCommandResult,
			ProbeID: probeID,
			Actor:   probeID,
			Summary: "Command completed: " + result.RequestID,
			Detail:  map[string]any{"exit_code": result.ExitCode, "duration_ms": result.Duration},
		})
		if err := s.cmdTracker.Complete(result.RequestID, &result); err != nil {
			s.logger.Debug("no waiting caller for result", zap.String("request_id", result.RequestID))
		}
		evtType := events.CommandCompleted
		if result.ExitCode != 0 {
			evtType = events.CommandFailed
		}
		s.publishEvent(evtType, probeID, fmt.Sprintf("Command %s exit=%d", result.RequestID, result.ExitCode),
			map[string]any{"request_id": result.RequestID, "exit_code": result.ExitCode})

	case protocol.MsgOutputChunk:
		data, _ := json.Marshal(env.Payload)
		var chunk protocol.OutputChunkPayload
		if err := json.Unmarshal(data, &chunk); err != nil {
			s.logger.Warn("bad output chunk", zap.String("probe", probeID), zap.Error(err))
			return
		}
		s.hub.DispatchChunk(chunk)
		if chunk.Final {
			s.logger.Info("streaming command completed",
				zap.String("probe", probeID),
				zap.String("request_id", chunk.RequestID),
				zap.Int("exit_code", chunk.ExitCode),
			)
			_ = s.cmdTracker.Complete(chunk.RequestID, &protocol.CommandResultPayload{
				RequestID: chunk.RequestID,
				ExitCode:  chunk.ExitCode,
			})
		}

	default:
		s.logger.Debug("unhandled message type",
			zap.String("probe", probeID),
			zap.String("type", string(env.Type)),
		)
	}
}
