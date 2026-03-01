package kubeflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type runResult struct {
	stdout string
	stderr string
	err    error
}

type fakeRunner struct {
	results map[string]runResult
	calls   []string
	runFn   func(command string, args ...string) (runResult, bool)
}

func (f *fakeRunner) Run(_ context.Context, command string, args ...string) ([]byte, []byte, error) {
	key := strings.TrimSpace(command + " " + strings.Join(args, " "))
	f.calls = append(f.calls, key)
	res, ok := f.results[key]
	if !ok && f.runFn != nil {
		res, ok = f.runFn(command, args...)
	}
	if !ok {
		return nil, nil, fmt.Errorf("unexpected command: %s", key)
	}
	return []byte(res.stdout), []byte(res.stderr), res.err
}

func TestCLIClientInventoryCollectsKubeflowResources(t *testing.T) {
	runner := &fakeRunner{results: map[string]runResult{
		"kubectl --kubeconfig /tmp/kubeconfig --context lab version --client=true -o json": {
			stdout: `{"clientVersion":{"gitVersion":"v1.31.0"}}`,
		},
		"kubectl --kubeconfig /tmp/kubeconfig --context lab get namespace kubeflow -o json": {
			stdout: `{"metadata":{"name":"kubeflow"}}`,
		},
		"kubectl --kubeconfig /tmp/kubeconfig --context lab api-resources --verbs=list -o name": {
			stdout: "pods\nnotebooks.kubeflow.org\n",
		},
		"kubectl --kubeconfig /tmp/kubeconfig --context lab get pods -n kubeflow -o json": {
			stdout: `{"items":[{"kind":"Pod","metadata":{"name":"ml-pipeline","namespace":"kubeflow","creationTimestamp":"2026-02-28T08:00:00Z"},"status":{"phase":"Running"}}]}`,
		},
		"kubectl --kubeconfig /tmp/kubeconfig --context lab get notebooks.kubeflow.org -n kubeflow -o json": {
			stdout: `{"items":[{"kind":"Notebook","metadata":{"name":"demo-notebook","namespace":"kubeflow","labels":{"team":"ml"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`,
		},
	}}

	client := NewCLIClient(ClientConfig{
		Binary:     "kubectl",
		Kubeconfig: "/tmp/kubeconfig",
		Context:    "lab",
		Namespace:  "kubeflow",
		Timeout:    5 * time.Second,
		Runner:     runner,
	})

	inventory, err := client.Inventory(context.Background())
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if inventory.Namespace != "kubeflow" {
		t.Fatalf("expected namespace kubeflow, got %q", inventory.Namespace)
	}
	if inventory.Counts["Pod"] != 1 {
		t.Fatalf("expected 1 Pod, got %d", inventory.Counts["Pod"])
	}
	if inventory.Counts["Notebook"] != 1 {
		t.Fatalf("expected 1 Notebook, got %d", inventory.Counts["Notebook"])
	}
	if len(inventory.Resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(inventory.Resources))
	}
	if inventory.Resources[0].Kind != "Notebook" || inventory.Resources[0].Status != "Ready" {
		t.Fatalf("unexpected first resource: %+v", inventory.Resources[0])
	}
	if inventory.Resources[1].Kind != "Pod" || inventory.Resources[1].Status != "Running" {
		t.Fatalf("unexpected second resource: %+v", inventory.Resources[1])
	}
}

func TestCLIClientStatusReturnsDisconnectedWhenClusterUnavailable(t *testing.T) {
	runner := &fakeRunner{results: map[string]runResult{
		"kubectl version --client=true -o json": {
			stdout: `{"clientVersion":{"gitVersion":"v1.31.0"}}`,
		},
		"kubectl get namespace kubeflow -o json": {
			stderr: "Unable to connect to the server: dial tcp 10.0.0.1:443: connect: connection refused",
			err:    errors.New("exit status 1"),
		},
	}}

	client := NewCLIClient(ClientConfig{Runner: runner})
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("status should not fail for unreachable cluster: %v", err)
	}
	if status.Connected {
		t.Fatal("expected disconnected status")
	}
	if status.LastError == "" {
		t.Fatal("expected last_error to be populated")
	}
	if status.KubectlVersion != "v1.31.0" {
		t.Fatalf("expected kubectl version v1.31.0, got %q", status.KubectlVersion)
	}
}

func TestCLIClientInventoryReturnsCLIMissing(t *testing.T) {
	runner := &fakeRunner{results: map[string]runResult{
		"kubectl version --client=true -o json": {
			err: &exec.Error{Name: "kubectl", Err: exec.ErrNotFound},
		},
	}}
	client := NewCLIClient(ClientConfig{Runner: runner})

	_, err := client.Inventory(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *ClientError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClientError, got %T", err)
	}
	if ce.Code != "cli_missing" {
		t.Fatalf("expected cli_missing, got %q", ce.Code)
	}
}

func TestCLIClientRunStatus(t *testing.T) {
	runner := &fakeRunner{results: map[string]runResult{
		"kubectl get runs.kubeflow.org run-a -n kubeflow -o json": {
			stdout: `{"kind":"Run","metadata":{"name":"run-a","namespace":"kubeflow"},"status":{"phase":"Running","reason":"Healthy","message":"still running"}}`,
		},
	}}

	client := NewCLIClient(ClientConfig{Runner: runner})
	result, err := client.RunStatus(context.Background(), RunStatusRequest{Name: "run-a"})
	if err != nil {
		t.Fatalf("run status: %v", err)
	}
	if result.Name != "run-a" || result.Namespace != "kubeflow" {
		t.Fatalf("unexpected run status identity: %+v", result)
	}
	if result.Status != "Running" {
		t.Fatalf("expected Running status, got %s", result.Status)
	}
	if result.Reason != "Healthy" {
		t.Fatalf("expected reason Healthy, got %s", result.Reason)
	}
}

func TestCLIClientSubmitRunAppliesManifest(t *testing.T) {
	var capturedManifest map[string]any
	runner := &fakeRunner{runFn: func(command string, args ...string) (runResult, bool) {
		joined := strings.Join(append([]string{command}, args...), " ")
		if !strings.Contains(joined, " apply -f ") || !strings.Contains(joined, " -n kubeflow -o json") {
			return runResult{}, false
		}

		idx := -1
		for i := range args {
			if args[i] == "-f" && i+1 < len(args) {
				idx = i + 1
				break
			}
		}
		if idx == -1 {
			t.Fatalf("expected -f arg in %v", args)
		}

		content, err := os.ReadFile(args[idx])
		if err != nil {
			t.Fatalf("read staged manifest: %v", err)
		}
		if err := json.Unmarshal(content, &capturedManifest); err != nil {
			t.Fatalf("decode staged manifest: %v", err)
		}

		return runResult{stdout: `{"kind":"Run","metadata":{"name":"run-a","namespace":"kubeflow"},"status":{"phase":"Pending"}}`}, true
	}}

	client := NewCLIClient(ClientConfig{Runner: runner})
	result, err := client.SubmitRun(context.Background(), SubmitRunRequest{
		Name: "run-a",
		Manifest: []byte(`{
			"apiVersion":"kubeflow.org/v1",
			"metadata":{},
			"spec":{"dummy":true}
		}`),
	})
	if err != nil {
		t.Fatalf("submit run: %v", err)
	}
	if result.Run.Name != "run-a" {
		t.Fatalf("expected run-a, got %+v", result.Run)
	}
	if result.Transition.Action != "submit" || result.Transition.After == "" {
		t.Fatalf("unexpected transition: %+v", result.Transition)
	}

	metadata, ok := capturedManifest["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata map in captured manifest: %+v", capturedManifest)
	}
	if metadata["name"] != "run-a" {
		t.Fatalf("expected metadata.name run-a, got %+v", metadata)
	}
	if metadata["namespace"] != "kubeflow" {
		t.Fatalf("expected metadata.namespace kubeflow, got %+v", metadata)
	}
}

func TestCLIClientCancelRun(t *testing.T) {
	runner := &fakeRunner{results: map[string]runResult{
		"kubectl get runs.kubeflow.org run-a -n kubeflow -o json": {
			stdout: `{"kind":"Run","metadata":{"name":"run-a","namespace":"kubeflow"},"status":{"phase":"Running"}}`,
		},
		"kubectl delete runs.kubeflow.org run-a -n kubeflow --ignore-not-found=true": {},
	}}
	client := NewCLIClient(ClientConfig{Runner: runner})
	result, err := client.CancelRun(context.Background(), CancelRunRequest{Name: "run-a"})
	if err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if !result.Canceled {
		t.Fatalf("expected canceled=true, got %+v", result)
	}
	if result.Transition.Before != "Running" || result.Transition.After != "canceled" {
		t.Fatalf("unexpected transition: %+v", result.Transition)
	}
}

func TestCLIClientCancelRunNotFound(t *testing.T) {
	runner := &fakeRunner{results: map[string]runResult{
		"kubectl get runs.kubeflow.org run-a -n kubeflow -o json": {
			stderr: "Error from server (NotFound): runs.kubeflow.org \"run-a\" not found",
			err:    errors.New("exit status 1"),
		},
		"kubectl delete runs.kubeflow.org run-a -n kubeflow --ignore-not-found=true": {},
	}}
	client := NewCLIClient(ClientConfig{Runner: runner})
	result, err := client.CancelRun(context.Background(), CancelRunRequest{Name: "run-a"})
	if err != nil {
		t.Fatalf("cancel run not found: %v", err)
	}
	if result.Canceled {
		t.Fatalf("expected canceled=false for missing run, got %+v", result)
	}
	if result.Transition.After != "not_found" {
		t.Fatalf("expected not_found transition, got %+v", result.Transition)
	}
}
