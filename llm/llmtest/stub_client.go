// Package llmtest provides public test helpers for the llm package.
//
// StubClient is a scriptable llm.Client. Each Stream call consumes one
// TurnScript; the agent loop tests script multi-turn conversations as a slice
// of TurnScripts.
package llmtest

import (
	"context"
	"errors"
	"sync"

	"github.com/mxcd/aikido/llm"
)

// ErrStubExhausted is returned by Stream when no scripts remain.
var ErrStubExhausted = errors.New("aikido/llmtest: stub client: script exhausted")

// TurnScript is the events the stub emits for one Stream call.
type TurnScript struct {
	Events []llm.Event
	// Block, if non-nil, causes Stream's producer to block on receive from this
	// channel before emitting any events. Closing it (or context cancellation)
	// unblocks the goroutine. Useful for timeout tests.
	Block <-chan struct{}
}

// StubClient is a scriptable llm.Client.
type StubClient struct {
	mu       sync.Mutex
	turns    []TurnScript
	cursor   int
	requests []llm.Request
}

var _ llm.Client = (*StubClient)(nil)

// NewStubClient returns a StubClient that plays the given turn scripts in order.
func NewStubClient(turns ...TurnScript) *StubClient {
	return &StubClient{turns: turns}
}

// Requests returns a snapshot of every llm.Request the stub has been called
// with, in order. Useful for assertion in tests.
func (s *StubClient) Requests() []llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]llm.Request, len(s.requests))
	copy(out, s.requests)
	return out
}

// Stream consumes one TurnScript and emits its events.
func (s *StubClient) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	s.mu.Lock()
	if s.cursor >= len(s.turns) {
		s.mu.Unlock()
		return nil, ErrStubExhausted
	}
	turn := s.turns[s.cursor]
	s.cursor++
	s.requests = append(s.requests, req)
	s.mu.Unlock()

	ch := make(chan llm.Event, len(turn.Events)+1)
	go func() {
		defer close(ch)
		if turn.Block != nil {
			select {
			case <-ctx.Done():
				return
			case <-turn.Block:
			}
		}
		for _, ev := range turn.Events {
			select {
			case <-ctx.Done():
				return
			case ch <- ev:
			}
		}
	}()
	return ch, nil
}
