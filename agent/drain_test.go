package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/llmtest"
	"github.com/mxcd/aikido/tools"
)

func TestDrain_HappyPath_SingleTurn(t *testing.T) {
	ch := make(chan Event, 8)
	ch <- Event{Kind: EventText, Text: "Hello "}
	ch <- Event{Kind: EventText, Text: "world"}
	ch <- Event{Kind: EventEnd, EndReason: EndReasonStop}
	close(ch)

	msgs, err := Drain(ch)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d; want 1", len(msgs))
	}
	if msgs[0].Role != llm.RoleAssistant || msgs[0].Content != "Hello world" {
		t.Errorf("msgs[0] = %+v", msgs[0])
	}
}

func TestDrain_WithToolCallsAndResults(t *testing.T) {
	ch := make(chan Event, 16)
	ch <- Event{Kind: EventText, Text: "calling tools"}
	ch <- Event{Kind: EventToolCall, ToolCall: &llm.ToolCall{ID: "c1", Name: "x", Arguments: `{}`}}
	ch <- Event{Kind: EventToolCall, ToolCall: &llm.ToolCall{ID: "c2", Name: "y", Arguments: `{}`}}
	ch <- Event{Kind: EventToolResult, ToolResult: &ToolResult{CallID: "c1", Name: "x", OK: true, Content: map[string]any{"r": 1}}}
	ch <- Event{Kind: EventToolResult, ToolResult: &ToolResult{CallID: "c2", Name: "y", OK: false, Error: "oops"}}
	ch <- Event{Kind: EventText, Text: "done"}
	ch <- Event{Kind: EventEnd, EndReason: EndReasonStop}
	close(ch)

	msgs, err := Drain(ch)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	// Expect: assistant1(text+2 calls), tool(c1), tool(c2), assistant2(done)
	if len(msgs) != 4 {
		t.Fatalf("len(msgs) = %d; want 4. msgs=%+v", len(msgs), msgs)
	}
	if msgs[0].Role != llm.RoleAssistant || msgs[0].Content != "calling tools" || len(msgs[0].ToolCalls) != 2 {
		t.Errorf("msgs[0] = %+v", msgs[0])
	}
	if msgs[1].Role != llm.RoleTool || msgs[1].ToolCallID != "c1" {
		t.Errorf("msgs[1] = %+v", msgs[1])
	}
	if msgs[2].Role != llm.RoleTool || msgs[2].ToolCallID != "c2" || !strings.Contains(msgs[2].Content, "oops") {
		t.Errorf("msgs[2] = %+v", msgs[2])
	}
	if msgs[3].Role != llm.RoleAssistant || msgs[3].Content != "done" {
		t.Errorf("msgs[3] = %+v", msgs[3])
	}
}

func TestDrain_FirstError(t *testing.T) {
	ch := make(chan Event, 4)
	first := errors.New("first")
	second := errors.New("second")
	ch <- Event{Kind: EventError, Err: first}
	ch <- Event{Kind: EventError, Err: second}
	ch <- Event{Kind: EventEnd, EndReason: EndReasonError}
	close(ch)
	_, err := Drain(ch)
	if !errors.Is(err, first) {
		t.Errorf("err = %v; want %v", err, first)
	}
}

func TestRunWithMessages_DrainEndToEnd(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "answer"},
		{Kind: llm.EventEnd},
	}})
	s, err := NewLocalSession(&SessionOptions{
		ID: "rwm", Client: stub, Model: "m", SystemPrompt: "be brief",
	})
	if err != nil {
		t.Fatalf("NewLocalSession: %v", err)
	}
	hist := []llm.Message{{Role: llm.RoleUser, Content: "what is 2+2?"}}
	ch, _ := s.RunWithMessages(context.Background(), hist)
	produced, err := Drain(ch)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(produced) != 1 || produced[0].Role != llm.RoleAssistant || produced[0].Content != "answer" {
		t.Errorf("produced = %+v", produced)
	}
	// RunWithMessages must not write to History.
	stored, _ := s.opts.History.Read(context.Background(), "rwm")
	if len(stored) != 0 {
		t.Errorf("History should not be touched by RunWithMessages, got %+v", stored)
	}
}

func TestRunWithMessages_ToolDispatchOnPassedHistory(t *testing.T) {
	reg := tools.NewRegistry()
	_ = reg.Register(llm.ToolDef{Name: "ping"}, func(ctx context.Context, _ json.RawMessage, _ tools.Env) (tools.Result, error) {
		return tools.Result{Content: "pong"}, nil
	})
	stub := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "c1", Name: "ping"}},
			{Kind: llm.EventEnd},
		}},
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "pong received"},
			{Kind: llm.EventEnd},
		}},
	)
	s, _ := NewLocalSession(&SessionOptions{
		ID: "x", Client: stub, Model: "m", Tools: reg,
	})
	ch, _ := s.RunWithMessages(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "ping it"},
	})
	msgs, err := Drain(ch)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	// Expected: asst1 (with toolCall), tool(c1), asst2 (text)
	if len(msgs) != 3 {
		t.Fatalf("len = %d; want 3. msgs=%+v", len(msgs), msgs)
	}
}
