package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/llmtest"
)

func TestRunAgent_RoundTrip(t *testing.T) {
	stub := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{
				ID: "c1", Name: "write_file",
				Arguments: `{"path":"hello.md","content":"hi there"}`,
			}},
			{Kind: llm.EventEnd},
		}},
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "wrote it"},
			{Kind: llm.EventEnd},
		}},
	)
	var out bytes.Buffer
	err := runAgent(context.Background(), agentOpts{
		model: "m", sessionID: "s1", prompt: "do the thing",
		maxTokens: 100, temperature: -1, maxTurns: 5,
	}, &out, stub)
	if err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "wrote it") {
		t.Errorf("missing assistant text: %q", got)
	}
	if !strings.Contains(got, "[tool-call] write_file") {
		t.Errorf("missing tool-call: %q", got)
	}
	if !strings.Contains(got, "[end] stop") {
		t.Errorf("missing end: %q", got)
	}
	if !strings.Contains(got, "hello.md") {
		t.Errorf("VFS state not printed: %q", got)
	}
}

func TestRunAgent_RequiresPrompt(t *testing.T) {
	stub := llmtest.NewStubClient()
	var out bytes.Buffer
	if err := runAgent(context.Background(), agentOpts{model: "m"}, &out, stub); err == nil {
		t.Error("expected error for missing prompt")
	}
}
