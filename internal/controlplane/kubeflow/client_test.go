package kubeflow

import (
	"context"
	"errors"
	"fmt"
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
}

func (f *fakeRunner) Run(_ context.Context, command string, args ...string) ([]byte, []byte, error) {
	key := strings.TrimSpace(command + " " + strings.Join(args, " "))
	f.calls = append(f.calls, key)
	res, ok := f.results[key]
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
