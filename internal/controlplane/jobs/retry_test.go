package jobs

import (
	"testing"
	"time"
)

func TestResolveRetryPolicyUsesDefaultsAndOverrides(t *testing.T) {
	resolved, err := resolveRetryPolicy(&RetryPolicy{MaxAttempts: 4, InitialBackoff: "2s"}, RetryPolicy{Multiplier: 3, MaxBackoff: "10s"})
	if err != nil {
		t.Fatalf("resolve retry policy: %v", err)
	}
	if resolved.MaxAttempts != 4 {
		t.Fatalf("max attempts = %d, want 4", resolved.MaxAttempts)
	}
	if resolved.InitialBackoff != 2*time.Second {
		t.Fatalf("initial backoff = %s, want 2s", resolved.InitialBackoff)
	}
	if resolved.Multiplier != 3 {
		t.Fatalf("multiplier = %v, want 3", resolved.Multiplier)
	}
	if resolved.MaxBackoff != 10*time.Second {
		t.Fatalf("max backoff = %s, want 10s", resolved.MaxBackoff)
	}
}

func TestRetryDelayProgressionAndCap(t *testing.T) {
	policy := resolvedRetryPolicy{
		MaxAttempts:    5,
		InitialBackoff: 100 * time.Millisecond,
		Multiplier:     2,
		MaxBackoff:     250 * time.Millisecond,
	}

	if got := policy.nextRetryDelay(1); got != 100*time.Millisecond {
		t.Fatalf("attempt 1 delay = %s, want 100ms", got)
	}
	if got := policy.nextRetryDelay(2); got != 200*time.Millisecond {
		t.Fatalf("attempt 2 delay = %s, want 200ms", got)
	}
	if got := policy.nextRetryDelay(3); got != 250*time.Millisecond {
		t.Fatalf("attempt 3 delay = %s, want capped 250ms", got)
	}
}
