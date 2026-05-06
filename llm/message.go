package llm

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

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
	CostUSD          float64
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
	ToolCalls  []ToolCall
	ToolCallID string
	Cache      *CacheBreakpoint
}
