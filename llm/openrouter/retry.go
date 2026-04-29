package openrouter

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mxcd/aikido/internal/retry"
	"github.com/mxcd/aikido/llm"
)

// retryPolicy returns the policy used to wrap stream-start. It only retries
// on rate-limit and 5xx errors. Auth and bad-request errors short-circuit.
func retryPolicy() retry.Policy {
	p := retry.DefaultPolicy()
	p.ShouldRetry = func(err error) bool {
		return errors.Is(err, llm.ErrRateLimited) || errors.Is(err, llm.ErrServerError)
	}
	return p
}

// classifyHTTPError maps an HTTP non-200 response onto a wrapped llm.Err*.
// It reads (and closes) resp.Body, attempts to extract the OpenRouter
// error envelope, and returns an error wrapping the appropriate sentinel.
//
// On 429 with a parseable Retry-After header the returned error is wrapped
// in a *retry.RetryAfterError so the retry helper honors the server's hint.
func classifyHTTPError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	// Best-effort message extraction.
	msg := string(body)
	if env := decodeErrorEnvelope(body); env != nil && env.Error.Message != "" {
		msg = env.Error.Message
	}
	// Trim very long bodies for the wrapped error message.
	if len(msg) > 256 {
		msg = msg[:256] + "..."
	}
	msg = strings.TrimSpace(msg)

	sentinel := classifySentinel(resp.StatusCode)
	wrapped := fmt.Errorf("openrouter: status %d: %s: %w", resp.StatusCode, msg, sentinel)

	if resp.StatusCode == http.StatusTooManyRequests {
		if d, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
			return &retry.RetryAfterError{Cause: wrapped, After: d}
		}
	}
	return wrapped
}

// classifySentinel maps HTTP status onto an llm sentinel. See
// OPENROUTER-DETAILS.md for the rationale on each row.
func classifySentinel(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return llm.ErrAuth
	case status == http.StatusPaymentRequired:
		// 402 — insufficient credits. Auth-class: caller must fix something.
		return llm.ErrAuth
	case status == http.StatusTooManyRequests:
		return llm.ErrRateLimited
	case status >= 500 && status <= 599:
		return llm.ErrServerError
	case status == http.StatusRequestTimeout:
		return llm.ErrServerError
	case status >= 400 && status <= 499:
		return llm.ErrInvalidRequest
	default:
		return llm.ErrServerError
	}
}

// decodeErrorEnvelope tries to parse the OpenRouter error JSON. Returns nil
// on parse failure (so callers can fall back to the raw body).
func decodeErrorEnvelope(body []byte) *errorEnvelope {
	var env errorEnvelope
	if err := jsonUnmarshalLenient(body, &env); err != nil {
		return nil
	}
	if env.Error.Message == "" {
		return nil
	}
	return &env
}

// parseRetryAfter handles RFC 7231: integer seconds OR HTTP-date.
// Returns ok=true with a positive duration on success.
func parseRetryAfter(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return 0, false
		}
		return d, true
	}
	return 0, false
}
