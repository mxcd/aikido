// Package memory provides an in-process agent.History backend.
//
// Suitable for single-replica deployments and tests; messages are lost when
// the process restarts.
package memory

import (
	"context"
	"sync"

	"github.com/mxcd/aikido/llm"
)

// History is the in-memory implementation.
type History struct {
	mu   sync.RWMutex
	data map[string][]llm.Message
}

// NewHistory returns an empty in-memory History.
func NewHistory() *History {
	return &History{data: make(map[string][]llm.Message)}
}

func (h *History) Append(ctx context.Context, sessionID string, msgs ...llm.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sessionID == "" {
		return ErrEmptySessionID
	}
	if len(msgs) == 0 {
		return nil
	}
	clone := make([]llm.Message, len(msgs))
	copy(clone, msgs)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.data[sessionID] = append(h.data[sessionID], clone...)
	return nil
}

func (h *History) Read(ctx context.Context, sessionID string) ([]llm.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sessionID == "" {
		return nil, ErrEmptySessionID
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	cur := h.data[sessionID]
	if cur == nil {
		return nil, nil
	}
	out := make([]llm.Message, len(cur))
	copy(out, cur)
	return out, nil
}
