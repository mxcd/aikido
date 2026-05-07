package retry

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestDo_SucceedsImmediately(t *testing.T) {
	t.Parallel()
	var calls int32
	err := Do(context.Background(), DefaultPolicy(), func(attempt int) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
}

func TestDo_RetriesUntilSuccess(t *testing.T) {
	t.Parallel()
	var calls int32
	policy := Policy{
		MaxAttempts: 3,
		BaseDelay:   1 * time.Millisecond,
		MaxDelay:    5 * time.Millisecond,
		Multiplier:  2.0,
		Jitter:      0,
		// Retry every error.
		ShouldRetry: func(err error) bool { return true },
	}
	err := Do(context.Background(), policy, func(attempt int) error {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

func TestDo_StopsOnNonRetryableError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("permanent")
	var calls int32
	policy := Policy{
		MaxAttempts: 5,
		BaseDelay:   1 * time.Millisecond,
		MaxDelay:    5 * time.Millisecond,
		Multiplier:  2.0,
		ShouldRetry: func(err error) bool { return false },
	}
	err := Do(context.Background(), policy, func(attempt int) error {
		atomic.AddInt32(&calls, 1)
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (no retry on non-retryable)", got)
	}
}

func TestDo_StopsAfterMaxAttempts(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("transient")
	var calls int32
	policy := Policy{
		MaxAttempts: 3,
		BaseDelay:   1 * time.Millisecond,
		MaxDelay:    5 * time.Millisecond,
		Multiplier:  2.0,
		ShouldRetry: func(err error) bool { return true },
	}
	err := Do(context.Background(), policy, func(attempt int) error {
		atomic.AddInt32(&calls, 1)
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3 (MaxAttempts)", got)
	}
}

func TestDo_HonorsRetryAfter(t *testing.T) {
	t.Parallel()
	var calls int32
	policy := Policy{
		MaxAttempts: 3,
		BaseDelay:   1 * time.Hour, // would block forever w/o RetryAfter override
		MaxDelay:    1 * time.Hour,
		Multiplier:  2.0,
		ShouldRetry: func(err error) bool { return true },
	}
	start := time.Now()
	err := Do(context.Background(), policy, func(attempt int) error {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			return &RetryAfterError{Cause: errors.New("rate limited"), After: 10 * time.Millisecond}
		}
		return nil
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %v; should have waited only ~10ms (Retry-After override)", elapsed)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
}

func TestDo_RespectsContextCancellation(t *testing.T) {
	t.Parallel()
	policy := Policy{
		MaxAttempts: 5,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    1 * time.Second,
		Multiplier:  2.0,
		ShouldRetry: func(err error) bool { return true },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := Do(ctx, policy, func(attempt int) error {
		return errors.New("transient")
	})
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want ctx error", err)
	}
}

func TestDefaultPolicy_Sane(t *testing.T) {
	t.Parallel()
	p := DefaultPolicy()
	if p.MaxAttempts < 2 {
		t.Errorf("MaxAttempts = %d, want >= 2", p.MaxAttempts)
	}
	if p.BaseDelay <= 0 {
		t.Errorf("BaseDelay = %v, want > 0", p.BaseDelay)
	}
	if p.MaxDelay < p.BaseDelay {
		t.Errorf("MaxDelay (%v) < BaseDelay (%v)", p.MaxDelay, p.BaseDelay)
	}
	if p.Multiplier < 1 {
		t.Errorf("Multiplier = %v, want >= 1", p.Multiplier)
	}
	if p.ShouldRetry == nil {
		t.Error("ShouldRetry must not be nil")
	}
}
