# Anthropic Mapping (D5 / ADR-016) — GATE CLOSED 30.04.2026

Paper-only mapping exercise: walk every field of `llm.Request`, `llm.Message`, `llm.Event`, `llm.Usage` through "what would a direct `llm/anthropic` client send/receive on the wire?" The goal is to find places where the v1 surface forces lossiness or impossibility against the Anthropic Messages API, *before* W1 implementation locks the types.

**Status:** all surface changes derived from this exercise have landed in `v1/API.md`. The resolution checklist at the bottom is closed.

References (Anthropic Messages API):
- `POST https://api.anthropic.com/v1/messages`
- Authentication header `x-api-key`, version header `anthropic-version: 2023-06-01`
- Model strings like `claude-sonnet-4-5-20250929`, `claude-opus-4-5-20251101` (etc.).
- Beta headers for newer features: `anthropic-beta: prompt-caching-2024-07-31` (now GA), `anthropic-beta: extended-thinking-...`.

---

## Wire-shape diff at a glance

| Concept | OpenAI / OpenRouter (v1's reference shape) | Anthropic Messages API |
|---|---|---|
| System prompt | `messages[0]` with `role: "system"` | top-level `system` field (string OR array of content blocks) |
| Conversation roles | `system`, `user`, `assistant`, `tool` | `user`, `assistant` only (no `tool` role) |
| Tool result | `messages[].role: "tool"` with `tool_call_id`, `content` | `messages[].role: "user"` with content array containing `{type: "tool_result", tool_use_id, content, is_error?}` |
| Tool call | assistant `messages[].tool_calls: [{id, function: {name, arguments}}]` | assistant content block `{type: "tool_use", id, name, input}` |
| Image input | `messages[].content[]` with `{type: "image_url", image_url: {...}}` | `messages[].content[]` with `{type: "image", source: {type: "base64"\|"url", media_type, data\|url}}` |
| Streaming | OpenAI: `data: {choices: [{delta: {...}}]}`; OpenRouter mirrors | `event: <type>\ndata: {...}` SSE with named events: `message_start`, `content_block_start/delta/stop`, `message_delta`, `message_stop`, `ping` |
| Tool-call streaming | `choices[].delta.tool_calls[].function.arguments` arrives as fragments indexed by `index` | `content_block_delta` with `delta: {type: "input_json_delta", partial_json}` indexed by `content_block` index |
| Usage | `usage: {prompt_tokens, completion_tokens, total_tokens, cache_*}` | `usage: {input_tokens, output_tokens, cache_creation_input_tokens, cache_read_input_tokens}` |
| Cache control | OpenAI: none. OpenRouter forwards Anthropic's `cache_control` per the docs but silently ignores for non-Anthropic providers. | Per-block `cache_control: {type: "ephemeral", ttl?: "5m"\|"1h"}`. Up to 4 breakpoints per request. |
| Thinking config | OpenAI o1/o3: `reasoning_effort: "low"\|"medium"\|"high"` | `thinking: {type: "enabled", budget_tokens: N}` (extended-thinking models only) |
| Thinking output | OpenAI: hidden | Streamed as content blocks of type `thinking` with `delta: {type: "thinking_delta", thinking}` and a `signature_delta` for verifiable redaction |
| Stop reason | `finish_reason: "stop"\|"length"\|"tool_calls"\|"content_filter"` | `stop_reason: "end_turn"\|"max_tokens"\|"stop_sequence"\|"tool_use"\|"refusal"\|"pause_turn"` |

---

## Field-by-field walk

### `llm.Request`

| `llm.Request` field | OpenAI/OpenRouter wire | Anthropic wire | Gap analysis |
|---|---|---|---|
| `Model` | `model` | `model` | Direct map. Catalog must distinguish anthropic-direct IDs (`claude-sonnet-4-5-20250929`) from OpenRouter-routed IDs (`anthropic/claude-sonnet-4-6`). No v1 surface change needed; the mapping happens in `llm/anthropic` v2. |
| `Messages` | `messages` array | `messages` array (BUT `role: "system"` extracted to top-level; `role: "tool"` translated to user with tool_result blocks) | Translation logic is provider-side. v1 surface is OK as long as the `Message` shape can express what each provider needs. See `llm.Message` walk below. |
| `Tools` | `tools: [{type: "function", function: {name, description, parameters}}]` | `tools: [{name, description, input_schema}]` | Both work from `ToolDef{Name, Description, Parameters}`. Provider re-shapes. No v1 surface change. |
| `MaxTokens` | `max_completion_tokens` (or `max_tokens`) | `max_tokens` (REQUIRED — Anthropic errors if absent) | Direct. v1's W1 implementation must default to a non-zero value when the caller leaves it at zero (Anthropic 400s otherwise). |
| `Temperature` | `temperature` (0–2) | `temperature` (0–1) | Range mismatch. The provider client clamps; document. **Decision (revised 30.04.2026):** changed to `*float32`. `nil` = provider default; non-nil = explicit, clamped per provider range. Eliminates the `temperature: 0` zero-value foot-gun for deterministic-output callers. `llm.Float32(0.7)` helper for inline construction. |
| `ThinkingEffort` | `reasoning_effort` (`low`/`medium`/`high`) | `thinking: {type: "enabled", budget_tokens: N}` — explicit token budget | **GAP.** A string enum loses Anthropic's explicit-budget option. Power users who want to spend exactly 8000 thinking tokens on Sonnet can't. |
| `StopSequences` | `stop` | `stop_sequences` | Direct. |
| (none) | (none) | `top_k`, `top_p`, `metadata.user_id` | Provider-specific extras live on the `anthropic.Options` struct in v2, same way `openrouter.Options.ProviderOrder` lives on the OpenRouter Options. No v1 change. |

**Recommendation for `ThinkingEffort` (LANDED in v1/API.md as `Request.Thinking *ThinkingConfig` with constructor union):**

```go
// Typed enum, not raw string — typo-safe at call sites.
type ThinkingEffort string
const (
    ThinkingEffortLow    ThinkingEffort = "low"
    ThinkingEffortMedium ThinkingEffort = "medium"
    ThinkingEffortHigh   ThinkingEffort = "high"
)

// Unexported fields prevent callers from setting both Effort and Budget
// at once. Provider precedence is encoded in the constructor choice,
// not in per-field provider rules at the call site.
type ThinkingConfig struct {
    effort ThinkingEffort
    budget int
}

func ThinkingByEffort(e ThinkingEffort) *ThinkingConfig
func ThinkingByBudget(n int) *ThinkingConfig
```

Provider mapping (mostly unchanged from earlier draft):
- OpenAI / OpenRouter (OpenAI-shape): `ThinkingByEffort` → `reasoning_effort: <effort>`. `ThinkingByBudget` falls back to a derived effort.
- Anthropic direct: `ThinkingByBudget` → `thinking: {type: "enabled", budget_tokens: <budget>}`. `ThinkingByEffort` derives a budget (`low: 1024`, `medium: 8192`, `high: 32768`).

Backward path: callers that pass `nil` get the model's default reasoning behavior.

### `llm.Message`

| `llm.Message` field | OpenAI/OpenRouter wire | Anthropic wire | Gap analysis |
|---|---|---|---|
| `Role: RoleSystem` | `messages[].role: "system"` | top-level `system` field — extracted, not in `messages` | Provider client extracts. No v1 surface change. v1 callers keep using `RoleSystem` in the message array; the Anthropic client splices it out. |
| `Role: RoleUser` | `messages[].role: "user"` | `messages[].role: "user"` | Direct. |
| `Role: RoleAssistant` | `messages[].role: "assistant"` | `messages[].role: "assistant"` | Direct. |
| `Role: RoleTool` | `messages[].role: "tool"` with `tool_call_id`, `content` | `messages[].role: "user"` with content array containing `[{type: "tool_result", tool_use_id, content, is_error?}]` | Provider-side translation. v1 surface OK. Provider must batch consecutive `RoleTool` messages into a single Anthropic user message containing multiple `tool_result` blocks. |
| `Content string` | `content: string` (or array of content parts) | `content: string` for simple cases, else array of content blocks | Direct for plain text. |
| `Images []ImagePart` | `content[]: [{type: "image_url", image_url: {url}}]` | `content[]: [{type: "image", source: {type: "url"\|"base64", media_type, data\|url}}]` | Both work from `{URL, ContentType}` — for base64 data URIs, parse out the media type and bytes. v1 surface OK. |
| `ToolCalls []ToolCall` | `messages[].tool_calls: [{id, type: "function", function: {name, arguments}}]` | `content[]: [{type: "tool_use", id, name, input}]` (input is a parsed JSON object, not a JSON string) | Provider-side translation. **Note:** Anthropic's `input` is a parsed JSON object, OpenAI's `arguments` is a JSON string. v1's `ToolCall.Arguments string` is the OpenAI shape; the Anthropic client `json.Unmarshal`s before sending. No v1 surface change. |
| `ToolCallID string` | `messages[].tool_call_id` | inside the tool_result block: `tool_use_id` | Provider-side translation. v1 surface OK. |
| `CacheHint bool` | OpenRouter forwards Anthropic-style `cache_control` blocks; OpenAI ignores | per-block `cache_control: {type: "ephemeral", ttl?: "5m"\|"1h"}` | **GAP.** Boolean cannot express the TTL choice. Boolean on a `Message` cannot express *which content block* gets the breakpoint when the message has multiple parts (text + image, or text + tool_result). |

**Recommendation for `CacheHint` (LANDED in v1/API.md as `Message.Cache *CacheBreakpoint` with typed-string `CacheTTL` enum):**

```go
type CacheTTL string
const (
    CacheTTL5Min  CacheTTL = "5m"
    CacheTTL1Hour CacheTTL = "1h"
)

type CacheBreakpoint struct {
    TTL CacheTTL  // empty TTL = "5m" (provider convention)
}
```

Provider mapping:
- Anthropic direct: emit `cache_control: {type: "ephemeral", ttl: <TTL>}` on the *last* content block of the message. (Up to 4 breakpoints per request — provider client tracks budget and warns on overflow.)
- OpenRouter: forward as `cache_control` block per docs. May silently no-op for non-Anthropic-routed models — document.
- OpenAI direct: no-op. Document.

For multi-block messages where the breakpoint should land on a specific block, v1 is intentionally limited to "breakpoint on the last block." Multi-block-positioned breakpoints are a v1.x consideration if real demand surfaces.

### `llm.Event` and `EventKind`

| Event kind | OpenAI/OpenRouter wire | Anthropic wire | Gap analysis |
|---|---|---|---|
| `EventTextDelta` | `choices[].delta.content` | `content_block_delta` with `delta.type: "text_delta"` and `delta.text` | Direct. |
| `EventToolCall` (assembled) | Fragments under `choices[].delta.tool_calls[].function.arguments`, indexed; provider buffers and emits at `finish_reason` | `content_block_start` with `content_block.type: "tool_use"` (gives `id`, `name`), then `content_block_delta` with `delta.type: "input_json_delta", partial_json` fragments, then `content_block_stop` | Both providers fragment-stream. Anthropic uses content-block index; OpenAI uses tool-call array index. Both buffered identically client-side. v1 surface OK. |
| `EventUsage` | `usage` field on the final non-stream chunk (or `stream_options.include_usage`) | `message_start.usage` for input tokens; `message_delta.usage` for output tokens (cumulative) | Provider client aggregates. `Usage{PromptTokens, CompletionTokens, CacheReadTokens, CacheWriteTokens, CostUSD}` accommodates both. **Note:** Anthropic doesn't return `cost`; the v2 `llm/anthropic` client computes from the catalog. |
| `EventError` | HTTP-level (400/401/429/500) before stream; mid-stream errors are rare and signal-shape varies | HTTP-level before stream. Mid-stream: a `message_delta` may have `stop_reason: "refusal"` (handled as text) or the SSE connection drops without an error envelope. | Direct enough. Mid-stream "error" is a fuzzy concept for both providers. v1's "stream-start retry only" policy holds. |
| `EventEnd` | Stream terminates with `data: [DONE]` after the final chunk | Stream terminates with `event: message_stop\ndata: {...}` then connection closes | Direct. Provider client emits `EventEnd` with the appropriate `EndReason` derived from the stop reason. |
| (none) | (none — reasoning is hidden by OpenAI) | `content_block_start` with `content_block.type: "thinking"`, then `content_block_delta` with `delta.type: "thinking_delta", thinking` fragments, plus `signature_delta` | **GAP.** No `EventThinking` in v1 means the Anthropic client must silently discard `thinking_delta` events. Lossy for observability — users wanting to display reasoning traces (or just inspect them in dev) cannot. |

**Recommendation for thinking output (LANDED in v1/API.md as `EventThinking` on a string-typed `EventKind`):**

```go
type EventKind string

const (
    EventTextDelta EventKind = "text_delta"
    EventToolCall  EventKind = "tool_call"
    EventThinking  EventKind = "thinking"   // NEW — emitted as fragments arrive
    EventUsage     EventKind = "usage"
    EventError     EventKind = "error"
    EventEnd       EventKind = "end"
)
```

The `iota`-based enum was replaced with a typed-string enum. Reasons: order-independent additions are no longer breaking; new EventKind values can land in any sequence in v1.x; serializing the kind to logs is human-readable. The same change applies to `agent.EventKind`.

`Event.Text` carries the thinking-token text; consumers that don't care simply ignore `EventThinking`. For OpenAI providers that don't surface thinking, no `EventThinking` is ever emitted. Same shape as `EventToolCall` for non-tool models — silent absence is OK.

`Event` shape stays the same: `Text` carries thinking text. Optionally a `Thinking *ThinkingPart` if signature/redaction matters; punt that to v1.x.

### `llm.Usage`

| `Usage` field | OpenAI/OpenRouter wire | Anthropic wire | Gap analysis |
|---|---|---|---|
| `PromptTokens` | `usage.prompt_tokens` | `usage.input_tokens` | Direct rename. |
| `CompletionTokens` | `usage.completion_tokens` | `usage.output_tokens` | Direct rename. |
| `CacheReadTokens` | `usage.prompt_tokens_details.cached_tokens` (OpenAI) | `usage.cache_read_input_tokens` | Direct. |
| `CacheWriteTokens` | (not surfaced) | `usage.cache_creation_input_tokens` | Anthropic-only. OpenAI clients populate as 0. |
| `CostUSD` | OpenRouter returns `usage.cost` (float64) | not returned — provider client computes from catalog | Computed. v1 surface OK. |
| (none) | OpenAI: `usage.completion_tokens_details.reasoning_tokens` | (rolled into `output_tokens`) | **Minor gap.** Reasoning tokens are not separately metered in `Usage`. Already flagged as INDEX.md Q36 ("defer to v2"). Confirmed: defer; users wanting reasoning-only accounting can subtract using API-specific extras in v2. |

No new field needed in v1. `Usage` accommodates both providers as-is.

---

## Summary of v1 surface changes — all LANDED in API.md

Five surface changes folded into v1/API.md as the gate-closing PR (30.04.2026):

1. **`llm.Message.CacheHint bool`** → **`llm.Message.Cache *CacheBreakpoint`** with typed-string `CacheTTL` enum (`CacheTTL5Min`, `CacheTTL1Hour`). `nil` = no breakpoint; `&CacheBreakpoint{}` = default 5m.
2. **`llm.Request.ThinkingEffort string`** → **`llm.Request.Thinking *ThinkingConfig`** with constructor union `ThinkingByEffort(ThinkingEffort) / ThinkingByBudget(int)`. Effort values are typed (`ThinkingEffortLow/Medium/High`). Provider precedence is encoded in the constructor choice, not at the call site (unexported fields prevent both being set).
3. **Add `EventThinking` to `EventKind`.** Provider clients that have no concept of streaming-visible reasoning never emit it.
4. **`EventKind` (in both `llm` and `agent`) switched from `iota` to typed `string`.** Order-independent additions; clean log serialization.
5. **`llm.Request.Temperature float32`** → **`llm.Request.Temperature *float32`** with `llm.Float32` helper. Eliminates the `temperature: 0` zero-value foot-gun for deterministic-output callers.

No changes needed to:
- `Role` constants (Anthropic translation is provider-side).
- `ToolCall` shape (`Arguments string` works; Anthropic client `json.Unmarshal`s).
- `ImagePart` shape (`URL + ContentType` covers both providers).
- `Usage` shape (cache_creation_input_tokens reuses `CacheWriteTokens`).
- `Client` interface (`Stream` only).

---

## Open questions for the implementation pass

These don't block v1 freeze, but should be answered when `llm/anthropic` lands in v2:

- **Cache-breakpoint placement.** v1 lands the breakpoint on the last content block of the message. Anthropic's API supports placement on system blocks, tool-defs, and any individual content block. Multi-block positioning is a v1.x consideration; the `CacheBreakpoint` struct can grow an optional `BlockIndex int` additively without breaking v1 callers.
- **Thinking signature/redaction.** Anthropic emits `signature_delta` events for verifiable thinking redaction. v1's `EventThinking{Text string}` doesn't surface signatures. If users build verification flows on top, expose a `*ThinkingPart` with `{Text, Signature}` additively in v1.x.
- **Effort-to-budget mapping.** When Anthropic-direct receives `Effort: "medium"` with no explicit `Budget`, what budget should the client send? Recommendation: `low: 1024`, `medium: 8192`, `high: 32768`. Document in `llm/anthropic` godoc; tweak when actual usage data lands.
- **Multi-modal output.** Anthropic doesn't currently emit images in chat completions. If/when it does, both providers' image-output paths land additively as a new event variant (`EventImage` or similar). Out of scope for v1.
- **`pause_turn` stop reason.** Anthropic's `stop_reason: "pause_turn"` is for very-long agentic flows; emits a partial response that the caller resumes. v1 maps it to `EventEnd(EndReasonStop)` for now; revisit if real users hit it.
- **Beta headers and feature flags.** Anthropic's prompt caching graduated to GA but extended thinking is still gated behind a beta header on some models. The `llm/anthropic` v2 client owns the header tracking; v1's surface is unaffected.

---

## Resolution checklist — CLOSED 30.04.2026

All gate-closing changes landed in `v1/API.md`:

- [x] `Message.Cache *CacheBreakpoint` (replaces `CacheHint bool`); `CacheTTL` typed enum.
- [x] `Request.Thinking *ThinkingConfig` (replaces `ThinkingEffort string`); constructor union; `ThinkingEffort` typed enum.
- [x] `EventThinking` added to `EventKind` const block.
- [x] `EventKind` (in both `llm` and `agent`) switched to typed `string`.
- [x] `Request.Temperature *float32` (replaces value-type `float32`); `llm.Float32` helper.
- [x] `v1/API.md` updated; `// EXPERIMENTAL` markers removed.
- [x] `OPENROUTER-DETAILS.md` and `WAVES-EARLY.md` carry pre-grilling shapes in places — see the divergence banner at the top of each; authoritative shapes live in `API.md` and `ARCHITECTURE.md`.
- [x] `INDEX.md` Open Question 3 (silently discard reasoning tokens) marked resolved (now emitted via `EventThinking`).
- [x] `INDEX.md` Open Question 22 (`Temperature` zero-value foot-gun) marked resolved (now `*float32`).
