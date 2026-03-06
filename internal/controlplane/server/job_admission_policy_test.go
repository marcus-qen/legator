package server

import (
	"context"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/config"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
)

// TestEvaluateScheduledJobAdmission_AllowsObserveCommands verifies that safe
// observe-level commands are admitted (not denied) by the scheduler admission
// evaluator. This is the core regression test for GAP-2: before the fix, all
// commands were wrapped in /bin/sh which classified as an unknown mutation and
// was denied.
func TestEvaluateScheduledJobAdmission_AllowsObserveCommands(t *testing.T) {
	srv := newTestServerWithJobsConfig(t, config.JobsConfig{})

	cases := []string{"hostname", "uptime", "df -h"}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			job := jobs.Job{Command: cmd}
			decision := srv.evaluateScheduledJobAdmission(context.Background(), job, "probe-1")
			if decision.Outcome != jobs.AdmissionOutcomeAllow {
				t.Errorf("expected allow for %q, got %s (reason: %s)", cmd, decision.Outcome, decision.Reason)
			}
		})
	}
}

// TestEvaluateScheduledJobAdmission_DeniesOrQueuesDangerousCommands verifies
// that dangerous mutation-level commands are not unconditionally allowed.
func TestEvaluateScheduledJobAdmission_DeniesOrQueuesDangerousCommands(t *testing.T) {
	srv := newTestServerWithJobsConfig(t, config.JobsConfig{})

	cases := []string{"rm -rf /", "systemctl restart nginx"}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			job := jobs.Job{Command: cmd}
			decision := srv.evaluateScheduledJobAdmission(context.Background(), job, "probe-1")
			if decision.Outcome == jobs.AdmissionOutcomeAllow {
				t.Errorf("expected deny or queue for %q, but got allow (reason: %s)", cmd, decision.Reason)
			}
		})
	}
}
