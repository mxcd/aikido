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

	// Modalities lists the output modalities the caller wants the model to
	// emit. For image-capable models, set ["image", "text"] — otherwise the
	// model defaults to text-only and image generation silently fails or
	// returns a provider error. Empty leaves the provider default.
	Modalities []string

	// ImageConfig tunes image-output generation (aspect ratio, resolution).
	// Honored only by image-capable models. Nil leaves provider defaults
	// (typically 1:1 / 1K).
	ImageConfig *ImageConfig
}

// ImageConfig configures image-output generation. Forwarded as the
// `image_config` field on the OpenRouter chat-completions request. Non-image
// models ignore it.
//
// AspectRatio values supported across image models: "1:1" (default), "2:3",
// "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9". The extended
// ratios "1:4", "4:1", "1:8", "8:1" are honored only by
// google/gemini-3.1-flash-image-preview.
//
// ImageSize values: "1K" (default), "2K", "4K". "0.5K" is honored only by
// google/gemini-3.1-flash-image-preview.
//
// Empty fields are omitted from the wire body, letting the provider default
// apply per-field.
type ImageConfig struct {
	AspectRatio string
	ImageSize   string
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

	// FinishReason carries the provider's reported finish_reason on the chunk
	// that closes a generation. Set on EventEnd (and on the EventError that
	// stands in for finish_reason="content_filter"). Common values: "stop",
	// "tool_calls", "length", "content_filter", "error". Empty when the
	// provider didn't surface one.
	FinishReason string
}

// Response is the fully-assembled result of a non-streaming completion.
//
// Returned by Client.Complete in one shot — no event channel, no SSE framing.
// Use this for image generation and any other workload where progressive token
// delivery has no UX value: the SSE path imposes a per-line buffer cap that
// can't accommodate the multi-MB single-chunk responses image-capable models
// emit.
//
// FinishReason mirrors the provider's reported value ("stop", "tool_calls",
// "length", "content_filter", ...) — empty when the provider didn't surface one.
type Response struct {
	Text         string
	ToolCalls    []ToolCall
	Images       []ImagePart
	Usage        *Usage
	FinishReason string
}

type Client interface {
	Stream(ctx context.Context, req Request) (<-chan Event, error)

	// Complete sends a non-streaming completion request and returns the full
	// response in one shot. Implementations should map provider errors onto the
	// same Err* sentinels Stream uses (ErrAuth, ErrRateLimited, ErrServerError,
	// ErrInvalidRequest, ErrContentFiltered).
	//
	// Complete is the right choice for image generation: image-capable models
	// emit the entire base64 payload in a single chunk that frequently exceeds
	// the SSE per-line scanner cap, deterministically failing Stream-based
	// callers. Complete reads the response as a single JSON body and is
	// immune to that failure mode.
	Complete(ctx context.Context, req Request) (Response, error)
}
