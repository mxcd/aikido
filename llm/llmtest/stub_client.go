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

// Complete consumes one TurnScript and folds its events into a Response.
// EventError short-circuits with that error (matching live-client semantics).
// Useful for testing non-streaming callers without a separate scripting type:
// the same TurnScript drives both Stream and Complete.
func (s *StubClient) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	s.mu.Lock()
	if s.cursor >= len(s.turns) {
		s.mu.Unlock()
		return llm.Response{}, ErrStubExhausted
	}
	turn := s.turns[s.cursor]
	s.cursor++
	s.requests = append(s.requests, req)
	s.mu.Unlock()

	if turn.Block != nil {
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-turn.Block:
		}
	}

	var resp llm.Response
	for _, ev := range turn.Events {
		switch ev.Kind {
		case llm.EventTextDelta:
			resp.Text += ev.Text
		case llm.EventToolCall:
			if ev.Tool != nil {
				resp.ToolCalls = append(resp.ToolCalls, *ev.Tool)
			}
		case llm.EventImage:
			if ev.Image != nil {
				resp.Images = append(resp.Images, *ev.Image)
			}
		case llm.EventUsage:
			resp.Usage = ev.Usage
		case llm.EventError:
			return resp, ev.Err
		case llm.EventEnd:
			resp.FinishReason = ev.FinishReason
		}
	}
	return resp, nil
}
