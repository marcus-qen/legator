package server

import (
	"strings"

	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func (s *Server) appendCommandStreamMarker(requestID string, kind cmdtracker.StreamEventKind, data string, meta map[string]any) {
	if s == nil || s.commandStreams == nil {
		return
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	if _, err := s.commandStreams.AppendMarker(requestID, kind, data, meta); err != nil {
		s.logger.Debug("append command stream marker failed", zap.String("request_id", requestID), zap.Error(err))
	}
}

func (s *Server) recordCommandOutputChunk(chunk protocol.OutputChunkPayload, dispatchLive bool) {
	if s == nil {
		return
	}
	if s.commandStreams != nil {
		if _, err := s.commandStreams.AppendOutputChunk(chunk); err != nil {
			s.logger.Debug("append command output chunk failed", zap.String("request_id", chunk.RequestID), zap.Int("seq", chunk.Seq), zap.Error(err))
		}
	}
	if dispatchLive && s.hub != nil {
		s.hub.DispatchChunk(chunk)
	}
}
