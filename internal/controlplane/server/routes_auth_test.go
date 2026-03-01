package server

import (
	"net/http"
	"testing"
)

// TestRoutesAuthCoverage verifies that every /api/v1/ endpoint (except /api/v1/register,
// which is legitimately public for probe self-registration) requires authentication.
//
// This test acts as a regression guard: if a new endpoint is added without a
// withPermission wrapper, unauthenticated requests will succeed (200/other) rather
// than fail (401/403), and this test will catch it.
func TestRoutesAuthCoverage(t *testing.T) {
	srv := newAuthTestServer(t)

	// Endpoints that MUST require auth (all /api/v1/ except /api/v1/register).
	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/me"},
		// Probe fleet
		{http.MethodGet, "/api/v1/probes"},
		{http.MethodGet, "/api/v1/probes/some-probe"},
		{http.MethodGet, "/api/v1/probes/some-probe/health"},
		{http.MethodPost, "/api/v1/probes/some-probe/command"},
		{http.MethodPost, "/api/v1/probes/some-probe/rotate-key"},
		{http.MethodPost, "/api/v1/probes/some-probe/update"},
		{http.MethodPut, "/api/v1/probes/some-probe/tags"},
		{http.MethodPost, "/api/v1/probes/some-probe/apply-policy/some-policy"},
		{http.MethodPost, "/api/v1/probes/some-probe/task"},
		{http.MethodDelete, "/api/v1/probes/some-probe"},
		// Fleet summary/inventory/tags
		{http.MethodGet, "/api/v1/fleet/summary"},
		{http.MethodGet, "/api/v1/fleet/inventory"},
		{http.MethodGet, "/api/v1/fleet/tags"},
		{http.MethodGet, "/api/v1/fleet/by-tag/some-tag"},
		{http.MethodPost, "/api/v1/fleet/by-tag/some-tag/command"},
		{http.MethodPost, "/api/v1/fleet/cleanup"},
		// Federation
		{http.MethodGet, "/api/v1/federation/inventory"},
		{http.MethodGet, "/api/v1/federation/summary"},
		// Reliability
		{http.MethodGet, "/api/v1/reliability/scorecard"},
		{http.MethodGet, "/api/v1/reliability/drills"},
		{http.MethodGet, "/api/v1/reliability/drills/history"},
		{http.MethodPost, "/api/v1/reliability/drills/some-drill/run"},
		{http.MethodPost, "/api/v1/reliability/incidents"},
		{http.MethodGet, "/api/v1/reliability/incidents"},
		{http.MethodGet, "/api/v1/reliability/incidents/some-id"},
		{http.MethodPatch, "/api/v1/reliability/incidents/some-id"},
		{http.MethodPost, "/api/v1/reliability/incidents/some-id/timeline"},
		{http.MethodDelete, "/api/v1/reliability/incidents/some-id"},
		{http.MethodGet, "/api/v1/reliability/incidents/some-id/export"},
		// Approvals
		{http.MethodGet, "/api/v1/approvals"},
		{http.MethodGet, "/api/v1/approvals/some-id"},
		{http.MethodPost, "/api/v1/approvals/some-id/decide"},
		// Audit
		{http.MethodGet, "/api/v1/audit"},
		{http.MethodGet, "/api/v1/audit/export"},
		{http.MethodGet, "/api/v1/audit/export/csv"},
		{http.MethodDelete, "/api/v1/audit/purge"},
		// Events
		{http.MethodGet, "/api/v1/events"},
		// Commands
		{http.MethodGet, "/api/v1/commands/pending"},
		{http.MethodGet, "/api/v1/commands/some-request/stream"},
		// Policies
		{http.MethodGet, "/api/v1/policies"},
		{http.MethodGet, "/api/v1/policies/some-id"},
		{http.MethodPost, "/api/v1/policies"},
		{http.MethodDelete, "/api/v1/policies/some-id"},
		// Tokens
		{http.MethodGet, "/api/v1/tokens"},
		{http.MethodPost, "/api/v1/tokens"},
		// Users
		{http.MethodGet, "/api/v1/users"},
		{http.MethodPost, "/api/v1/users"},
		{http.MethodDelete, "/api/v1/users/some-id"},
		// Webhooks
		{http.MethodGet, "/api/v1/webhooks"},
		{http.MethodPost, "/api/v1/webhooks"},
		{http.MethodGet, "/api/v1/webhooks/some-id"},
		{http.MethodDelete, "/api/v1/webhooks/some-id"},
		{http.MethodPost, "/api/v1/webhooks/some-id/test"},
		{http.MethodGet, "/api/v1/webhooks/deliveries"},
		// Alerts
		{http.MethodGet, "/api/v1/alerts"},
		{http.MethodPost, "/api/v1/alerts"},
		{http.MethodGet, "/api/v1/alerts/active"},
		{http.MethodGet, "/api/v1/alerts/some-id"},
		{http.MethodPut, "/api/v1/alerts/some-id"},
		{http.MethodDelete, "/api/v1/alerts/some-id"},
		{http.MethodGet, "/api/v1/alerts/some-id/history"},
		// Metrics
		{http.MethodGet, "/api/v1/metrics"},
		// Jobs
		{http.MethodGet, "/api/v1/jobs"},
		{http.MethodGet, "/api/v1/jobs/runs"},
		{http.MethodPost, "/api/v1/jobs"},
		{http.MethodGet, "/api/v1/jobs/some-id"},
		{http.MethodPut, "/api/v1/jobs/some-id"},
		{http.MethodDelete, "/api/v1/jobs/some-id"},
		{http.MethodPost, "/api/v1/jobs/some-id/run"},
		{http.MethodPost, "/api/v1/jobs/some-id/cancel"},
		{http.MethodGet, "/api/v1/jobs/some-id/runs"},
		{http.MethodPost, "/api/v1/jobs/some-id/runs/some-run/cancel"},
		{http.MethodPost, "/api/v1/jobs/some-id/runs/some-run/retry"},
		{http.MethodPost, "/api/v1/jobs/some-id/enable"},
		{http.MethodPost, "/api/v1/jobs/some-id/disable"},
		// Auth keys
		{http.MethodGet, "/api/v1/auth/keys"},
		{http.MethodPost, "/api/v1/auth/keys"},
		{http.MethodDelete, "/api/v1/auth/keys/some-id"},
		// Discovery
		{http.MethodPost, "/api/v1/discovery/scan"},
		{http.MethodGet, "/api/v1/discovery/runs"},
		{http.MethodGet, "/api/v1/discovery/runs/some-id"},
		{http.MethodPost, "/api/v1/discovery/install-token"},
		// Model profiles
		{http.MethodGet, "/api/v1/model-profiles"},
		{http.MethodPost, "/api/v1/model-profiles"},
		{http.MethodPut, "/api/v1/model-profiles/some-id"},
		{http.MethodDelete, "/api/v1/model-profiles/some-id"},
		{http.MethodPost, "/api/v1/model-profiles/some-id/activate"},
		{http.MethodGet, "/api/v1/model-profiles/active"},
		{http.MethodGet, "/api/v1/model-usage"},
		// Cloud connectors
		{http.MethodGet, "/api/v1/cloud/connectors"},
		{http.MethodPost, "/api/v1/cloud/connectors"},
		{http.MethodPut, "/api/v1/cloud/connectors/some-id"},
		{http.MethodDelete, "/api/v1/cloud/connectors/some-id"},
		{http.MethodPost, "/api/v1/cloud/connectors/some-id/scan"},
		{http.MethodGet, "/api/v1/cloud/assets"},
		// Automation packs
		{http.MethodGet, "/api/v1/automation-packs"},
		{http.MethodPost, "/api/v1/automation-packs"},
		{http.MethodGet, "/api/v1/automation-packs/some-id"},
		{http.MethodPost, "/api/v1/automation-packs/dry-run"},
		{http.MethodPost, "/api/v1/automation-packs/some-id/executions"},
		{http.MethodGet, "/api/v1/automation-packs/executions/some-id"},
		{http.MethodGet, "/api/v1/automation-packs/executions/some-id/timeline"},
		{http.MethodGet, "/api/v1/automation-packs/executions/some-id/artifacts"},
		// Kubeflow
		{http.MethodGet, "/api/v1/kubeflow/status"},
		{http.MethodGet, "/api/v1/kubeflow/inventory"},
		{http.MethodGet, "/api/v1/kubeflow/runs/some-run/status"},
		{http.MethodPost, "/api/v1/kubeflow/actions/refresh"},
		{http.MethodPost, "/api/v1/kubeflow/runs/submit"},
		{http.MethodPost, "/api/v1/kubeflow/runs/some-run/cancel"},
		// Grafana
		{http.MethodGet, "/api/v1/grafana/status"},
		{http.MethodGet, "/api/v1/grafana/snapshot"},
		// Network devices
		{http.MethodGet, "/api/v1/network/devices"},
		{http.MethodPost, "/api/v1/network/devices"},
		{http.MethodGet, "/api/v1/network/devices/some-id"},
		{http.MethodPut, "/api/v1/network/devices/some-id"},
		{http.MethodDelete, "/api/v1/network/devices/some-id"},
		{http.MethodPost, "/api/v1/network/devices/some-id/test"},
		{http.MethodPost, "/api/v1/network/devices/some-id/inventory"},
		// Chat
		{http.MethodGet, "/api/v1/probes/some-probe/chat"},
		{http.MethodPost, "/api/v1/probes/some-probe/chat"},
		{http.MethodDelete, "/api/v1/probes/some-probe/chat"},
		{http.MethodGet, "/api/v1/fleet/chat"},
		{http.MethodPost, "/api/v1/fleet/chat"},
	}

	for _, ep := range endpoints {
		ep := ep // capture loop variable
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			// No auth token — expect 401 Unauthorized
			rr := makeRequest(t, srv, ep.method, ep.path, "" /* no token */, "")
			if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusForbidden {
				t.Errorf("%s %s: expected 401 or 403 without auth credentials, got %d (body: %s)",
					ep.method, ep.path, rr.Code, rr.Body.String())
			}
		})
	}
}

// TestPublicEndpointsAreAccessibleWithoutAuth verifies that legitimately public
// endpoints do NOT require authentication.
func TestPublicEndpointsAreAccessibleWithoutAuth(t *testing.T) {
	srv := newAuthTestServer(t)

	public := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/healthz"},
		{http.MethodGet, "/version"},
		// /api/v1/register is public (probe self-registration with token)
		// NOTE: POST /api/v1/register is excluded from auth coverage by design
	}

	for _, ep := range public {
		ep := ep
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			rr := makeRequest(t, srv, ep.method, ep.path, "", "")
			if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
				t.Errorf("%s %s: expected public endpoint to be accessible without auth, got %d",
					ep.method, ep.path, rr.Code)
			}
		})
	}
}
