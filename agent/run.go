package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/tools"
)

// Run executes one agent turn from a single user message.
//
// The user message and the assistant's response (including any tool calls and
// tool results produced this turn) are accumulated for one variadic
// History.Append at end of turn. The returned channel closes after EventEnd.
//
// On EndReasonError (including History I/O failure or Lock-acquire timeout)
// the History is NOT updated for this turn — the caller knows to retry, and
// the durable transcript stays consistent.
func (s *Session) Run(ctx context.Context, userText string) (<-chan Event, error) {
	out := make(chan Event, 16)
	go s.runLoop(ctx, out, userText, nil, true)
	return out, nil
}

// RunWithMessages is the lower-level escape hatch for callers that maintain
// their own history shape (trimming, branched conversations, agent-as-subroutine).
//
// The history slice is the conversation so far, excluding the system prompt
// (which Run prepends from SessionOptions.SystemPrompt). RunWithMessages does
// not append to History; the caller owns the message log here and uses Drain
// to assemble the produced messages.
func (s *Session) RunWithMessages(ctx context.Context, history []llm.Message) (<-chan Event, error) {
	out := make(chan Event, 16)
	go s.runLoop(ctx, out, "", history, false)
	return out, nil
}

func (s *Session) runLoop(userCtx context.Context, out chan<- Event, userText string, customHistory []llm.Message, useHistoryStore bool) {
	defer close(out)

	// Build runCtx with RunTimeout cap.
	runCtx := userCtx
	var cancelRun context.CancelFunc
	if s.opts.RunTimeout > 0 {
		runCtx, cancelRun = context.WithTimeout(userCtx, s.opts.RunTimeout)
		defer cancelRun()
	}

	// Acquire the per-session lock.
	unlock, err := s.opts.Locker.Lock(runCtx, s.opts.ID)
	if err != nil {
		s.emitErrEnd(out, userCtx, runCtx, nil, fmt.Errorf("aikido/agent: locker.Lock: %w", err))
		return
	}
	defer unlock()

	// Build the initial message list.
	var hist []llm.Message
	if useHistoryStore {
		hist, err = s.opts.History.Read(runCtx, s.opts.ID)
		if err != nil {
			s.emitErrEnd(out, userCtx, runCtx, nil, fmt.Errorf("aikido/agent: history.Read: %w", err))
			return
		}
	} else {
		hist = customHistory
	}

	msgs := make([]llm.Message, 0, len(hist)+2)
	if s.opts.SystemPrompt != "" {
		msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: s.opts.SystemPrompt})
	}
	msgs = append(msgs, hist...)

	var appended []llm.Message
	if useHistoryStore {
		userMsg := llm.Message{Role: llm.RoleUser, Content: userText}
		msgs = append(msgs, userMsg)
		appended = []llm.Message{userMsg}
	}

	var toolDefs []llm.ToolDef
	if s.opts.Tools != nil {
		toolDefs = s.opts.Tools.Defs()
	}

	for turn := 0; turn < s.opts.MaxTurns; turn++ {
		turnID := uuid.New()

		callCtx := runCtx
		var cancelCall context.CancelFunc = func() {}
		if s.opts.LLMCallTimeout > 0 {
			callCtx, cancelCall = context.WithTimeout(runCtx, s.opts.LLMCallTimeout)
		}

		req := llm.Request{
			Model:       s.opts.Model,
			Messages:    msgs,
			Tools:       toolDefs,
			MaxTokens:   s.opts.MaxTokens,
			Temperature: s.opts.Temperature,
		}

		events, streamErr := s.opts.Client.Stream(callCtx, req)
		if streamErr != nil {
			callErr := callCtx.Err()
			cancelCall()
			s.emitErrEnd(out, userCtx, runCtx, callErr, fmt.Errorf("aikido/agent: client.Stream: %w", streamErr))
			return
		}

		text, calls, usage, drainErr := s.drainProviderStream(callCtx, events, out)
		callErr := callCtx.Err()
		cancelCall()

		if drainErr != nil {
			s.emitErrEnd(out, userCtx, runCtx, callErr, drainErr)
			return
		}

		if usage != nil {
			out <- Event{Kind: EventUsage, Usage: usage}
		}

		assistantMsg := llm.Message{
			Role:      llm.RoleAssistant,
			Content:   text,
			ToolCalls: calls,
		}
		msgs = append(msgs, assistantMsg)
		appended = append(appended, assistantMsg)

		if len(calls) == 0 {
			if useHistoryStore {
				if err := s.opts.History.Append(runCtx, s.opts.ID, appended...); err != nil {
					s.emitErrEnd(out, userCtx, runCtx, nil, fmt.Errorf("aikido/agent: history.Append: %w", err))
					return
				}
			}
			out <- Event{Kind: EventEnd, EndReason: EndReasonStop}
			return
		}

		env := tools.Env{SessionID: s.opts.ID, TurnID: turnID}
		for i := range calls {
			call := calls[i]
			res, dispErr := s.dispatch(runCtx, call, env)

			tr := buildToolResult(call, res, dispErr)
			callCopy := call
			out <- Event{Kind: EventToolCall, ToolCall: &callCopy}
			out <- Event{Kind: EventToolResult, ToolResult: &tr}

			toolMsg := llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: call.ID,
				Content:    toolResultJSON(tr),
			}
			msgs = append(msgs, toolMsg)
			appended = append(appended, toolMsg)
		}
	}

	// MaxTurns exhausted: still flush History (the spec does so) and end.
	if useHistoryStore {
		if err := s.opts.History.Append(runCtx, s.opts.ID, appended...); err != nil {
			s.emitErrEnd(out, userCtx, runCtx, nil, fmt.Errorf("aikido/agent: history.Append: %w", err))
			return
		}
	}
	out <- Event{Kind: EventEnd, EndReason: EndReasonMaxTurns}
}

// dispatch routes a tool call. If the registry is nil we treat every tool name
// as unknown; the model self-corrects on the next turn.
func (s *Session) dispatch(ctx context.Context, call llm.ToolCall, env tools.Env) (tools.Result, error) {
	if s.opts.Tools == nil {
		return tools.Result{}, fmt.Errorf("%w: %s", tools.ErrUnknownTool, call.Name)
	}
	return s.opts.Tools.Dispatch(ctx, call, env)
}

// drainProviderStream consumes the provider's stream, forwarding TextDelta
// and Thinking events to the caller's channel and accumulating text, tool
// calls, and usage for the agent loop.
func (s *Session) drainProviderStream(ctx context.Context, events <-chan llm.Event, out chan<- Event) (text string, calls []llm.ToolCall, usage *llm.Usage, err error) {
	var sb strings.Builder
	for {
		select {
		case <-ctx.Done():
			return sb.String(), calls, usage, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return sb.String(), calls, usage, err
			}
			switch ev.Kind {
			case llm.EventTextDelta:
				sb.WriteString(ev.Text)
				out <- Event{Kind: EventText, Text: ev.Text}
			case llm.EventThinking:
				out <- Event{Kind: EventThinking, Text: ev.Text}
			case llm.EventToolCall:
				if ev.Tool != nil {
					calls = append(calls, *ev.Tool)
				}
			case llm.EventUsage:
				usage = ev.Usage
			case llm.EventError:
				if err == nil {
					err = ev.Err
				}
			case llm.EventEnd:
				// continue draining; the channel will close right after
			}
		}
	}
}

// emitErrEnd is the canonical "something went wrong" exit path. It emits
// EventError(err) followed by EventEnd with the right EndReason given the
// state of the various contexts.
func (s *Session) emitErrEnd(out chan<- Event, userCtx, runCtx context.Context, callErr error, err error) {
	out <- Event{Kind: EventError, Err: err}
	out <- Event{Kind: EventEnd, EndReason: resolveEndReason(userCtx, runCtx, callErr)}
}

// resolveEndReason maps the various ctx errors to the right EndReason.
//
// Order matters: caller cancellation always wins, then RunTimeout, then
// LLMCallTimeout, then everything else is EndReasonError.
func resolveEndReason(userCtx, runCtx context.Context, callErr error) string {
	if userCtx != nil {
		switch {
		case errors.Is(userCtx.Err(), context.Canceled):
			return EndReasonCancelled
		case errors.Is(userCtx.Err(), context.DeadlineExceeded):
			return EndReasonTimeout
		}
	}
	if runCtx != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return EndReasonTimeout
	}
	if errors.Is(callErr, context.DeadlineExceeded) {
		return EndReasonTimeout
	}
	if errors.Is(callErr, context.Canceled) {
		return EndReasonCancelled
	}
	return EndReasonError
}

func buildToolResult(call llm.ToolCall, res tools.Result, err error) ToolResult {
	tr := ToolResult{
		CallID: call.ID,
		Name:   call.Name,
		OK:     err == nil,
	}
	if err != nil {
		tr.Error = err.Error()
	} else {
		tr.Content = res.Content
	}
	return tr
}

// toolResultJSON serializes the tool result for the model's tool message.
func toolResultJSON(tr ToolResult) string {
	if !tr.OK {
		b, _ := json.Marshal(map[string]any{"error": tr.Error})
		return string(b)
	}
	if tr.Content == nil {
		return "null"
	}
	b, err := json.Marshal(tr.Content)
	if err != nil {
		fallback, _ := json.Marshal(map[string]any{"error": "content marshal failed: " + err.Error()})
		return string(fallback)
	}
	return string(b)
}
