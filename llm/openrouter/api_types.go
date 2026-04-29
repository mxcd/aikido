package openrouter

import (
	"encoding/json"

	"github.com/mxcd/aikido/llm"
)

// chatRequest is the OpenAI-compatible chat-completions request body.
//
// `omitempty` is load-bearing: zero-value fields the caller did not set must
// not appear on the wire. Pointer types are used where zero is a valid
// user-set value (notably Temperature: 0 for deterministic decoding).
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []apiMessage  `json:"messages"`
	Tools       []apiTool     `json:"tools,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature *float32      `json:"temperature,omitempty"`
	Stop        []string      `json:"stop,omitempty"`
	Reasoning   *apiReasoning `json:"reasoning,omitempty"`
	Provider    *apiProvider  `json:"provider,omitempty"`
}

// apiReasoning is the OpenRouter-normalized thinking-control block.
// OpenRouter accepts `effort` ("low"|"medium"|"high"|...) and forwards an
// equivalent directive to the routed provider. aikido maps `*ThinkingConfig`
// onto this; ThinkingByEffort sets `effort`, ThinkingByBudget is forwarded
// here as the OpenAI-style `effort` derived from a coarse budget bucket
// (per ADR-016).
type apiReasoning struct {
	Effort string `json:"effort,omitempty"`
}

// apiProvider configures OpenRouter's provider-routing block. Populated when
// Options.ProviderOrder is non-empty.
type apiProvider struct {
	Order          []string `json:"order,omitempty"`
	AllowFallbacks *bool    `json:"allow_fallbacks,omitempty"`
}

// apiMessage is the OpenAI-compatible message shape. Content is RawMessage so
// it can carry either a JSON string (text-only) or a JSON array (multimodal /
// cache_control-tagged content blocks).
type apiMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []apiToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

type apiToolCall struct {
	ID       string        `json:"id"`
	Type     string        `json:"type"` // "function"
	Function apiToolCallFn `json:"function"`
}

type apiToolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type apiTool struct {
	Type     string      `json:"type"` // "function"
	Function apiFunction `json:"function"`
}

type apiFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// --- streaming response shapes ---

type streamChunk struct {
	ID      string         `json:"id,omitempty"`
	Object  string         `json:"object,omitempty"`
	Choices []streamChoice `json:"choices,omitempty"`
	Usage   *apiUsage      `json:"usage,omitempty"`
	// Top-level error envelope on mid-stream errors.
	Error *apiError `json:"error,omitempty"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason,omitempty"`
}

type streamDelta struct {
	Role      string             `json:"role,omitempty"`
	Content   string             `json:"content,omitempty"`
	ToolCalls []toolCallFragment `json:"tool_calls,omitempty"`
	Reasoning string             `json:"reasoning,omitempty"`
}

type toolCallFragment struct {
	Index    int          `json:"index"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function functionFrag `json:"function,omitempty"`
}

type functionFrag struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type apiUsage struct {
	PromptTokens            int               `json:"prompt_tokens"`
	CompletionTokens        int               `json:"completion_tokens"`
	TotalTokens             int               `json:"total_tokens"`
	PromptTokensDetails     promptDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails completionDetails `json:"completion_tokens_details,omitempty"`
	Cost                    float64           `json:"cost,omitempty"`
	// Legacy field shapes (pre-2026 cache renaming) — accepted for resilience.
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

type promptDetails struct {
	CachedTokens     int `json:"cached_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	AudioTokens      int `json:"audio_tokens,omitempty"`
	// Legacy alternates.
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

type completionDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

type apiError struct {
	// `code` may be either a JSON string (provider-defined symbol) or an int
	// (HTTP status mirror). Decode permissively.
	Code     json.RawMessage `json:"code,omitempty"`
	Message  string          `json:"message,omitempty"`
	Metadata map[string]any  `json:"metadata,omitempty"`
}

// errorEnvelope is the JSON envelope OpenRouter wraps non-200 responses in.
type errorEnvelope struct {
	Error apiError `json:"error"`
}

// toLLMUsage maps the wire usage onto the locked llm.Usage shape.
func toLLMUsage(u *apiUsage) *llm.Usage {
	if u == nil {
		return nil
	}
	cacheRead := u.PromptTokensDetails.CachedTokens
	if cacheRead == 0 {
		cacheRead = u.PromptTokensDetails.CacheReadInputTokens
	}
	if cacheRead == 0 {
		cacheRead = u.CacheReadInputTokens
	}
	cacheWrite := u.PromptTokensDetails.CacheWriteTokens
	if cacheWrite == 0 {
		cacheWrite = u.PromptTokensDetails.CacheCreationInputTokens
	}
	if cacheWrite == 0 {
		cacheWrite = u.CacheCreationInputTokens
	}
	return &llm.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		CostUSD:          u.Cost,
	}
}

// effortFromConfig collapses a *ThinkingConfig onto the OpenAI-shape effort
// string OpenRouter accepts. Empty string means "no thinking" — caller omits
// the reasoning block entirely.
//
// For ThinkingByEffort the value passes through verbatim.
//
// For ThinkingByBudget the budget is bucketed onto coarse effort (per
// ADR-016): <2048 → low, <16384 → medium, ≥16384 → high. Direct Anthropic
// (v2) honors budget verbatim; here we lose precision, which is fine because
// OpenRouter maps "effort" to an internal budget anyway.
func effortFromConfig(t *llm.ThinkingConfig) string {
	if t == nil {
		return ""
	}
	if e := t.Effort(); e != "" {
		return string(e)
	}
	b := t.Budget()
	switch {
	case b <= 0:
		return ""
	case b < 2048:
		return string(llm.ThinkingEffortLow)
	case b < 16384:
		return string(llm.ThinkingEffortMedium)
	default:
		return string(llm.ThinkingEffortHigh)
	}
}

// buildAPIMessages converts public llm.Messages onto the wire shape.
func buildAPIMessages(in []llm.Message) ([]apiMessage, error) {
	out := make([]apiMessage, 0, len(in))
	for _, m := range in {
		am := apiMessage{Role: string(m.Role)}

		// tool-role messages must carry tool_call_id.
		if m.Role == llm.RoleTool {
			am.ToolCallID = m.ToolCallID
		}

		// assistant tool calls.
		if len(m.ToolCalls) > 0 {
			am.ToolCalls = make([]apiToolCall, 0, len(m.ToolCalls))
			for _, c := range m.ToolCalls {
				am.ToolCalls = append(am.ToolCalls, apiToolCall{
					ID:   c.ID,
					Type: "function",
					Function: apiToolCallFn{
						Name:      c.Name,
						Arguments: c.Arguments,
					},
				})
			}
		}

		// Build content. Three shapes:
		//   1. plain string  — text-only, no images, no cache breakpoint.
		//   2. array of typed parts — multimodal or cache breakpoint.
		//   3. empty string ("") — assistant with only tool calls.
		needArray := len(m.Images) > 0 || m.Cache != nil
		switch {
		case needArray:
			parts, err := buildContentParts(m)
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(parts)
			if err != nil {
				return nil, err
			}
			am.Content = raw
		case m.Content == "" && len(m.ToolCalls) > 0:
			// Assistant message with only tool calls. Empty string per
			// OPENROUTER-DETAILS spec (more stable than null on the wire).
			am.Content = json.RawMessage(`""`)
		default:
			raw, err := json.Marshal(m.Content)
			if err != nil {
				return nil, err
			}
			am.Content = raw
		}

		out = append(out, am)
	}
	return out, nil
}

// buildContentParts builds the typed-parts array for a message with images or
// a cache breakpoint. The cache_control directive lands on the last part.
func buildContentParts(m llm.Message) ([]map[string]any, error) {
	parts := make([]map[string]any, 0, 1+len(m.Images))
	if m.Content != "" {
		parts = append(parts, map[string]any{
			"type": "text",
			"text": m.Content,
		})
	}
	for _, img := range m.Images {
		parts = append(parts, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": img.URL,
			},
		})
	}
	if len(parts) == 0 {
		// Edge case: a Cache breakpoint on an otherwise-empty message. Emit a
		// stub text part so the breakpoint has somewhere to live.
		parts = append(parts, map[string]any{
			"type": "text",
			"text": "",
		})
	}
	if m.Cache != nil {
		cc := map[string]any{"type": "ephemeral"}
		if m.Cache.TTL == llm.CacheTTL1Hour {
			cc["ttl"] = "1h"
		}
		parts[len(parts)-1]["cache_control"] = cc
	}
	return parts, nil
}

// buildAPITools converts ToolDefs onto the wire shape.
func buildAPITools(defs []llm.ToolDef) []apiTool {
	if len(defs) == 0 {
		return nil
	}
	out := make([]apiTool, 0, len(defs))
	for _, d := range defs {
		out = append(out, apiTool{
			Type: "function",
			Function: apiFunction{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  d.Parameters,
			},
		})
	}
	return out
}
