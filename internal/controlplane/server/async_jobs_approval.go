package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
	"github.com/marcus-qen/legator/internal/protocol"
)

// approvalDecisionRequest defines payload for explicit async job approval/rejection.
type approvalDecisionRequest struct {
	DecidedBy string `json:"decided_by,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// approvalDecisionResponse defines response payload for async approval endpoints.
type approvalDecisionResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// handleApproveAsyncJob handles POST /api/v1/jobs/{id}/approve.
// It resumes the SAME waiting_approval async job (no replacement job).
func (s *Server) handleApproveAsyncJob(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermApprovalWrite) {
		return
	}
	if s.asyncJobsManager == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "jobs_unavailable", "async jobs manager unavailable")
		return
	}

	jobID := strings.TrimSpace(r.PathValue("id"))
	if jobID == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_id", "job id required")
		return
	}

	var req approvalDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, context.Canceled) && err.Error() != "EOF" {
		writeJSONError(w, http.StatusBadRequest, "invalid_body", "invalid request body")
		return
	}
	decidedBy := strings.TrimSpace(req.DecidedBy)
	if decidedBy == "" {
		decidedBy = "api"
	}

	job, err := s.asyncJobsManager.ApproveJob(jobID)
	if err != nil {
		switch {
		case jobs.IsAsyncJobConflict(err) || errors.Is(err, jobs.ErrInvalidAsyncJobTransition):
			writeJSONError(w, http.StatusConflict, "already_decided", err.Error())
			return
		case jobs.IsNotFound(err):
			writeJSONError(w, http.StatusNotFound, "not_found", "async job not found")
			return
		default:
			writeJSONError(w, http.StatusInternalServerError, "approve_failed", fmt.Sprintf("approve failed: %v", err))
			return
		}
	}

	s.syncApprovalDecision(job.ApprovalID, approval.DecisionApproved, decidedBy)

	s.appendCommandStreamMarker(job.RequestID, cmdtracker.StreamEventApproval, "job_approved", map[string]any{
		"job_id":      job.ID,
		"approval_id": job.ApprovalID,
		"approved_by": decidedBy,
	})
	s.recordAudit(audit.Event{
		Type:    audit.EventApprovalDecided,
		ProbeID: job.ProbeID,
		Actor:   decidedBy,
		Summary: fmt.Sprintf("Async job %s approved", job.ID),
		Detail: map[string]any{
			"job_id":      job.ID,
			"request_id":  job.RequestID,
			"approval_id": job.ApprovalID,
			"decision":    "approved",
			"decided_by":  decidedBy,
		},
	})

	// Dispatch the same resumed job.
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
// It marks a waiting_approval async job as failed without replacing the job.
func (s *Server) handleRejectAsyncJob(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermApprovalWrite) {
		return
	}
	if s.asyncJobsManager == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "jobs_unavailable", "async jobs manager unavailable")
		return
	}

	jobID := strings.TrimSpace(r.PathValue("id"))
	if jobID == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_id", "job id required")
		return
	}

	var req approvalDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeJSONError(w, http.StatusBadRequest, "invalid_body", "invalid request body")
		return
	}
	decidedBy := strings.TrimSpace(req.DecidedBy)
	if decidedBy == "" {
		decidedBy = "api"
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "rejected by " + decidedBy
	}

	job, err := s.asyncJobsManager.RejectJob(jobID, reason)
	if err != nil {
		switch {
		case jobs.IsAsyncJobConflict(err) || errors.Is(err, jobs.ErrInvalidAsyncJobTransition):
			writeJSONError(w, http.StatusConflict, "already_decided", err.Error())
			return
		case jobs.IsNotFound(err):
			writeJSONError(w, http.StatusNotFound, "not_found", "async job not found")
			return
		default:
			writeJSONError(w, http.StatusInternalServerError, "reject_failed", fmt.Sprintf("reject failed: %v", err))
			return
		}
	}

	s.syncApprovalDecision(job.ApprovalID, approval.DecisionDenied, decidedBy)

	s.appendCommandStreamMarker(job.RequestID, cmdtracker.StreamEventApproval, "job_rejected", map[string]any{
		"job_id":      job.ID,
		"approval_id": job.ApprovalID,
		"rejected_by": decidedBy,
		"reason":      reason,
	})
	s.recordAudit(audit.Event{
		Type:    audit.EventApprovalDecided,
		ProbeID: job.ProbeID,
		Actor:   decidedBy,
		Summary: fmt.Sprintf("Async job %s rejected: %s", job.ID, reason),
		Detail: map[string]any{
			"job_id":      job.ID,
			"request_id":  job.RequestID,
			"approval_id": job.ApprovalID,
			"decision":    "rejected",
			"decided_by":  decidedBy,
			"reason":      reason,
		},
	})

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
			newExpiry := now.Add(extensionDur)
			if extErr := s.jobsStore.ExtendApprovalExpiry(job.ID, newExpiry); extErr != nil {
				s.logger.Sugar().Warnf("extend approval expiry failed: job=%s err=%v", job.ID, extErr)
				continue
			}
			s.recordApprovalTimeoutAudit(job, behavior, "extended", "approval timeout escalated", map[string]any{
				"new_expiry": newExpiry.Format(time.RFC3339),
			})
			s.appendCommandStreamMarker(job.RequestID, cmdtracker.StreamEventApproval, "approval_timeout_escalated", map[string]any{
				"job_id":      job.ID,
				"approval_id": job.ApprovalID,
				"new_expiry":  newExpiry.Format(time.RFC3339),
			})
			processed++

		case "reads_only":
			if isReadOnlyAsyncJobLevel(job.Level) {
				resumed, approveErr := s.asyncJobsManager.ApproveJob(job.ID)
				if approveErr != nil {
					s.logger.Sugar().Warnf("reads_only timeout auto-approve failed: job=%s err=%v", job.ID, approveErr)
					continue
				}
				s.syncApprovalDecision(job.ApprovalID, approval.DecisionApproved, "system")
				s.recordApprovalTimeoutAudit(*resumed, behavior, "auto_approved", "reads_only auto-approved on timeout", nil)
				s.appendCommandStreamMarker(resumed.RequestID, cmdtracker.StreamEventApproval, "approval_timeout_reads_only_resumed", map[string]any{
					"job_id":      resumed.ID,
					"approval_id": resumed.ApprovalID,
				})

				capturedJob := *resumed
				go func() {
					if dispatchErr := s.dispatchQueuedAsyncJob(context.Background(), capturedJob); dispatchErr != nil {
						s.logger.Sugar().Warnf("reads_only timeout dispatch failed: job=%s err=%v", capturedJob.ID, dispatchErr)
					}
				}()
				processed++
				continue
			}

			reason := "approval timeout: reads_only policy blocked mutating command"
			if _, cancelErr := s.asyncJobsManager.CancelJob(job.ID, reason); cancelErr != nil {
				s.logger.Sugar().Warnf("cancel timed-out job (reads_only) failed: job=%s err=%v", job.ID, cancelErr)
				continue
			}
			s.syncApprovalDecision(job.ApprovalID, approval.DecisionDenied, "system")
			s.recordApprovalTimeoutAudit(job, behavior, "cancelled", reason, nil)
			s.appendCommandStreamMarker(job.RequestID, cmdtracker.StreamEventApproval, "approval_timeout_reads_only_cancelled", map[string]any{
				"job_id":      job.ID,
				"approval_id": job.ApprovalID,
			})
			processed++

		default: // cancel
			reason := "approval timeout: auto-cancelled"
			if _, cancelErr := s.asyncJobsManager.CancelJob(job.ID, reason); cancelErr != nil {
				s.logger.Sugar().Warnf("cancel timed-out job failed: job=%s err=%v", job.ID, cancelErr)
				continue
			}
			s.syncApprovalDecision(job.ApprovalID, approval.DecisionDenied, "system")
			s.recordApprovalTimeoutAudit(job, behavior, "cancelled", reason, nil)
			s.appendCommandStreamMarker(job.RequestID, cmdtracker.StreamEventApproval, "approval_timeout_cancelled", map[string]any{
				"job_id":      job.ID,
				"approval_id": job.ApprovalID,
			})
			processed++
		}
	}

	return processed, nil
}

// runApprovalTimeoutChecker periodically calls processTimedOutApprovalJobs
// in the background while the server is running.
func (s *Server) runApprovalTimeoutChecker(ctx context.Context) {
	checkInterval := 30 * time.Second
	ticker := time.NewTicker(checkInterval)
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

func (s *Server) recordApprovalTimeoutAudit(job jobs.AsyncJob, behavior, action, reason string, extra map[string]any) {
	detail := map[string]any{
		"job_id":           job.ID,
		"request_id":       job.RequestID,
		"approval_id":      job.ApprovalID,
		"timeout_behavior": behavior,
		"timeout_action":   action,
		"reason":           reason,
	}
	for k, v := range extra {
		detail[k] = v
	}
	s.recordAudit(audit.Event{
		Type:    audit.EventApprovalDecided,
		ProbeID: job.ProbeID,
		Actor:   "system",
		Summary: fmt.Sprintf("Async approval timeout handled (%s): %s", behavior, job.ID),
		Detail:  detail,
	})
}

func (s *Server) syncApprovalDecision(approvalID string, decision approval.Decision, decidedBy string) {
	approvalID = strings.TrimSpace(approvalID)
	decidedBy = strings.TrimSpace(decidedBy)
	if s == nil || s.approvalQueue == nil || approvalID == "" {
		return
	}
	if decidedBy == "" {
		decidedBy = "system"
	}
	if _, ok := s.approvalQueue.Get(approvalID); !ok {
		return
	}
	if _, err := s.approvalQueue.Decide(approvalID, decision, decidedBy); err != nil {
		if strings.Contains(err.Error(), "already decided") || strings.Contains(err.Error(), "expired at") || strings.Contains(err.Error(), "not found") {
			return
		}
		s.logger.Sugar().Debugf("sync approval decision failed: approval=%s decision=%s err=%v", approvalID, decision, err)
	}
}

func isReadOnlyAsyncJobLevel(level string) bool {
	switch protocol.CapabilityLevel(strings.TrimSpace(level)) {
	case protocol.CapObserve, protocol.CapDiagnose:
		return true
	default:
		return false
	}
}
