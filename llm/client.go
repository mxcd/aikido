package llm

import "context"

type Request struct {
	Model         string
	Messages      []Message
	Tools         []ToolDef
	MaxTokens     int
	Temperature   *float32
	Thinking      *ThinkingConfig
	StopSequences []string
}

// EventKind identifies the kind of a streaming event.
type EventKind string

const (
	EventTextDelta EventKind = "text_delta"
	EventToolCall  EventKind = "tool_call"
	EventThinking  EventKind = "thinking"
	EventImage     EventKind = "image"
	EventUsage     EventKind = "usage"
	EventError     EventKind = "error"
	EventEnd       EventKind = "end"
)

// Event is one streaming event emitted by a Client. Channel closes after EventEnd.
type Event struct {
	Kind  EventKind
	Text  string
	Tool  *ToolCall
	Image *ImagePart
	Usage *Usage
	Err   error
}

type Client interface {
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}
