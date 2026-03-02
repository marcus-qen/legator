package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
)

type approvalDecisionRequest struct {
	DecidedBy string `json:"decided_by"`
	Reason    string `json:"reason,omitempty"`
}

type approvalDecisionResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// handleApproveAsyncJob handles POST /api/v1/jobs/{id}/approve.
// It resumes a waiting_approval job by transitioning it to running and dispatching.
// Returns 409 Conflict if the job has already been decided.
func (s *Server) handleApproveAsyncJob(w http.ResponseWriter, r *http.Request) {
	if s.asyncJobsManager == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "jobs_unavailable", "async jobs manager unavailable")
		return
	}
	jobID := r.PathValue("id")
	if jobID == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_id", "job id required")
		return
	}

	var req approvalDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeJSONError(w, http.StatusBadRequest, "invalid_body", "invalid request body")
		return
	}
	decidedBy := req.DecidedBy
	if decidedBy == "" {
		decidedBy = "api"
	}

	job, err := s.asyncJobsManager.ApproveJob(jobID)
	if err != nil {
		if jobs.IsAsyncJobConflict(err) || errors.Is(err, jobs.ErrInvalidAsyncJobTransition) {
			writeJSONError(w, http.StatusConflict, "already_decided", err.Error())
			return
		}
		if jobs.IsNotFound(err) {
			writeJSONError(w, http.StatusNotFound, "not_found", "async job not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "approve_failed", fmt.Sprintf("approve failed: %v", err))
		return
	}

	// Record stream marker for approval event
	s.appendCommandStreamMarker(job.RequestID, cmdtracker.StreamEventApproval, "job_approved", map[string]any{
		"job_id":      job.ID,
		"approved_by": decidedBy,
	})

	// Emit audit event
	s.emitAudit(audit.EventApprovalDecided, job.ProbeID, decidedBy, fmt.Sprintf("Async job %s approved", job.ID))

	// Dispatch the now-running job asynchronously
	capturedJob := *job
	go func() {
		if err := s.dispatchQueuedAsyncJob(context.Background(), capturedJob); err != nil {
			s.logger.Sugar().Warnf("async job dispatch after approval failed: job=%s err=%v", capturedJob.ID, err)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(approvalDecisionResponse{JobID: job.ID, Status: "approved"})
}

// handleRejectAsyncJob handles POST /api/v1/jobs/{id}/reject.
// It fails a waiting_approval job with the provided reason.
// Returns 409 Conflict if the job has already been decided.
func (s *Server) handleRejectAsyncJob(w http.ResponseWriter, r *http.Request) {
	if s.asyncJobsManager == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "jobs_unavailable", "async jobs manager unavailable")
		return
	}
	jobID := r.PathValue("id")
	if jobID == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_id", "job id required")
		return
	}

	var req approvalDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeJSONError(w, http.StatusBadRequest, "invalid_body", "invalid request body")
		return
	}
	decidedBy := req.DecidedBy
	if decidedBy == "" {
		decidedBy = "api"
	}
	reason := req.Reason
	if reason == "" {
		reason = "rejected by " + decidedBy
	}

	job, err := s.asyncJobsManager.RejectJob(jobID, reason)
	if err != nil {
		if jobs.IsAsyncJobConflict(err) || errors.Is(err, jobs.ErrInvalidAsyncJobTransition) {
			writeJSONError(w, http.StatusConflict, "already_decided", err.Error())
			return
		}
		if jobs.IsNotFound(err) {
			writeJSONError(w, http.StatusNotFound, "not_found", "async job not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "reject_failed", fmt.Sprintf("reject failed: %v", err))
		return
	}

	// Record stream marker for rejection event
	s.appendCommandStreamMarker(job.RequestID, cmdtracker.StreamEventApproval, "job_rejected", map[string]any{
		"job_id":      job.ID,
		"rejected_by": decidedBy,
		"reason":      reason,
	})

	// Emit audit event
	s.emitAudit(audit.EventApprovalDecided, job.ProbeID, decidedBy, fmt.Sprintf("Async job %s rejected: %s", job.ID, reason))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(approvalDecisionResponse{JobID: job.ID, Status: "rejected"})
}

// processTimedOutApprovalJobs finds expired waiting_approval jobs and applies the configured
// timeout behavior (cancel, reads_only, escalate). Returns the count of jobs processed.
func (s *Server) processTimedOutApprovalJobs(now time.Time) (int, error) {
	if s.asyncJobsManager == nil || s.jobsStore == nil {
		return 0, nil
	}
	behavior := s.cfg.Jobs.ApprovalTimeoutBehaviorOrDefault()
	extensionDur := s.cfg.Jobs.ApprovalTimeoutDuration()
	if extensionDur <= 0 {
		extensionDur = 900 * time.Second
	}

	expired, err := s.jobsStore.ListExpiredWaitingApprovalJobs(now, 100)
	if err != nil {
		return 0, fmt.Errorf("list expired approval jobs: %w", err)
	}

	processed := 0
	for _, job := range expired {
		switch behavior {
		case "escalate":
			// Extend the deadline and record an escalation audit event.
			newExpiry := now.Add(extensionDur)
			if extErr := s.jobsStore.ExtendApprovalExpiry(job.ID, newExpiry); extErr != nil {
				s.logger.Sugar().Warnf("extend approval expiry failed: job=%s err=%v", job.ID, extErr)
				continue
			}
			s.emitAudit(audit.EventApprovalRequest, job.ProbeID, "system",
				fmt.Sprintf("Async job %s approval escalated (deadline extended to %s)", job.ID, newExpiry.Format(time.RFC3339)))
			s.appendCommandStreamMarker(job.RequestID, cmdtracker.StreamEventApproval, "approval_timeout_escalated", map[string]any{
				"job_id":     job.ID,
				"new_expiry": newExpiry.Format(time.RFC3339),
			})

		case "reads_only":
			// Cancel jobs that exceeded their approval window under reads_only policy.
			if _, cancelErr := s.asyncJobsManager.CancelJob(job.ID, "approval timeout: reads_only policy enforced"); cancelErr != nil {
				s.logger.Sugar().Warnf("cancel timed-out job (reads_only) failed: job=%s err=%v", job.ID, cancelErr)
				continue
			}
			s.appendCommandStreamMarker(job.RequestID, cmdtracker.StreamEventApproval, "approval_timeout_reads_only", map[string]any{
				"job_id": job.ID,
			})

		default: // "cancel"
			if _, cancelErr := s.asyncJobsManager.CancelJob(job.ID, "approval timeout: auto-cancelled"); cancelErr != nil {
				s.logger.Sugar().Warnf("cancel timed-out job failed: job=%s err=%v", job.ID, cancelErr)
				continue
			}
			s.appendCommandStreamMarker(job.RequestID, cmdtracker.StreamEventApproval, "approval_timeout_cancelled", map[string]any{
				"job_id": job.ID,
			})
		}
		processed++
	}
	return processed, nil
}

// runApprovalTimeoutChecker periodically calls processTimedOutApprovalJobs
// in the background while the server is running.
func (s *Server) runApprovalTimeoutChecker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if _, err := s.processTimedOutApprovalJobs(now); err != nil {
				s.logger.Sugar().Warnf("approval timeout checker error: %v", err)
			}
		}
	}
}
