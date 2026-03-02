package server

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func (s *Server) createAsyncCommandJob(probeID string, cmd protocol.CommandPayload) *jobs.AsyncJob {
	if s == nil || s.asyncJobsManager == nil {
		return nil
	}
	job, err := s.asyncJobsManager.CreateForCommand(probeID, cmd)
	if err != nil {
		s.logger.Warn("create async job failed",
			zap.String("probe_id", probeID),
			zap.String("request_id", cmd.RequestID),
			zap.Error(err),
		)
		return nil
	}
	s.recordAudit(audit.Event{
		Type:    audit.EventJobCreated,
		ProbeID: probeID,
		Actor:   "api",
		Summary: fmt.Sprintf("Async job created: %s", job.ID),
		Detail: map[string]any{
			"job_id":     job.ID,
			"request_id": job.RequestID,
			"command":    job.Command,
		},
	})
	return job
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
}
