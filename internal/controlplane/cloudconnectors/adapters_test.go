package cloudconnectors

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

type fakeRunner struct {
	responses map[string]fakeResponse
	calls     []string
}

type fakeResponse struct {
	stdout string
	stderr string
	err    error
}

func (f *fakeRunner) Run(_ context.Context, command string, args ...string) ([]byte, []byte, error) {
	key := strings.TrimSpace(command + " " + strings.Join(args, " "))
	f.calls = append(f.calls, key)
	resp, ok := f.responses[key]
	if !ok {
		return nil, nil, errors.New("unexpected command")
	}
	return []byte(resp.stdout), []byte(resp.stderr), resp.err
}

func TestCLIAdapterAWSNormalizationAndAllowlist(t *testing.T) {
	runner := &fakeRunner{responses: map[string]fakeResponse{
		"aws sts get-caller-identity --output json": {
			stdout: `{"Account":"123456789012","Arn":"arn:aws:iam::123456789012:user/test"}`,
		},
		"aws ec2 describe-instances --output json": {
			stdout: `{"Reservations":[{"Instances":[{"InstanceId":"i-abc123","State":{"Name":"running"},"Placement":{"AvailabilityZone":"us-east-1a"},"Tags":[{"Key":"Name","Value":"web-01"}]}]}]}`,
		},
	}}

	adapter := NewCLIAdapterWithRunner(runner)
	assets, err := adapter.Scan(context.Background(), Connector{ID: "c1", Provider: ProviderAWS})
	if err != nil {
		t.Fatalf("scan aws: %v", err)
	}
	if len(assets) != 2 {
		t.Fatalf("expected 2 assets (account + instance), got %d", len(assets))
	}
	if assets[0].AssetType != "account" {
		t.Fatalf("expected first asset account, got %q", assets[0].AssetType)
	}
	if assets[1].AssetType != "instance" || assets[1].Region != "us-east-1" {
		t.Fatalf("unexpected normalized instance asset: %+v", assets[1])
	}

	wantCalls := []string{
		"aws sts get-caller-identity --output json",
		"aws ec2 describe-instances --output json",
	}
	if len(runner.calls) != len(wantCalls) {
		t.Fatalf("expected %d CLI calls, got %d (%v)", len(wantCalls), len(runner.calls), runner.calls)
	}
	for i := range wantCalls {
		if runner.calls[i] != wantCalls[i] {
			t.Fatalf("unexpected call[%d]: got %q want %q", i, runner.calls[i], wantCalls[i])
		}
	}
}

func TestCLIAdapterMissingBinaryReturnsStructuredError(t *testing.T) {
	runner := &fakeRunner{responses: map[string]fakeResponse{
		"aws sts get-caller-identity --output json": {
			err: &exec.Error{Name: "aws", Err: exec.ErrNotFound},
		},
	}}

	adapter := NewCLIAdapterWithRunner(runner)
	_, err := adapter.Scan(context.Background(), Connector{ID: "c1", Provider: ProviderAWS})
	if err == nil {
		t.Fatal("expected error")
	}

	scanErr, ok := err.(*ScanError)
	if !ok {
		t.Fatalf("expected ScanError, got %T", err)
	}
	if scanErr.Code != "cli_missing" {
		t.Fatalf("expected cli_missing, got %q", scanErr.Code)
	}
}
