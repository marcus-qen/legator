package approvalpolicy

import (
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
)

// TestClassifyCommandWithMetadata_ShellObserveDefence is a defence-in-depth
// test: when the classifier receives "sh" or "bash" as the base command, it
// should return CapObserve rather than falling through to the unknown-mutation
// fallback. The inner command determines real risk; shell invocation itself is
// not inherently destructive.
func TestClassifyCommandWithMetadata_ShellObserveDefence(t *testing.T) {
	cases := []struct {
		command string
		args    []string
	}{
		{"sh", []string{"-c", "hostname"}},
		{"bash", []string{"-c", "hostname"}},
		{"bash", []string{"-c", "uptime"}},
	}

	for _, tc := range cases {
		name := tc.command
		if len(tc.args) > 0 {
			name += " " + tc.args[0]
		}
		t.Run(name, func(t *testing.T) {
			result := classifyCommandWithMetadata(tc.command, tc.args)
			if result.Level != protocol.CapObserve {
				t.Errorf("classifyCommandWithMetadata(%q, %v) = level %v, want CapObserve (reasonCode: %s)",
					tc.command, tc.args, result.Level, result.ReasonCode)
			}
		})
	}
}
