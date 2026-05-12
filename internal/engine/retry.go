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

// RetryPolicy bundles attempt limit and backoff config (spec §3.6).
// Additional preset names (aggressive/linear/patient) are not yet wired
// to a per-node `retry_policy` attribute; they will be added when that
// selector lands.
type RetryPolicy struct {
	MaxAttempts int
	Backoff     BackoffConfig
}

// PolicyNone is the no-retry preset.
func PolicyNone() RetryPolicy {
	return RetryPolicy{MaxAttempts: 1}
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
	}
}
