package tools

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// Env carries per-call execution context to a Handler.
type Env struct {
	SessionID string
	TurnID    uuid.UUID
}

// Result is what a Handler returns. Content is JSON-serialized to the model.
type Result struct {
	Content any
	Display string
}

// Handler is the function signature every tool implements.
type Handler func(ctx context.Context, args json.RawMessage, env Env) (Result, error)
