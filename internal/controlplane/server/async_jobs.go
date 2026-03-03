package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/controlplane/events"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func (s *Server) createAsyncCommandJob(probeID string, cmd protocol.CommandPayload, workspaceID string) (*jobs.AsyncJob, error) {
	if s == nil || s.asyncJobsManager == nil {
		return nil, nil
	}
	job, err := s.asyncJobsManager.CreateJob(jobs.AsyncJob{
		WorkspaceID: strings.TrimSpace(workspaceID),
		ProbeID:     strings.TrimSpace(probeID),
		RequestID:   strings.TrimSpace(cmd.RequestID),
		Command:     strings.TrimSpace(cmd.Command),
		Args:        append([]string(nil), cmd.Args...),
		Level:       string(cmd.Level),
	})
	if err != nil {
		s.logger.Warn("create async job failed",
			zap.String("probe_id", probeID),
			zap.String("request_id", cmd.RequestID),
			zap.Error(err),
		)
		return nil, err
	}
	s.recordAudit(audit.Event{
		Type:        audit.EventJobCreated,
		WorkspaceID: strings.TrimSpace(job.WorkspaceID),
		ProbeID:     probeID,
		Actor:       "api",
		Summary: fmt.Sprintf("Async job created: %s", job.ID),
		Detail: map[string]any{
			"job_id":     job.ID,
			"request_id": job.RequestID,
			"command":    job.Command,
		},
	})
	s.appendCommandStreamMarker(job.RequestID, cmdtracker.StreamEventJob, "async_job_created", map[string]any{
		"job_id":   job.ID,
		"probe_id": probeID,
	})
	return job, nil
}

func (s *Server) dispatchQueuedAsyncJob(ctx context.Context, job jobs.AsyncJob) error {
	if s == nil || s.dispatchCore == nil {
		return fmt.Errorf("dispatch core unavailable")
	}
	ps, ok := s.fleetMgr.Get(job.ProbeID)
	if !ok {
		return fmt.Errorf("probe not found")
	}

	cmd := protocol.CommandPayload{
		RequestID: strings.TrimSpace(job.RequestID),
		Command:   strings.TrimSpace(job.Command),
		Args:      append([]string(nil), job.Args...),
		Level:     protocol.CapabilityLevel(strings.TrimSpace(job.Level)),
	}
	if cmd.RequestID == "" {
		cmd.RequestID = corecommanddispatch.NextCommandRequestID()
	}

	if strings.EqualFold(ps.Type, fleet.ProbeTypeRemote) {
		projection := s.invokeRemoteCommand(ctx, ps, cmd, false, false)
		if projection == nil || projection.Envelope == nil || !projection.Envelope.Dispatched {
			if projection != nil && projection.Envelope != nil && projection.Envelope.Err != nil {
				return projection.Envelope.Err
			}
			return fmt.Errorf("remote command dispatch failed")
		}
	} else {
		envelope := s.dispatchCore.DispatchWithPolicy(ctx, ps.ID, cmd, corecommanddispatch.DispatchOnlyPolicy(false))
		if envelope == nil || !envelope.Dispatched {
			if envelope != nil && envelope.Err != nil {
				return envelope.Err
			}
			return fmt.Errorf("command dispatch failed")
		}
	}

	s.recordAudit(audit.Event{
		Type:    audit.EventJobRunStarted,
		ProbeID: ps.ID,
		Actor:   "scheduler",
		Summary: fmt.Sprintf("Async job started: %s", job.ID),
		Detail:  map[string]any{"job_id": job.ID, "request_id": cmd.RequestID},
	})
	s.emitAudit(audit.EventCommandSent, ps.ID, "api", fmt.Sprintf("Command dispatched: %s", cmd.Command))
	s.publishEvent(events.CommandDispatched, ps.ID, fmt.Sprintf("Command dispatched: %s", cmd.Command),
		map[string]string{"request_id": cmd.RequestID, "command": cmd.Command})
	s.appendCommandStreamMarker(cmd.RequestID, cmdtracker.StreamEventDispatch, "command_dispatched", map[string]any{
		"probe_id": ps.ID,
		"job_id":   job.ID,
		"command":  cmd.Command,
	})

	return nil
}

func (s *Server) markAsyncJobRunning(jobID string) {
	if strings.TrimSpace(jobID) == "" || s == nil || s.asyncJobsManager == nil {
		return
	}
	job, err := s.asyncJobsManager.MarkRunning(jobID)
	if err != nil {
		if errors.Is(err, jobs.ErrInvalidAsyncJobTransition) || jobs.IsNotFound(err) {
			return
		}
		s.logger.Debug("mark async job running failed", zap.String("job_id", jobID), zap.Error(err))
		return
	}
	s.recordAudit(audit.Event{
		Type:    audit.EventJobRunStarted,
		ProbeID: job.ProbeID,
		Actor:   "api",
		Summary: fmt.Sprintf("Async job started: %s", job.ID),
		Detail:  map[string]any{"job_id": job.ID, "request_id": job.RequestID},
	})
}

func (s *Server) markAsyncJobRunningByRequestID(requestID string) {
	if strings.TrimSpace(requestID) == "" || s == nil || s.asyncJobsManager == nil {
		return
	}
	job, err := s.asyncJobsManager.MarkRunningByRequestID(requestID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, jobs.ErrInvalidAsyncJobTransition) {
			return
		}
		s.logger.Debug("mark async job running by request failed", zap.String("request_id", requestID), zap.Error(err))
		return
	}
	s.recordAudit(audit.Event{
		Type:    audit.EventJobRunStarted,
		ProbeID: job.ProbeID,
		Actor:   "approval",
		Summary: fmt.Sprintf("Async job started after approval: %s", job.ID),
		Detail:  map[string]any{"job_id": job.ID, "request_id": job.RequestID},
	})
	s.appendCommandStreamMarker(job.RequestID, cmdtracker.StreamEventApproval, "approval_granted", map[string]any{
		"job_id":   job.ID,
		"probe_id": job.ProbeID,
	})
}

func (s *Server) markAsyncJobWaitingApproval(jobID, approvalID string, expiresAt *time.Time, reason string) {
	if strings.TrimSpace(jobID) == "" || s == nil || s.asyncJobsManager == nil {
		return
	}
	job, err := s.asyncJobsManager.MarkWaitingApproval(jobID, approvalID, reason, expiresAt)
	if err != nil {
		if errors.Is(err, jobs.ErrInvalidAsyncJobTransition) || jobs.IsNotFound(err) {
			return
		}
		s.logger.Debug("mark async job waiting approval failed", zap.String("job_id", jobID), zap.Error(err))
		return
	}
	s.recordAudit(audit.Event{
		Type:    audit.EventApprovalRequest,
		ProbeID: job.ProbeID,
		Actor:   "api",
		Summary: fmt.Sprintf("Async job waiting approval: %s", job.ID),
		Detail:  map[string]any{"job_id": job.ID, "request_id": job.RequestID, "approval_id": approvalID},
	})
	s.appendCommandStreamMarker(job.RequestID, cmdtracker.StreamEventApproval, "pending_approval", map[string]any{
		"job_id":      job.ID,
		"probe_id":    job.ProbeID,
		"approval_id": approvalID,
		"expires_at":  job.ExpiresAt,
	})
}

func (s *Server) failAsyncJobByRequestID(requestID, reason, output string, exitCode *int) {
	if strings.TrimSpace(requestID) == "" || s == nil || s.asyncJobsManager == nil {
		return
	}
	job, err := s.asyncJobsManager.MarkFailedByRequestID(requestID, reason, output, exitCode)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, jobs.ErrInvalidAsyncJobTransition) {
			return
		}
		s.logger.Debug("fail async job failed", zap.String("request_id", requestID), zap.Error(err))
		return
	}
	s.recordAudit(audit.Event{
		Type:    audit.EventJobRunFailed,
		ProbeID: job.ProbeID,
		Actor:   "system",
		Summary: fmt.Sprintf("Async job failed: %s", job.ID),
		Detail:  map[string]any{"job_id": job.ID, "request_id": job.RequestID, "reason": reason},
	})
	s.appendCommandStreamMarker(job.RequestID, cmdtracker.StreamEventResult, "job_failed", map[string]any{
		"job_id":    job.ID,
		"probe_id":  job.ProbeID,
		"reason":    reason,
		"exit_code": exitCode,
	})
}

func (s *Server) completeAsyncJobByRequestID(requestID string, exitCode int, output string) {
	if strings.TrimSpace(requestID) == "" || s == nil || s.asyncJobsManager == nil {
		return
	}
	if exitCode != 0 {
		s.failAsyncJobByRequestID(requestID, fmt.Sprintf("command exited with code %d", exitCode), output, &exitCode)
		return
	}
	job, err := s.asyncJobsManager.MarkSucceededByRequestID(requestID, exitCode, output)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, jobs.ErrInvalidAsyncJobTransition) {
			return
		}
		s.logger.Debug("complete async job failed", zap.String("request_id", requestID), zap.Error(err))
		return
	}
	s.recordAudit(audit.Event{
		Type:    audit.EventJobRunSucceeded,
		ProbeID: job.ProbeID,
		Actor:   "system",
		Summary: fmt.Sprintf("Async job succeeded: %s", job.ID),
		Detail:  map[string]any{"job_id": job.ID, "request_id": job.RequestID},
	})
	s.appendCommandStreamMarker(job.RequestID, cmdtracker.StreamEventResult, "job_succeeded", map[string]any{
		"job_id":    job.ID,
		"probe_id":  job.ProbeID,
		"exit_code": exitCode,
	})
}
