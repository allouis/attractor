package engine

import (
	"math/rand"
	"time"
)

// BackoffConfig controls retry pacing (spec §3.6).
type BackoffConfig struct {
	InitialDelay  time.Duration
	BackoffFactor float64
	MaxDelay      time.Duration
	Jitter        bool
}

// DelayForAttempt returns the sleep duration for the 1-indexed attempt
// number, applying configured backoff and optional jitter.
func (b BackoffConfig) DelayForAttempt(attempt int, rng *rand.Rand) time.Duration {
	if attempt <= 0 {
		return 0
	}
	delay := float64(b.InitialDelay)
	for i := 1; i < attempt; i++ {
		delay *= b.BackoffFactor
	}
	if d := time.Duration(delay); d > b.MaxDelay && b.MaxDelay > 0 {
		delay = float64(b.MaxDelay)
	}
	if b.Jitter && rng != nil {
		// Uniform multiplier in [0.5, 1.5) per spec §3.6.
		delay *= 0.5 + rng.Float64()
	}
	return time.Duration(delay)
}

// RetryPolicy bundles attempt limit, backoff, and a should-retry
// predicate (spec §3.6).
type RetryPolicy struct {
	MaxAttempts int
	Backoff     BackoffConfig
	ShouldRetry func(err error) bool
}

// PolicyNone is the no-retry preset.
func PolicyNone() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 1,
		ShouldRetry: defaultShouldRetry,
	}
}

// PolicyStandard is the general-purpose preset (5 attempts, 200ms x2).
func PolicyStandard() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 5,
		Backoff: BackoffConfig{
			InitialDelay:  200 * time.Millisecond,
			BackoffFactor: 2.0,
			MaxDelay:      60 * time.Second,
			Jitter:        true,
		},
		ShouldRetry: defaultShouldRetry,
	}
}

// PolicyAggressive widens the standard preset (500ms initial, x2).
func PolicyAggressive() RetryPolicy {
	p := PolicyStandard()
	p.Backoff.InitialDelay = 500 * time.Millisecond
	return p
}

// PolicyLinear retries 3 times with a fixed 500ms delay.
func PolicyLinear() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 3,
		Backoff: BackoffConfig{
			InitialDelay:  500 * time.Millisecond,
			BackoffFactor: 1.0,
			MaxDelay:      60 * time.Second,
		},
		ShouldRetry: defaultShouldRetry,
	}
}

// PolicyPatient retries 3 times with 2s initial and a 3x factor for
// long-running operations.
func PolicyPatient() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 3,
		Backoff: BackoffConfig{
			InitialDelay:  2 * time.Second,
			BackoffFactor: 3.0,
			MaxDelay:      60 * time.Second,
			Jitter:        true,
		},
		ShouldRetry: defaultShouldRetry,
	}
}

// defaultShouldRetry retries any non-nil error. Specific handlers may
// supply their own predicate based on error category.
func defaultShouldRetry(err error) bool { return err != nil }

// buildPolicy resolves a per-node retry policy by combining the node's
// max_retries attribute with the graph default.
func buildPolicy(node *struct {
	MaxRetries int
}, graphDefault int) RetryPolicy {
	max := node.MaxRetries
	if max < 0 {
		max = graphDefault
	}
	if max == 0 {
		return PolicyNone()
	}
	p := PolicyStandard()
	p.MaxAttempts = max + 1
	return p
}
