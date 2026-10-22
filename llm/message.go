package llm

import "time"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ImagePart is one image attached to a message or returned by an image-capable
// model. URL is set when the provider returned a remote URL or when the caller
// supplied a data URI / remote URL on input. Data is set when the provider
// returned inline bytes (decoded from a data: URI) or when the caller wants to
// inline an image on output. ContentType is the MIME type when known
// ("image/png", "image/jpeg", ...) — empty when not provided by the wire.
type ImagePart struct {
	URL         string
	ContentType string
	Data        []byte
}

// AudioPart is one audio clip attached to a message. Data is the encoded clip
// as recorded; Format names its container the way OpenRouter's input_audio
// expects it ("wav", "mp3", "m4a", "webm", "ogg", "flac", "aiff"). AudioFormat
// derives Format from a MIME type.
type AudioPart struct {
	Data   []byte
	Format string
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Usage is the accounting and performance record of one model call.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
	CostUSD          float64

	// Provider is the upstream provider that served the call (OpenRouter's
	// "Anthropic", "Azure", ...). Empty when the client cannot tell.
	Provider string
	// Latency is the time from the call to its first output: the first
	// streamed token (text, reasoning or tool-call fragment) on Stream, the
	// whole response on Complete. Measured client-side, retries included.
	Latency time.Duration
	// Duration is the time from the call to its usage report (end of the
	// generation).
	Duration time.Duration
	// TokensPerSecond is CompletionTokens over the generation time: Duration
	// minus Latency on Stream, Duration on Complete. 0 when unmeasurable
	// (no completion tokens, or under 250 ms of generation).
	TokensPerSecond float64
}

// minRateWindow is the shortest generation time SetTiming derives a
// TokensPerSecond from.
const minRateWindow = 250 * time.Millisecond

// SetTiming fills Latency, Duration and TokensPerSecond from a call that
// started at start, produced its first output at firstOutput and ended at end.
// A zero firstOutput (non-streaming call, or no output seen) counts the whole
// call as latency and computes the rate over Duration.
func (u *Usage) SetTiming(start, firstOutput, end time.Time) {
	u.Duration = end.Sub(start)
	gen := end.Sub(firstOutput)
	if firstOutput.IsZero() {
		u.Latency, gen = u.Duration, u.Duration
	} else {
		u.Latency = firstOutput.Sub(start)
	}
	u.TokensPerSecond = 0
	// Providers that buffer (Bedrock) deliver short replies in a few large
	// chunks, so a short generation window measures chunking, not the model:
	// 83 tokens over 62 ms read as 1300 tok/s. Below 250 ms there is no rate.
	if gen >= minRateWindow && u.CompletionTokens > 0 {
		u.TokensPerSecond = float64(u.CompletionTokens) / gen.Seconds()
	}
}

type CacheTTL string

const (
	CacheTTL5Min  CacheTTL = "5m"
	CacheTTL1Hour CacheTTL = "1h"
)

// CacheBreakpoint marks a message as a cache breakpoint on providers that
// support it. nil = no breakpoint. &CacheBreakpoint{} = default 5m TTL.
type CacheBreakpoint struct {
	TTL CacheTTL
}

type Message struct {
	Role       Role
	Content    string
	Images     []ImagePart
	Audio      []AudioPart
	ToolCalls  []ToolCall
	ToolCallID string
	Cache      *CacheBreakpoint
}
