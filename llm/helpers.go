package llm

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mxcd/aikido/retry"
)

// Float32 returns a pointer to v.
//
// Convenience for SessionOptions.Temperature so callers can write inline values.
// Float32(0) returns a non-nil pointer to zero — the deterministic-zero case.
func Float32(v float32) *float32 {
	return &v
}

// IsTransientServerError reports whether err is a transient upstream-provider
// failure (typically a 5xx, mid-stream RST, or other server-side flake) that
// is worth retrying. Authentication errors, invalid-request errors, and rate
// limits are NOT classified as transient — auth and bad-request errors won't
// fix themselves with a retry, and rate limits should be honored explicitly
// (typically via a Retry-After-aware policy at the caller).
//
// Use as the ShouldRetry predicate when retrying llm.Collect:
//
//	llm.CollectWithRetry(ctx, c, req, retry.Policy{
//	    MaxAttempts: 5,
//	    BaseDelay:   2 * time.Second,
//	    MaxDelay:    30 * time.Second,
//	    Multiplier:  2.0,
//	    Jitter:      0.2,
//	    ShouldRetry: llm.IsTransientServerError,
//	})
func IsTransientServerError(err error) bool {
	return errors.Is(err, ErrServerError)
}

// IsContentFilter reports whether err is a content-policy abort surfaced by the
// provider (finish_reason="content_filter", an explicit error envelope with a
// matching code/message, or any error wrapping ErrContentFiltered). Use this
// to short-circuit retry — the same prompt will trip the classifier again on
// the next attempt — and to render a "try different wording" message rather
// than a generic "try again later".
//
// Note: providers sometimes mid-stream RST without sending a structured
// content-filter signal. Those failures surface as ErrServerError and are
// indistinguishable from genuine transient flake at the wire level. Callers
// that want stronger detection can heuristically classify "all retries
// failed identically" as a content-filter signal at the call-site.
func IsContentFilter(err error) bool {
	return errors.Is(err, ErrContentFiltered)
}

// DefaultStreamingRetryPolicy returns a sensible retry policy for image
// generation and other streaming LLM operations: 5 attempts, 2s base, 30s cap,
// 2x multiplier, 20% jitter, retrying only transient provider errors.
//
// Tuned for image-gen preview models (e.g. Gemini flash-image-preview) which
// drop streams under upstream load at ~20% in observed traces. With 5 attempts
// at this rate the effective failure rate is ~0.03%.
func DefaultStreamingRetryPolicy() retry.Policy {
	return retry.Policy{
		MaxAttempts: 5,
		BaseDelay:   2 * time.Second,
		MaxDelay:    30 * time.Second,
		Multiplier:  2.0,
		Jitter:      0.2,
		ShouldRetry: IsTransientServerError,
	}
}

// CollectWithRetry wraps Collect with retry.Do using the supplied policy.
// On retry, the entire stream is restarted from scratch (no resume) — Collect's
// accumulated state is discarded between attempts. The final return values are
// from the last attempt.
//
// Provider-side cost is paid per attempt: a streaming model that aborts after
// emitting partial tokens still bills for those tokens. Tune MaxAttempts with
// cost in mind.
//
// If policy.ShouldRetry is nil, IsTransientServerError is used. If
// policy.MaxAttempts is < 1, it's clamped to 1 (no retry).
func CollectWithRetry(ctx context.Context, c Client, req Request, policy retry.Policy) (text string, calls []ToolCall, images []ImagePart, usage *Usage, err error) {
	if policy.ShouldRetry == nil {
		policy.ShouldRetry = IsTransientServerError
	}
	retryErr := retry.Do(ctx, policy, func(_ int) error {
		text, calls, images, usage, err = Collect(ctx, c, req)
		return err
	})
	// retry.Do returns the last fn error (or ctx.Err() on cancellation). The
	// outputs above are already populated from the final attempt.
	return text, calls, images, usage, retryErr
}

// CompleteWithRetry wraps Client.Complete with retry.Do using the supplied
// policy. Each attempt sends a fresh non-streaming request; the final Response
// is from the last attempt.
//
// Use this for image generation: a single oversized base64 payload exceeds the
// SSE scanner cap and trips ErrServerError on every Stream attempt. Complete
// reads the body as one JSON document and avoids the cap entirely; the retry
// wrapper is here for the standard 429/5xx-at-request-start flake.
//
// If policy.ShouldRetry is nil, IsTransientServerError is used.
func CompleteWithRetry(ctx context.Context, c Client, req Request, policy retry.Policy) (Response, error) {
	if policy.ShouldRetry == nil {
		policy.ShouldRetry = IsTransientServerError
	}
	var resp Response
	retryErr := retry.Do(ctx, policy, func(_ int) error {
		var err error
		resp, err = c.Complete(ctx, req)
		return err
	})
	return resp, retryErr
}

// Collect drains a stream into a final result. Useful for non-streaming callers.
//
// Returns text accumulated from EventTextDelta, all complete tool calls, all
// images surfaced by the provider, final Usage if the provider emitted one,
// and the first error encountered. Thinking text is not included in the
// returned text.
//
// Collect respects ctx cancellation: if ctx is cancelled before the stream
// closes, Collect returns ctx.Err() without waiting for the producer.
func Collect(ctx context.Context, c Client, req Request) (text string, calls []ToolCall, images []ImagePart, usage *Usage, err error) {
	events, err := c.Stream(ctx, req)
	if err != nil {
		return "", nil, nil, nil, err
	}
	var sb strings.Builder
	for {
		select {
		case <-ctx.Done():
			return sb.String(), calls, images, usage, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return sb.String(), calls, images, usage, nil
			}
			switch ev.Kind {
			case EventTextDelta:
				sb.WriteString(ev.Text)
			case EventToolCall:
				if ev.Tool != nil {
					calls = append(calls, *ev.Tool)
				}
			case EventImage:
				if ev.Image != nil {
					images = append(images, *ev.Image)
				}
			case EventUsage:
				usage = ev.Usage
			case EventError:
				return sb.String(), calls, images, usage, ev.Err
			case EventThinking, EventEnd:
				// thinking is not added to text; end is informational
			}
		}
	}
}
