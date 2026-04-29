package agent

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/tools"
)

// SessionOptions configure a Session.
type SessionOptions struct {
	ID           string
	Client       llm.Client
	Tools        *tools.Registry
	History      History
	Locker       Locker
	Model        string
	SystemPrompt string

	MaxTurns       int
	RunTimeout     time.Duration
	LLMCallTimeout time.Duration
	MaxTokens      int
	Temperature    *float32

	Logger *slog.Logger
}

// Session bundles a session ID, a model + system prompt, a tool registry, a
// History plug-in, and a Locker plug-in. Multiple Run calls share the
// session's history, serialized by the Locker.
type Session struct {
	opts SessionOptions
}

// NewSession constructs a Session. Returns an error if any required field
// (Client, History, Locker, Model, ID) is empty.
func NewSession(opts *SessionOptions) (*Session, error) {
	if opts == nil {
		return nil, errors.New("aikido/agent: nil SessionOptions")
	}
	if opts.ID == "" {
		return nil, errors.New("aikido/agent: SessionOptions.ID is required")
	}
	if opts.Client == nil {
		return nil, errors.New("aikido/agent: SessionOptions.Client is required")
	}
	if opts.Model == "" {
		return nil, errors.New("aikido/agent: SessionOptions.Model is required")
	}
	if opts.History == nil {
		return nil, errors.New("aikido/agent: SessionOptions.History is required (use agent/history/memory.NewHistory or NewLocalSession)")
	}
	if opts.Locker == nil {
		return nil, errors.New("aikido/agent: SessionOptions.Locker is required (use agent.NewLocalLocker or NewLocalSession)")
	}

	cp := *opts
	if cp.MaxTurns <= 0 {
		cp.MaxTurns = 20
	}
	if cp.RunTimeout < 0 {
		return nil, fmt.Errorf("aikido/agent: SessionOptions.RunTimeout must be >= 0, got %v", cp.RunTimeout)
	}
	if cp.RunTimeout == 0 {
		cp.RunTimeout = 10 * time.Minute
	}
	if cp.LLMCallTimeout < 0 {
		return nil, fmt.Errorf("aikido/agent: SessionOptions.LLMCallTimeout must be >= 0, got %v", cp.LLMCallTimeout)
	}
	if cp.LLMCallTimeout == 0 {
		cp.LLMCallTimeout = 180 * time.Second
	}
	if cp.MaxTokens == 0 {
		cp.MaxTokens = 16384
	}
	if cp.Logger == nil {
		cp.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Session{opts: cp}, nil
}
