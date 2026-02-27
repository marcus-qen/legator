package executor

import (
	"runtime"
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
)

func TestBuildExecSpecRequiresCommand(t *testing.T) {
	_, err := buildExecSpec(&protocol.CommandPayload{})
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestBuildExecSpecPassThroughOnNonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-windows behaviour")
	}

	spec, err := buildExecSpec(&protocol.CommandPayload{
		Command: "echo",
		Args:    []string{"hello"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.name != "echo" {
		t.Fatalf("expected command echo, got %q", spec.name)
	}
	if len(spec.args) != 1 || spec.args[0] != "hello" {
		t.Fatalf("unexpected args: %#v", spec.args)
	}
}
