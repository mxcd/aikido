package agent

import (
	"context"

	"github.com/mxcd/aikido/llm"
)

// History is the pluggable conversation-history interface (ADR-014).
//
// SessionID is opaque to aikido; implementations use it as a key into whatever
// store the caller has. v1 ships an in-memory backend under
// agent/history/memory.
//
// Append is variadic so the agent can flush a turn's worth of messages (user,
// assistant, tool results) in a single backend round-trip.
type History interface {
	Append(ctx context.Context, sessionID string, msgs ...llm.Message) error
	Read(ctx context.Context, sessionID string) ([]llm.Message, error)
}
