// Package retry is an exponential-backoff helper used internally by provider
// clients (e.g. llm/openrouter) and exposed publicly so callers can wrap
// higher-level operations (e.g. llm.CollectWithRetry) with the same policy
// shape. See ADR-027 for the promotion from internal to public.
//
// The shape is small on purpose: a single Do function driven by a Policy
// struct, with a RetryAfterError escape hatch for honoring HTTP 429
// Retry-After headers.
package retry

import (
	"context"
	"errors"
	"math/rand"
	"time"
)

// Policy configures Do.
type Policy struct {
	// MaxAttempts is the total number of attempts (>= 1). MaxAttempts == 1 is
	// "no retry, just call once."
	MaxAttempts int

	// BaseDelay is the initial backoff delay before the second attempt.
	BaseDelay time.Duration

	// MaxDelay caps any single backoff sleep.
	MaxDelay time.Duration

	// Multiplier is the exponential growth factor between attempts (>= 1.0).
	Multiplier float64

	// Jitter, in the [0, 1] range, is the proportional jitter applied to each
	// computed sleep. Jitter == 0 means no randomization. Jitter == 0.2 means
	// the actual sleep is uniformly random in [d*(1-0.2), d*(1+0.2)].
	Jitter float64

	// ShouldRetry decides whether err is retryable. Required (non-nil).
	ShouldRetry func(err error) bool
}

// DefaultPolicy returns the project-wide default retry policy: 3 attempts,
// 500ms base, 30s cap, 2x multiplier, 20% jitter. The default ShouldRetry
// retries every error — provider clients should override with a typed-error
// predicate.
func DefaultPolicy() Policy {
	return Policy{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    30 * time.Second,
		Multiplier:  2.0,
		Jitter:      0.2,
		ShouldRetry: func(err error) bool { return err != nil },
	}
}

// RetryAfterError is an error that overrides the policy's computed backoff
// with an explicit duration. Provider clients wrap their 429-with-Retry-After
// failures in RetryAfterError so this package honors the server's hint.
type RetryAfterError struct {
	Cause error
	After time.Duration
}

func (e *RetryAfterError) Error() string {
	if e.Cause == nil {
		return "retry after"
	}
	return e.Cause.Error()
}

func (e *RetryAfterError) Unwrap() error { return e.Cause }

// Do invokes fn until it returns nil, ctx cancels, MaxAttempts is reached, or
// fn returns an error policy.ShouldRetry deems non-retryable. Returns the
// final error (the last fn return value, or ctx.Err() if context cancelled
// during a sleep).
//
// The attempt argument passed to fn is 1-based.
func Do(ctx context.Context, policy Policy, fn func(attempt int) error) error {
	if policy.MaxAttempts < 1 {
		policy.MaxAttempts = 1
	}
	if policy.ShouldRetry == nil {
		policy.ShouldRetry = func(err error) bool { return err != nil }
	}

	var lastErr error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		// Bail on context before each attempt.
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return lastErr
			}
			return err
		}

		err := fn(attempt)
		if err == nil {
			return nil
		}
		lastErr = err

		if !policy.ShouldRetry(err) {
			return err
		}
		if attempt == policy.MaxAttempts {
			return err
		}

		// Determine sleep duration. If the error carries an explicit
		// Retry-After hint, honor it (capped by MaxDelay).
		var sleep time.Duration
		var rae *RetryAfterError
		if errors.As(err, &rae) && rae.After > 0 {
			sleep = rae.After
		} else {
			sleep = computeBackoff(policy, attempt)
		}
		if policy.MaxDelay > 0 && sleep > policy.MaxDelay {
			sleep = policy.MaxDelay
		}

		select {
		case <-ctx.Done():
			// Prefer the context error over the last attempt's error so
			// callers can detect cancellation cleanly.
			return ctx.Err()
		case <-time.After(sleep):
		}
	}
	return lastErr
}

// computeBackoff returns the unjittered (well, jittered) base * mult^(attempt-1).
// attempt is 1-based; the wait between attempt N and attempt N+1 uses
// computeBackoff(_, N).
func computeBackoff(p Policy, attempt int) time.Duration {
	if p.BaseDelay <= 0 {
		return 0
	}
	d := float64(p.BaseDelay)
	for i := 1; i < attempt; i++ {
		d *= p.Multiplier
	}
	if p.Jitter > 0 {
		// Uniform in [d*(1-j), d*(1+j)].
		j := p.Jitter
		if j > 1 {
			j = 1
		}
		// math/rand is fine here — this isn't security-sensitive and seeding
		// global rand at package init is intentional simplicity.
		d *= 1 + j*(2*rand.Float64()-1) //nolint:gosec
	}
	if d < 0 {
		d = 0
	}
	return time.Duration(d)
}
