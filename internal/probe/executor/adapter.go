package executor

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/marcus-qen/legator/internal/protocol"
)

type execSpec struct {
	name string
	args []string
}

func buildExecSpec(cmd *protocol.CommandPayload) (execSpec, error) {
	if strings.TrimSpace(cmd.Command) == "" {
		return execSpec{}, fmt.Errorf("command is required")
	}

	if runtime.GOOS != "windows" {
		return execSpec{name: cmd.Command, args: cmd.Args}, nil
	}

	command := strings.TrimSpace(strings.ToLower(cmd.Command))
	switch command {
	case "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		if len(cmd.Args) > 0 {
			first := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
			if strings.HasPrefix(first, "-") {
				return execSpec{name: "powershell.exe", args: cmd.Args}, nil
			}
		}
		script := strings.TrimSpace(strings.Join(cmd.Args, " "))
		if script == "" {
			return execSpec{}, fmt.Errorf("powershell command requires script arguments")
		}
		return execSpec{name: "powershell.exe", args: []string{"-NoProfile", "-NonInteractive", "-Command", script}}, nil
	case "cmd", "cmd.exe":
		if len(cmd.Args) > 0 {
			arg0 := strings.ToLower(strings.TrimSpace(cmd.Args[0]))
			if arg0 == "/c" || arg0 == "/k" {
				return execSpec{name: "cmd.exe", args: cmd.Args}, nil
			}
		}
		line := strings.TrimSpace(strings.Join(cmd.Args, " "))
		if line == "" {
			return execSpec{}, fmt.Errorf("cmd command requires arguments")
		}
		return execSpec{name: "cmd.exe", args: []string{"/C", line}}, nil
	default:
		args := append([]string{"/C", cmd.Command}, cmd.Args...)
		return execSpec{name: "cmd.exe", args: args}, nil
	}
}
