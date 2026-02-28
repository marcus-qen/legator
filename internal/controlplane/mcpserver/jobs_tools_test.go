package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestListJobsToolParityWithHTTP(t *testing.T) {
	srv, _, _, jobsStore := newTestMCPServer(t)

	jobA, err := jobsStore.CreateJob(jobs.Job{Name: "job-a", Command: "echo a", Schedule: "1m", Target: jobs.Target{Kind: jobs.TargetKindProbe, Value: "probe-a"}, Enabled: true})
	if err != nil {
		t.Fatalf("create job-a: %v", err)
	}
	_, err = jobsStore.CreateJob(jobs.Job{Name: "job-b", Command: "echo b", Schedule: "5m", Target: jobs.Target{Kind: jobs.TargetKindProbe, Value: "probe-b"}, Enabled: true})
	if err != nil {
		t.Fatalf("create job-b: %v", err)
	}

	session := connectClient(t, srv)
	mcpResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "legator_list_jobs"})
	if err != nil {
		t.Fatalf("call legator_list_jobs: %v", err)
	}
	var mcpJobs []jobs.Job
	decodeToolJSON(t, mcpResult, &mcpJobs)
	if len(mcpJobs) != 2 {
		t.Fatalf("expected 2 jobs from MCP, got %d", len(mcpJobs))
	}

	handler := jobs.NewHandler(jobsStore, nil)
	rr := httptest.NewRecorder()
	handler.HandleListJobs(rr, httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var httpJobs []jobs.Job
	if err := json.Unmarshal(rr.Body.Bytes(), &httpJobs); err != nil {
		t.Fatalf("decode http jobs: %v", err)
	}
	if len(httpJobs) != len(mcpJobs) {
		t.Fatalf("job count mismatch: mcp=%d http=%d", len(mcpJobs), len(httpJobs))
	}
	if mcpJobs[0].ID != httpJobs[0].ID || mcpJobs[1].ID != httpJobs[1].ID {
		t.Fatalf("expected MCP and HTTP ordering/ids parity, mcp=%s,%s http=%s,%s", mcpJobs[0].ID, mcpJobs[1].ID, httpJobs[0].ID, httpJobs[1].ID)
	}
	if jobA.ID == "" {
		t.Fatal("expected non-empty job id")
	}
}

func TestListJobRunsToolParityWithHTTP(t *testing.T) {
	srv, _, _, jobsStore := newTestMCPServer(t)

	job, err := jobsStore.CreateJob(jobs.Job{Name: "runs", Command: "echo run", Schedule: "1m", Target: jobs.Target{Kind: jobs.TargetKindProbe, Value: "probe-1"}, Enabled: true})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	base := time.Now().UTC().Add(-2 * time.Minute)
	runSuccess, err := jobsStore.RecordRunStart(jobs.JobRun{JobID: job.ID, ProbeID: "probe-1", RequestID: "req-success", StartedAt: base})
	if err != nil {
		t.Fatalf("record success run: %v", err)
	}
	if err := jobsStore.CompleteRun(runSuccess.ID, jobs.RunStatusSuccess, intPtrMCP(0), "ok"); err != nil {
		t.Fatalf("complete success run: %v", err)
	}
	runFailed, err := jobsStore.RecordRunStart(jobs.JobRun{JobID: job.ID, ProbeID: "probe-1", RequestID: "req-failed", StartedAt: base.Add(time.Minute)})
	if err != nil {
		t.Fatalf("record failed run: %v", err)
	}
	if err := jobsStore.CompleteRun(runFailed.ID, jobs.RunStatusFailed, intPtrMCP(2), "boom"); err != nil {
		t.Fatalf("complete failed run: %v", err)
	}

	session := connectClient(t, srv)
	mcpResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "legator_list_job_runs",
		Arguments: map[string]any{
			"job_id": job.ID,
			"status": jobs.RunStatusFailed,
			"limit":  10,
		},
	})
	if err != nil {
		t.Fatalf("call legator_list_job_runs: %v", err)
	}
	var mcpPayload jobRunsSummaryPayload
	decodeToolJSON(t, mcpResult, &mcpPayload)

	handler := jobs.NewHandler(jobsStore, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/runs?job_id="+job.ID+"&status=failed&limit=10", nil)
	rr := httptest.NewRecorder()
	handler.HandleListAllRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var httpPayload jobRunsSummaryPayload
	if err := json.Unmarshal(rr.Body.Bytes(), &httpPayload); err != nil {
		t.Fatalf("decode http runs payload: %v", err)
	}

	if mcpPayload.Count != httpPayload.Count || mcpPayload.FailedCount != httpPayload.FailedCount {
		t.Fatalf("summary mismatch mcp=%+v http=%+v", mcpPayload, httpPayload)
	}
	if len(mcpPayload.Runs) != 1 || len(httpPayload.Runs) != 1 {
		t.Fatalf("expected one failed run in both payloads mcp=%d http=%d", len(mcpPayload.Runs), len(httpPayload.Runs))
	}
	if mcpPayload.Runs[0].ID != httpPayload.Runs[0].ID {
		t.Fatalf("run id mismatch mcp=%s http=%s", mcpPayload.Runs[0].ID, httpPayload.Runs[0].ID)
	}
}

func TestGetRunPollAndOutputTools(t *testing.T) {
	srv, _, _, jobsStore := newTestMCPServer(t)

	job, err := jobsStore.CreateJob(jobs.Job{Name: "poll", Command: "echo poll", Schedule: "1m", Target: jobs.Target{Kind: jobs.TargetKindProbe, Value: "probe-1"}, Enabled: true})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	run, err := jobsStore.RecordRunStart(jobs.JobRun{JobID: job.ID, ProbeID: "probe-1", RequestID: "req-poll", StartedAt: time.Now().UTC().Add(-time.Minute)})
	if err != nil {
		t.Fatalf("record run: %v", err)
	}

	session := connectClient(t, srv)
	getRunResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "legator_get_job_run",
		Arguments: map[string]any{"run_id": run.ID},
	})
	if err != nil {
		t.Fatalf("call legator_get_job_run: %v", err)
	}
	var getRunPayload struct {
		Run      jobs.JobRun `json:"run"`
		Active   bool        `json:"active"`
		Terminal bool        `json:"terminal"`
	}
	decodeToolJSON(t, getRunResult, &getRunPayload)
	if !getRunPayload.Active || getRunPayload.Terminal {
		t.Fatalf("expected pending run to be active and non-terminal: %+v", getRunPayload)
	}

	pollResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "legator_poll_job_active",
		Arguments: map[string]any{
			"job_id":       job.ID,
			"wait_ms":      1,
			"include_runs": true,
		},
	})
	if err != nil {
		t.Fatalf("call legator_poll_job_active: %v", err)
	}
	var pollPayload pollActiveJobStatusPayload
	decodeToolJSON(t, pollResult, &pollPayload)
	if !pollPayload.Active || pollPayload.Completed {
		t.Fatalf("expected active poll payload for pending run: %+v", pollPayload)
	}

	if err := jobsStore.CompleteRun(run.ID, jobs.RunStatusSuccess, intPtrMCP(0), "done"); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	streamResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "legator_stream_job_run_output",
		Arguments: map[string]any{"run_id": run.ID, "wait_ms": 1},
	})
	if err != nil {
		t.Fatalf("call legator_stream_job_run_output: %v", err)
	}
	var outputPayload streamJobRunOutputPayload
	decodeToolJSON(t, streamResult, &outputPayload)
	if !outputPayload.Terminal || outputPayload.Status != jobs.RunStatusSuccess {
		t.Fatalf("expected terminal success payload, got %+v", outputPayload)
	}
	if outputPayload.BufferedOutput != "done" {
		t.Fatalf("expected buffered output 'done', got %q", outputPayload.BufferedOutput)
	}
}

func TestStreamJobEventsToolAndResources(t *testing.T) {
	srv, _, auditStore, jobsStore := newTestMCPServer(t)

	job, err := jobsStore.CreateJob(jobs.Job{Name: "evt", Command: "echo evt", Schedule: "1m", Target: jobs.Target{Kind: jobs.TargetKindProbe, Value: "probe-1"}, Enabled: true})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	run, err := jobsStore.RecordRunStart(jobs.JobRun{JobID: job.ID, ProbeID: "probe-1", RequestID: "req-evt", StartedAt: time.Now().UTC().Add(-time.Minute)})
	if err != nil {
		t.Fatalf("record run: %v", err)
	}

	auditStore.Record(audit.Event{Type: audit.EventJobRunStarted, ProbeID: "probe-1", Summary: "job started", Detail: map[string]any{"job_id": job.ID, "run_id": run.ID, "request_id": run.RequestID}})
	auditStore.Record(audit.Event{Type: audit.EventCommandSent, ProbeID: "probe-1", Summary: "ignore non-job"})

	session := connectClient(t, srv)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "legator_stream_job_events",
		Arguments: map[string]any{
			"job_id":  job.ID,
			"wait_ms": 0,
			"limit":   10,
		},
	})
	if err != nil {
		t.Fatalf("call legator_stream_job_events: %v", err)
	}
	var payload streamJobEventsPayload
	decodeToolJSON(t, result, &payload)
	if payload.Count != 1 || len(payload.Events) != 1 {
		t.Fatalf("expected one filtered job event, got %+v", payload)
	}
	if payload.Events[0].Type != audit.EventJobRunStarted {
		t.Fatalf("expected job.run.started event, got %s", payload.Events[0].Type)
	}

	jobsResource, err := srv.handleJobsListResource(context.Background(), nil)
	if err != nil {
		t.Fatalf("read jobs list resource: %v", err)
	}
	if len(jobsResource.Contents) != 1 {
		t.Fatalf("expected one jobs list resource content block, got %d", len(jobsResource.Contents))
	}
	var resourceJobs []jobs.Job
	if err := json.Unmarshal([]byte(jobsResource.Contents[0].Text), &resourceJobs); err != nil {
		t.Fatalf("decode jobs list resource: %v", err)
	}
	if len(resourceJobs) != 1 || resourceJobs[0].ID != job.ID {
		t.Fatalf("unexpected jobs list resource payload: %+v", resourceJobs)
	}

	activeRunsResource, err := srv.handleJobsActiveRunsResource(context.Background(), &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: resourceJobsActiveRuns}})
	if err != nil {
		t.Fatalf("read jobs active runs resource: %v", err)
	}
	if len(activeRunsResource.Contents) != 1 {
		t.Fatalf("expected one jobs active runs content block, got %d", len(activeRunsResource.Contents))
	}
	var activePayload struct {
		Runs  []jobs.JobRun `json:"runs"`
		Count int           `json:"count"`
	}
	if err := json.Unmarshal([]byte(activeRunsResource.Contents[0].Text), &activePayload); err != nil {
		t.Fatalf("decode active runs resource: %v", err)
	}
	if activePayload.Count != 1 || len(activePayload.Runs) != 1 || activePayload.Runs[0].ID != run.ID {
		t.Fatalf("unexpected active runs payload: %+v", activePayload)
	}
}

func intPtrMCP(v int) *int {
	return &v
}
