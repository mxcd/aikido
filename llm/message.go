package llm

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ImagePart struct {
	URL         string
	ContentType string
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
