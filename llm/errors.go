package llm

import "errors"

// Errors providers wrap with %w when mapping HTTP status to a typed cause.
var (
	ErrAuth           = errors.New("aikido/llm: authentication failed")
	ErrRateLimited    = errors.New("aikido/llm: rate limited")
	ErrServerError    = errors.New("aikido/llm: provider server error")
	ErrInvalidRequest = errors.New("aikido/llm: invalid request")

	// ErrContentFiltered is wrapped when the provider's safety policy aborts a
	// generation. Detected from finish_reason="content_filter" on a streamed
	// chunk OR a structured error envelope whose code/type signals content
	// filtering. Callers should NOT retry — the same prompt will trip the
	// classifier deterministically.
	//
	// Note: providers sometimes mid-stream RST without sending a structured
	// signal. Those failures still surface as ErrServerError; callers cannot
	// distinguish silent content aborts from genuine transient flake at the
	// wire level.
	ErrContentFiltered = errors.New("aikido/llm: content filtered by provider safety policy")
)
