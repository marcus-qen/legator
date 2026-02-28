package jobs

import (
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	defaultRetryMaxAttempts    = 1
	defaultRetryInitialBackoff = 5 * time.Second
	defaultRetryMultiplier     = 2.0
)

type resolvedRetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	Multiplier     float64
	MaxBackoff     time.Duration
}

func defaultResolvedRetryPolicy() resolvedRetryPolicy {
	return resolvedRetryPolicy{
		MaxAttempts:    defaultRetryMaxAttempts,
		InitialBackoff: defaultRetryInitialBackoff,
		Multiplier:     defaultRetryMultiplier,
	}
}

func resolveRetryPolicy(jobPolicy *RetryPolicy, global RetryPolicy) (resolvedRetryPolicy, error) {
	base := defaultResolvedRetryPolicy()
	if err := applyRetryPolicyOverrides(&base, &global); err != nil {
		return resolvedRetryPolicy{}, err
	}
	if err := applyRetryPolicyOverrides(&base, jobPolicy); err != nil {
		return resolvedRetryPolicy{}, err
	}
	return base, nil
}

func applyRetryPolicyOverrides(base *resolvedRetryPolicy, policy *RetryPolicy) error {
	if base == nil || policy == nil {
		return nil
	}

	if policy.MaxAttempts < 0 {
		return fmt.Errorf("retry_policy.max_attempts must be >= 1")
	}
	if policy.MaxAttempts > 0 {
		base.MaxAttempts = policy.MaxAttempts
	}

	if strings.TrimSpace(policy.InitialBackoff) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(policy.InitialBackoff))
		if err != nil || d <= 0 {
			return fmt.Errorf("retry_policy.initial_backoff must be a positive duration")
		}
		base.InitialBackoff = d
	}

	if policy.Multiplier < 0 {
		return fmt.Errorf("retry_policy.multiplier must be >= 1")
	}
	if policy.Multiplier > 0 {
		if policy.Multiplier < 1 {
			return fmt.Errorf("retry_policy.multiplier must be >= 1")
		}
		base.Multiplier = policy.Multiplier
	}

	if strings.TrimSpace(policy.MaxBackoff) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(policy.MaxBackoff))
		if err != nil || d <= 0 {
			return fmt.Errorf("retry_policy.max_backoff must be a positive duration")
		}
		base.MaxBackoff = d
	}

	if base.MaxAttempts <= 0 {
		return fmt.Errorf("retry_policy.max_attempts must be >= 1")
	}
	if base.InitialBackoff <= 0 {
		return fmt.Errorf("retry_policy.initial_backoff must be a positive duration")
	}
	if base.Multiplier < 1 {
		return fmt.Errorf("retry_policy.multiplier must be >= 1")
	}
	if base.MaxBackoff < 0 {
		return fmt.Errorf("retry_policy.max_backoff must be >= 0")
	}

	return nil
}

func validateRetryPolicy(policy *RetryPolicy) error {
	if policy == nil {
		return nil
	}
	_, err := resolveRetryPolicy(policy, RetryPolicy{})
	return err
}

// nextRetryDelay returns the delay before scheduling the next attempt after
// failedAttempt has completed.
func (p resolvedRetryPolicy) nextRetryDelay(failedAttempt int) time.Duration {
	if failedAttempt < 1 {
		failedAttempt = 1
	}

	exponent := float64(failedAttempt - 1)
	multiplier := math.Pow(p.Multiplier, exponent)
	delay := time.Duration(float64(p.InitialBackoff) * multiplier)
	if delay <= 0 {
		delay = p.InitialBackoff
	}
	if p.MaxBackoff > 0 && delay > p.MaxBackoff {
		return p.MaxBackoff
	}
	return delay
}
