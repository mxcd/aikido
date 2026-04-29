package llm

import "errors"

// Errors providers wrap with %w when mapping HTTP status to a typed cause.
var (
	ErrAuth           = errors.New("aikido/llm: authentication failed")
	ErrRateLimited    = errors.New("aikido/llm: rate limited")
	ErrServerError    = errors.New("aikido/llm: provider server error")
	ErrInvalidRequest = errors.New("aikido/llm: invalid request")
)
