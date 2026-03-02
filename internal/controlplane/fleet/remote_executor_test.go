package fleet

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

type fakeRemoteRunner struct {
	results map[string]*RemoteRunResult
	errs    map[string]error
	calls   []string
}

func (f *fakeRemoteRunner) Run(_ context.Context, _ RemoteExecutionTarget, command string, _ time.Duration, onChunk func(stream, data string)) (*RemoteRunResult, error) {
	f.calls = append(f.calls, command)
	if onChunk != nil {
		onChunk("stdout", "chunk:"+command)
	}
	if err := f.errs[command]; err != nil {
		return nil, err
	}
	if res, ok := f.results[command]; ok {
		copy := *res
		return &copy, nil
	}
	return &RemoteRunResult{Stdout: "", Stderr: "", ExitCode: 0, Duration: 10 * time.Millisecond}, nil
}

func remoteProbeFixture() *ProbeState {
	return &ProbeState{
		ID:     "rpr-1",
		Type:   ProbeTypeRemote,
		Status: "pending",
		Remote: &RemoteProbeConfig{
			Host:     "10.0.0.8",
			Port:     22,
			Username: "root",
		},
		RemoteCredentials: &RemoteProbeCredentials{Password: "secret"},
	}
}

func TestRemoteExecutorExecute(t *testing.T) {
	runner := &fakeRemoteRunner{results: map[string]*RemoteRunResult{
		"echo 'hello world'": {
			Stdout:   "hello world\n",
			Stderr:   "",
			ExitCode: 0,
			Duration: 25 * time.Millisecond,
		},
	}}
	exec := &RemoteExecutor{
		runner:           runner,
		defaultTimeout:   30 * time.Second,
		inventoryTimeout: 45 * time.Second,
		maxOutputBytes:   128 * 1024,
		now:              func() time.Time { return time.Now().UTC() },
	}

	chunks := make([]protocol.OutputChunkPayload, 0)
	result, err := exec.Execute(context.Background(), remoteProbeFixture(), protocol.CommandPayload{
		RequestID: "req-remote-1",
		Command:   "echo",
		Args:      []string{"hello world"},
	}, func(chunk protocol.OutputChunkPayload) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("execute remote command: %v", err)
	}
	if result.RequestID != "req-remote-1" {
		t.Fatalf("expected request id req-remote-1, got %s", result.RequestID)
	}
	if strings.TrimSpace(result.Stdout) != "hello world" {
		t.Fatalf("unexpected stdout: %q", result.Stdout)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
	if len(chunks) == 0 {
		t.Fatal("expected streamed chunks")
	}
	if !chunks[len(chunks)-1].Final {
		t.Fatalf("expected final chunk, got %+v", chunks[len(chunks)-1])
	}
	if len(runner.calls) != 1 || runner.calls[0] != "echo 'hello world'" {
		t.Fatalf("unexpected command call list: %#v", runner.calls)
	}
}

func TestRemoteExecutorCollectInventory(t *testing.T) {
	runner := &fakeRemoteRunner{results: map[string]*RemoteRunResult{
		"hostname": {Stdout: "node-a\n", ExitCode: 0, Duration: 10 * time.Millisecond},
		"uname -s": {Stdout: "Linux\n", ExitCode: 0, Duration: 10 * time.Millisecond},
		"uname -m": {Stdout: "amd64\n", ExitCode: 0, Duration: 10 * time.Millisecond},
		"uname -r": {Stdout: "6.8.0\n", ExitCode: 0, Duration: 10 * time.Millisecond},
		"getconf _NPROCESSORS_ONLN 2>/dev/null || nproc 2>/dev/null || echo 1": {Stdout: "4\n", ExitCode: 0, Duration: 10 * time.Millisecond},
		"awk '/MemTotal/ {print $2*1024}' /proc/meminfo 2>/dev/null || echo 0": {Stdout: "8589934592\n", ExitCode: 0, Duration: 10 * time.Millisecond},
		"df -B1 --total 2>/dev/null | awk '/total/ {print $2}' | tail -n1":   {Stdout: "17179869184\n", ExitCode: 0, Duration: 10 * time.Millisecond},
		"ip -o addr show 2>/dev/null || ip addr 2>/dev/null":                   {Stdout: "2: eth0 inet 10.0.0.8/24 brd 10.0.0.255 scope global eth0\n", ExitCode: 0, Duration: 10 * time.Millisecond},
		"(dpkg-query -W -f='${Package}\t${Version}\n' 2>/dev/null || rpm -qa --qf '%{NAME}\t%{VERSION}-%{RELEASE}\n' 2>/dev/null || apk info -v 2>/dev/null || true) | head -n 100": {
			Stdout:   "bash\t5.2.0\n",
			ExitCode: 0,
			Duration: 10 * time.Millisecond,
		},
		"(systemctl list-units --type=service --no-pager --no-legend 2>/dev/null || service --status-all 2>/dev/null || true) | head -n 100": {
			Stdout:   "sshd.service loaded active running OpenSSH\n",
			ExitCode: 0,
			Duration: 10 * time.Millisecond,
		},
		"getent passwd 2>/dev/null || cat /etc/passwd 2>/dev/null || true": {
			Stdout:   "root:x:0:0:root:/root:/bin/bash\n",
			ExitCode: 0,
			Duration: 10 * time.Millisecond,
		},
	}}
	exec := &RemoteExecutor{
		runner:           runner,
		defaultTimeout:   30 * time.Second,
		inventoryTimeout: 45 * time.Second,
		maxOutputBytes:   128 * 1024,
		now:              func() time.Time { return time.Now().UTC() },
	}

	inv, err := exec.CollectInventory(context.Background(), remoteProbeFixture())
	if err != nil {
		t.Fatalf("collect inventory: %v", err)
	}
	if inv.Hostname != "node-a" {
		t.Fatalf("expected hostname node-a, got %q", inv.Hostname)
	}
	if inv.OS != "linux" || inv.Arch != "amd64" {
		t.Fatalf("unexpected os/arch: %s/%s", inv.OS, inv.Arch)
	}
	if inv.CPUs != 4 {
		t.Fatalf("expected 4 CPUs, got %d", inv.CPUs)
	}
	if inv.MemTotal == 0 || inv.DiskTotal == 0 {
		t.Fatalf("expected non-zero mem/disk totals: mem=%d disk=%d", inv.MemTotal, inv.DiskTotal)
	}
	if len(inv.Interfaces) == 0 {
		t.Fatal("expected interfaces to be parsed")
	}
	if len(inv.Packages) == 0 || inv.Packages[0].Name != "bash" {
		t.Fatalf("expected parsed package inventory, got %#v", inv.Packages)
	}
	if len(inv.Users) == 0 || inv.Users[0].Name != "root" {
		t.Fatalf("expected parsed user inventory, got %#v", inv.Users)
	}
}

func TestRemoteTargetFromProbeValidation(t *testing.T) {
	_, err := remoteTargetFromProbe(&ProbeState{ID: "p1", Type: ProbeTypeAgent})
	if err == nil {
		t.Fatal("expected error when probe is not remote")
	}

	probe := remoteProbeFixture()
	probe.RemoteCredentials = nil
	_, err = remoteTargetFromProbe(probe)
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("expected credentials error, got %v", err)
	}

	probe = remoteProbeFixture()
	probe.Remote.Host = ""
	_, err = remoteTargetFromProbe(probe)
	if err == nil {
		t.Fatal("expected host validation error")
	}

	probe = remoteProbeFixture()
	target, err := remoteTargetFromProbe(probe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.Host != "10.0.0.8" || target.Username != "root" {
		t.Fatalf("unexpected target: %+v", target)
	}
	if target.Password == "" {
		t.Fatalf("expected password to be preserved, got %+v", target)
	}
	if fmt.Sprintf("%d", target.Port) != "22" {
		t.Fatalf("unexpected port: %d", target.Port)
	}
}
