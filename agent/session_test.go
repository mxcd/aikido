package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/llmtest"
)

func TestNewSession_RequiredFields(t *testing.T) {
	stubClient := llmtest.NewStubClient()
	cases := []struct {
		name string
		opts *SessionOptions
	}{
		{"nil opts", nil},
		{"missing ID", &SessionOptions{Client: stubClient, Model: "m", History: noopHistory{}, Locker: NewLocalLocker()}},
		{"missing Client", &SessionOptions{ID: "s", Model: "m", History: noopHistory{}, Locker: NewLocalLocker()}},
		{"missing Model", &SessionOptions{ID: "s", Client: stubClient, History: noopHistory{}, Locker: NewLocalLocker()}},
		{"missing History", &SessionOptions{ID: "s", Client: stubClient, Model: "m", Locker: NewLocalLocker()}},
		{"missing Locker", &SessionOptions{ID: "s", Client: stubClient, Model: "m", History: noopHistory{}}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewSession(tt.opts); err == nil {
				t.Errorf("expected error for %s", tt.name)
			}
		})
	}
}

func TestNewSession_DefaultsApplied(t *testing.T) {
	s, err := NewSession(&SessionOptions{
		ID:      "s",
		Client:  llmtest.NewStubClient(),
		Model:   "m",
		History: noopHistory{},
		Locker:  NewLocalLocker(),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if s.opts.MaxTurns != 20 {
		t.Errorf("MaxTurns default = %d; want 20", s.opts.MaxTurns)
	}
	if s.opts.RunTimeout != 10*time.Minute {
		t.Errorf("RunTimeout default = %v; want 10m", s.opts.RunTimeout)
	}
	if s.opts.LLMCallTimeout != 180*time.Second {
		t.Errorf("LLMCallTimeout default = %v; want 180s", s.opts.LLMCallTimeout)
	}
	if s.opts.MaxTokens != 16384 {
		t.Errorf("MaxTokens default = %d; want 16384", s.opts.MaxTokens)
	}
	if s.opts.Logger == nil {
		t.Error("Logger should be set to a no-op default")
	}
}

func TestNewSession_NegativeTimeoutErrors(t *testing.T) {
	_, err := NewSession(&SessionOptions{
		ID: "s", Client: llmtest.NewStubClient(), Model: "m",
		History: noopHistory{}, Locker: NewLocalLocker(),
		RunTimeout: -1 * time.Second,
	})
	if err == nil {
		t.Error("expected error for negative RunTimeout")
	}
	_, err = NewSession(&SessionOptions{
		ID: "s", Client: llmtest.NewStubClient(), Model: "m",
		History: noopHistory{}, Locker: NewLocalLocker(),
		LLMCallTimeout: -1 * time.Second,
	})
	if err == nil {
		t.Error("expected error for negative LLMCallTimeout")
	}
}

// noopHistory is a History that never errors but stores nothing.
type noopHistory struct{}

func (noopHistory) Append(_ context.Context, _ string, _ ...llm.Message) error { return nil }
func (noopHistory) Read(_ context.Context, _ string) ([]llm.Message, error)    { return nil, nil }

// errorHistory always errors on the chosen op.
type errorHistory struct {
	errOnAppend error
	errOnRead   error
}

func (h errorHistory) Append(_ context.Context, _ string, _ ...llm.Message) error {
	return h.errOnAppend
}
func (h errorHistory) Read(_ context.Context, _ string) ([]llm.Message, error) {
	return nil, h.errOnRead
}

// errorLocker always returns an error.
type errorLocker struct{ err error }

func (e errorLocker) Lock(_ context.Context, _ string) (func(), error) { return nil, e.err }

// blockingClient blocks until ctx done.
type blockingClient struct{}

func (blockingClient) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	ch := make(chan llm.Event)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// errOnDial returns Stream's error before any goroutine starts.
type dialErrClient struct{ err error }

func (d dialErrClient) Stream(_ context.Context, _ llm.Request) (<-chan llm.Event, error) {
	return nil, d.err
}

// import needed by helpers above
var _ = errors.Is
