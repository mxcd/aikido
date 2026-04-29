# OpenRouter Wire-Format Reference

> ⚠️ **Divergence banner (30.04.2026).** Wire-format content (endpoints, SSE shape, retry headers, tool-call fragment assembly, canned-transcript payloads) remains accurate. Mappings between wire fields and `llm.*` types reflect *pre-second-grilling* shapes in places. Authoritative `llm` types live in `../API.md`. Specifically: `Message.CacheHint bool` → `Message.Cache *CacheBreakpoint` with `CacheTTL`; `Request.ThinkingEffort string` → `Request.Thinking *ThinkingConfig` with constructor union; `Request.Temperature float32` → `*float32`; `EventKind` is typed `string` with `EventThinking` added. The W2/W3 split is collapsed in the post-grilling plan into a single new W2; this file's W2 vs W3 organization is logically still useful but the wave numbers are off by one.

Everything `llm/openrouter` needs to know about the upstream API. Scope is the merged W2 (true streaming + tool-call assembly + retry — formerly two waves). Public types in `llm` are locked by [API.md](../API.md); this file documents only the wire shapes that map onto them.

Sources cited inline. Live OpenRouter docs were fetched 29.04.2026; some doc pages have moved since the original PLAN was written, which is why the canonical URLs below differ from the placeholder ones in PLAN.md.

- Chat completions: <https://openrouter.ai/docs/api/api-reference/chat/send-chat-completion-request>
- Streaming: <https://openrouter.ai/docs/api/reference/streaming>
- Tool calling: <https://openrouter.ai/docs/guides/features/tool-calling>
- Prompt caching: <https://openrouter.ai/docs/guides/best-practices/prompt-caching>
- Errors and debugging: <https://openrouter.ai/docs/api/reference/errors-and-debugging>
- Usage accounting: <https://openrouter.ai/docs/guides/administration/usage-accounting>
- Rate limits: <https://openrouter.ai/docs/api/reference/limits>
- API overview: <https://openrouter.ai/docs/api/reference/overview>

DeepThought patterns cited:

- `~/Nextcloud/DeepThought/Patterns/Streaming-Tool-Call-Assembly-Pattern.md`
- `~/Nextcloud/DeepThought/Patterns/OpenRouter-Model-ID-Hyphen-Normalization-Gotcha.md`
- `~/Nextcloud/DeepThought/Patterns/OpenRouter-Model-ID-Naming-Convention.md`
- `~/Nextcloud/DeepThought/Patterns/Anthropic-Prompt-Caching-Cost-Control.md`
- `~/Nextcloud/DeepThought/Patterns/Pluggable-LLM-Client-Interface-Pattern.md`
- `~/Nextcloud/DeepThought/Patterns/Backend/OpenRouter-SSE-Streaming-Retry-Semantics-Pattern.md`
- `~/Nextcloud/DeepThought/Patterns/Agent/OpenRouter-Multimodal-Response-Parsing-Pattern.md`
- `~/Nextcloud/DeepThought/References/OpenRouter-Sonnet-Timeout-Tuning.md`
- `~/Nextcloud/DeepThought/Patterns/Agent/Prompt-Caching-for-Agent-Loop-Cost-Reduction.md`

Reference Go code:

- `/Users/mapa/github.com/asolabs/hub/internal/client/openrouter/client.go`
- `/Users/mapa/github.com/asolabs/hub/sandbox/agent-test/agent/openrouter.go`

---

## Endpoints

aikido v1 calls exactly one endpoint.

| Endpoint                       | Method | Purpose                                                                  |
| ------------------------------ | ------ | ------------------------------------------------------------------------ |
| `{base}/chat/completions`      | POST   | Chat completions (text + tools, streaming or non-streaming).             |

`base` is `https://openrouter.ai/api/v1` by default; configurable via `Options.BaseURL` (see [API.md](../API.md)). All other OpenRouter endpoints (`/models`, `/generation`, `/key`, `/credits`) are out of scope for v1: aikido seeds a static catalog (W1) rather than fetching `/models` dynamically.

Source: <https://openrouter.ai/docs/api/reference/overview>.

---

## Request shape

### Headers

```
Authorization: Bearer {APIKey}
Content-Type: application/json
HTTP-Referer: {Options.HTTPReferer}   # optional, ranking attribution
X-Title:      {Options.XTitle}        # optional, ranking attribution
```

`HTTP-Referer` and `X-Title` are OpenRouter-ranking signals — purely cosmetic; OpenRouter omits them with no functional consequence. aikido sends them only when set on `Options`.

For streaming, do NOT need to send `Accept: text/event-stream` — OpenRouter switches based on the request-body `stream: true` flag. The reference clients in `asolabs/hub` send it anyway as good hygiene; aikido should do the same since it is harmless and clarifies intent on the wire.

Source: <https://openrouter.ai/docs/api/reference/overview>; verified against `/Users/mapa/github.com/asolabs/hub/internal/client/openrouter/client.go`.

### Body — fields aikido sends

aikido's `llm.Request` maps onto an OpenAI-compatible body. Other request fields exist (`top_p`, `top_k`, `frequency_penalty`, `presence_penalty`, `seed`, `response_format`, `provider`, `plugins`, ...) but v1 does not set any of them — they are out of scope per [PLAN.md](../PLAN.md). The implementation MUST NOT serialize zero-value fields the user did not set; use `,omitempty` plus pointer types where zero is meaningful (e.g. `temperature: 0` is a valid value the user might choose).

```go
// llm/openrouter/api_types.go (unexported)

type chatRequest struct {
    Model         string         `json:"model"`
    Messages      []apiMessage   `json:"messages"`
    Tools         []apiTool      `json:"tools,omitempty"`
    Stream        bool           `json:"stream,omitempty"`
    MaxTokens     int            `json:"max_tokens,omitempty"`
    Temperature   *float32       `json:"temperature,omitempty"`
    Stop          []string       `json:"stop,omitempty"`
    Reasoning     *apiReasoning  `json:"reasoning,omitempty"`
    // Note: no stream_options, no usage:{include:true} —
    // those are deprecated as of 2026 and have no effect.
}

type apiReasoning struct {
    // OpenRouter normalizes ThinkingEffort across providers
    Effort string `json:"effort,omitempty"` // "low" | "medium" | "high"
}
```

`stream` is `true` for both W2 (non-streaming-from-the-caller's-POV but reading the SSE channel under the hood per ADR-006) and W3.

`Reasoning.Effort` exists upstream as `"xhigh" | "high" | "medium" | "low" | "minimal" | "none"` (see chat-completions doc). aikido's `Request.ThinkingEffort` is `"" | "low" | "medium" | "high"` per [API.md](../API.md). Pass through verbatim when non-empty; map `""` to omitting the `reasoning` block entirely.

Source: <https://openrouter.ai/docs/api/api-reference/chat/send-chat-completion-request>.

### messages[]

OpenRouter accepts the OpenAI-compatible message shape with role-based discrimination. aikido emits four roles: `system`, `user`, `assistant`, `tool`. The relevant per-role fields:

```go
type apiMessage struct {
    Role       string             `json:"role"`
    // Content is either a plain string or an array of content parts.
    // Use json.RawMessage to defer the choice to runtime.
    Content    json.RawMessage    `json:"content"`
    ToolCalls  []apiToolCall      `json:"tool_calls,omitempty"`  // assistant only
    ToolCallID string             `json:"tool_call_id,omitempty"` // tool only
    Name       string             `json:"name,omitempty"`         // optional
}
```

When converting `llm.Message` → `apiMessage`:

- **system / user** with no images: `Content` is a JSON string. Wire: `"content": "Hello"`.
- **user with images**: `Content` is a JSON array of content parts. Wire shown below.
- **assistant with text only**: `Content` is a JSON string.
- **assistant with tool calls**: `Content` may be an empty string (`""`) or `null`; OpenRouter accepts both. aikido emits `""` for stability — `null` requires a pointer and complicates marshal logic. The `tool_calls` array carries the calls.
- **tool**: `Content` is a JSON string carrying the tool result body. `ToolCallID` is required and must match the assistant's `ToolCall.ID` it replies to.

Source: <https://openrouter.ai/docs/api/api-reference/chat/send-chat-completion-request>.

### Multimodal content (images)

Per [API.md](../API.md), `llm.Message.Images []ImagePart` is the only multimodal hook in v1. When `len(Images) > 0`, the message's `content` becomes a JSON array of typed parts:

```json
{
  "role": "user",
  "content": [
    {"type": "text", "text": "What is in this image?"},
    {"type": "image_url", "image_url": {"url": "https://example.com/cat.png"}}
  ]
}
```

For data-URI images (when constructing from raw bytes):

```json
{
  "type": "image_url",
  "image_url": {"url": "data:image/png;base64,iVBORw0KGgoAAAANS..."}
}
```

`ImagePart.URL` carries either a remote URL or a data URI. `ImagePart.ContentType` is informational only on the v1 send path — OpenRouter parses the data URI itself.

Note: `image_url.detail` (`"auto"|"low"|"high"`) is supported upstream for OpenAI vision models but aikido does NOT expose it in v1. See "Open questions" below if we need to add a control later.

The reference parsing for *response*-side image content (image-gen) lives in `~/Nextcloud/DeepThought/Patterns/Agent/OpenRouter-Multimodal-Response-Parsing-Pattern.md` but is out of v1 scope (image gen is v2 per ADR-002).

Source: <https://openrouter.ai/docs/api/api-reference/chat/send-chat-completion-request>; verified against `/Users/mapa/github.com/asolabs/hub/internal/client/openrouter/client.go` (`extractImageDataURI`).

### tools[]

```json
{
  "type": "function",
  "function": {
    "name": "get_weather",
    "description": "Get current weather for a location.",
    "parameters": {
      "type": "object",
      "properties": {
        "location": {"type": "string", "description": "City name"}
      },
      "required": ["location"]
    }
  }
}
```

`function.parameters` is a JSON Schema. aikido's `llm.ToolDef.Parameters json.RawMessage` is forwarded verbatim — the contract is "whatever the caller produced via `tools.Object/...` or `tools.SchemaFromType`". OpenRouter does not validate the schema beyond shape; an invalid schema typically surfaces as a 400 from the upstream provider.

Optional `function.strict: true` exists for OpenAI structured-outputs mode; aikido does not set it in v1.

Source: <https://openrouter.ai/docs/guides/features/tool-calling>.

### tool_choice

Not set in v1. The default `"auto"` is correct — aikido lets the model decide whether to call tools.

Source: <https://openrouter.ai/docs/guides/features/tool-calling>.

### parallel_tool_calls

Not set in v1. OpenRouter's default is `true`. aikido's tool-call assembly handles N tool calls per assistant turn unconditionally (see "Tool-call fragmentation" below), so the default is fine.

Source: <https://openrouter.ai/docs/guides/features/tool-calling>.

### Stream flag

`"stream": true` always (see ADR-006). Even for non-streaming consumers using `llm.Collect`, aikido reads the SSE channel and assembles the final result client-side.

`stream_options` and the legacy top-level `usage: {include: true}` request flag are **deprecated as of 2026** — usage is always returned. aikido MUST NOT send either.

Source: <https://openrouter.ai/docs/guides/administration/usage-accounting> ("The `usage: { include: true }` and `stream_options: { include_usage: true }` parameters are deprecated and have no effect.").

### cache_control passthrough

Per [API.md](../API.md), `llm.Message.CacheHint bool` is the only caching surface in v1. When `true`, aikido attaches `cache_control: {"type": "ephemeral"}` to the **last content block of that message**.

Two cases:

- Plain-text message (`Content` is a string): aikido must convert to the array form to attach `cache_control`. The string becomes a single `{"type": "text", "text": "...", "cache_control": {"type": "ephemeral"}}` block.
- Multi-block message: attach `cache_control` to the final block in the array.

```json
{
  "role": "system",
  "content": [
    {
      "type": "text",
      "text": "You are an opinionated documentation engineer. ...",
      "cache_control": {"type": "ephemeral"}
    }
  ]
}
```

The 1-hour TTL variant (`{"type": "ephemeral", "ttl": "1h"}`) is supported upstream but NOT exposed in v1; the default 5-minute TTL fits the agent-loop turn cadence (per `~/Nextcloud/DeepThought/Patterns/Agent/Prompt-Caching-for-Agent-Loop-Cost-Reduction.md` — turns are tightly coupled, persistent caching has marginal extra value, and the cost overhead is real).

See "Cache control behavior" below for what each provider does with the directive.

Source: <https://openrouter.ai/docs/guides/best-practices/prompt-caching>.

### Full request example (system + user + tools, stream)

```json
{
  "model": "anthropic/claude-sonnet-4-6",
  "stream": true,
  "max_tokens": 8192,
  "messages": [
    {
      "role": "system",
      "content": [
        {
          "type": "text",
          "text": "You are a careful coding assistant.",
          "cache_control": {"type": "ephemeral"}
        }
      ]
    },
    {"role": "user", "content": "List files in the project."}
  ],
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "list_files",
        "description": "List files in the project VFS.",
        "parameters": {
          "type": "object",
          "properties": {},
          "required": []
        }
      }
    }
  ]
}
```

---

## Streaming response (SSE)

`POST /chat/completions` with `stream: true` returns `Content-Type: text/event-stream`. The body is a sequence of SSE events terminated by `data: [DONE]`.

### Line format

The wire is line-based UTF-8. aikido's parser must handle:

- `data: {json}\n\n` — a normal event. The `data: ` prefix is followed by exactly one JSON object.
- `: comment\n` — a comment line (single colon prefix). OpenRouter sends `: OPENROUTER PROCESSING` periodically as a TCP-keepalive substitute; ignore these. They have no `\n\n` separator semantics — treat them as no-ops.
- `data: [DONE]\n\n` — terminator. After this line, no more useful payloads will arrive; close the stream.
- Empty lines (`\n`) — separators between events. Ignore.

The parser also tolerates `\r\n` line endings; servers occasionally use them. Implementation: `bufio.Scanner` with a `ScanLines` split function strips both `\r` and `\n` automatically.

Source: <https://openrouter.ai/docs/api/reference/streaming> ("Server-Sent Events (SSE) are supported... The SSE stream will occasionally contain a 'comment' payload, which you should ignore."); also `~/Nextcloud/DeepThought/Patterns/Backend/OpenRouter-SSE-Streaming-Retry-Semantics-Pattern.md`.

### Chunk shape

Each `data:` payload after JSON-decode is a `chat.completion.chunk` object:

```json
{
  "id": "gen-abc123",
  "object": "chat.completion.chunk",
  "created": 1714402356,
  "model": "anthropic/claude-sonnet-4-6",
  "choices": [
    {
      "index": 0,
      "delta": {
        "role": "assistant",
        "content": ""
      },
      "finish_reason": null
    }
  ]
}
```

aikido decodes only the fields it cares about (parser is permissive on unknown fields, per [PLAN.md](../PLAN.md) W2 risks):

```go
// llm/openrouter/api_types.go (unexported)

type streamChunk struct {
    ID      string         `json:"id,omitempty"`
    Choices []streamChoice `json:"choices,omitempty"`
    Usage   *apiUsage      `json:"usage,omitempty"`
    // Top-level error envelope on mid-stream errors.
    Error   *apiError      `json:"error,omitempty"`
}

type streamChoice struct {
    Index        int        `json:"index"`
    Delta        streamDelta `json:"delta"`
    FinishReason *string    `json:"finish_reason,omitempty"`
}

type streamDelta struct {
    Role      string             `json:"role,omitempty"`
    Content   string             `json:"content,omitempty"`
    ToolCalls []toolCallFragment `json:"tool_calls,omitempty"`
    Reasoning string             `json:"reasoning,omitempty"` // optional, for thinking models
}

type toolCallFragment struct {
    Index    int          `json:"index"`
    ID       string       `json:"id,omitempty"`
    Type     string       `json:"type,omitempty"` // "function"
    Function functionFrag `json:"function,omitempty"`
}

type functionFrag struct {
    Name      string `json:"name,omitempty"`
    Arguments string `json:"arguments,omitempty"`
}

type apiUsage struct {
    PromptTokens         int          `json:"prompt_tokens"`
    CompletionTokens     int          `json:"completion_tokens"`
    TotalTokens          int          `json:"total_tokens"`
    PromptTokensDetails  promptDetails  `json:"prompt_tokens_details,omitempty"`
    CompletionTokensDetails completionDetails `json:"completion_tokens_details,omitempty"`
    Cost                 float64      `json:"cost,omitempty"`
}

type promptDetails struct {
    CachedTokens     int `json:"cached_tokens,omitempty"`
    CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
    AudioTokens      int `json:"audio_tokens,omitempty"`
}

type completionDetails struct {
    ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

type apiError struct {
    Code     int                    `json:"code,omitempty"`
    Message  string                 `json:"message,omitempty"`
    Metadata map[string]any         `json:"metadata,omitempty"`
}
```

### Concrete delta examples

**1. First chunk (role only):**

```
data: {"id":"gen-1","object":"chat.completion.chunk","created":1714402356,"model":"anthropic/claude-sonnet-4-6","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}
```

**2. Text content chunk:**

```
data: {"id":"gen-1","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"gen-1","choices":[{"index":0,"delta":{"content":", world"},"finish_reason":null}]}
```

**3. Final chunk with `finish_reason` (no usage yet):**

```
data: {"id":"gen-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}
```

**4. Usage chunk (last data: before `[DONE]`):**

```
data: {"id":"gen-1","choices":[],"usage":{"prompt_tokens":42,"completion_tokens":7,"total_tokens":49,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"completion_tokens_details":{"reasoning_tokens":0},"cost":0.000147}}
```

**5. Terminator:**

```
data: [DONE]
```

The usage chunk and the final-content chunk may be the same object on some providers (usage attached to the chunk that carries `finish_reason`). aikido's parser must:

- emit `EventUsage` whenever it sees a non-nil `usage` field on a chunk;
- emit it before `EventEnd`;
- not assume usage and `finish_reason` arrive together.

Source: <https://openrouter.ai/docs/guides/administration/usage-accounting> ("Usage information is included in the last SSE message for streaming responses").

### Comment line example

```
: OPENROUTER PROCESSING

data: {"id":"gen-1","choices":[{"index":0,"delta":{"content":"more"}}]}
```

The comment carries no JSON; ignore it. It exists to keep idle TCP connections open while the upstream provider is still reasoning.

Source: <https://openrouter.ai/docs/api/reference/streaming>.

### finish_reason values

Per OpenRouter's normalization across models:

| Value             | Semantics                                         | aikido handling                          |
| ----------------- | ------------------------------------------------- | ---------------------------------------- |
| `"stop"`          | Natural end of turn. Model has nothing more.      | `EventEnd`, no follow-up turn.           |
| `"tool_calls"`    | Model emitted tool calls and wants results.       | Emit assembled tool calls, then `EventEnd`. The agent loop follows up. |
| `"length"`        | Hit `max_tokens`. Output is truncated.            | Treat as `stop` for v1 — the agent's `MaxTokens` guard makes this rare. |
| `"content_filter"` | Provider's safety filter blocked output.         | Emit `EventError` wrapping `ErrContentFiltered` (TBD: see "Open questions"). |
| `"error"`         | Mid-stream provider failure (see below).         | Emit `EventError` from the chunk's `error` envelope, then `EventEnd`. |

Source: <https://openrouter.ai/docs/guides/features/tool-calling> ("OpenRouter normalizes each model's finish_reason to one of: tool_calls, stop, length, content_filter, error").

### Where `usage` lives

Every chunk *can* carry a `usage` field, but in practice:

- Streaming responses emit usage **only on the final chunk** — typically the same chunk that carries `finish_reason`, or a dedicated trailing chunk with empty `choices`.
- aikido's parser MUST tolerate both shapes.
- The legacy `stream_options: {include_usage: true}` flag is deprecated and unnecessary as of 2026; usage is always sent.

Source: <https://openrouter.ai/docs/guides/administration/usage-accounting>.

### Mapping `apiUsage` → `llm.Usage`

[API.md](../API.md) locks `llm.Usage` as:

```go
type Usage struct {
    PromptTokens     int
    CompletionTokens int
    CacheReadTokens  int
    CacheWriteTokens int
    CostUSD          float64
}
```

Mapping:

```go
func toLLMUsage(u *apiUsage) *llm.Usage {
    if u == nil {
        return nil
    }
    return &llm.Usage{
        PromptTokens:     u.PromptTokens,
        CompletionTokens: u.CompletionTokens,
        CacheReadTokens:  u.PromptTokensDetails.CachedTokens,
        CacheWriteTokens: u.PromptTokensDetails.CacheWriteTokens,
        CostUSD:          u.Cost,
    }
}
```

Note: OpenRouter renamed the cache fields some time before 2026. The older shape used `cache_read_input_tokens` / `cache_creation_input_tokens` at the top level of `usage` (still seen in `~/Nextcloud/DeepThought/Patterns/Agent/Prompt-Caching-for-Agent-Loop-Cost-Reduction.md`); the current shape is `prompt_tokens_details.cached_tokens` / `prompt_tokens_details.cache_write_tokens`. aikido's decoder should accept both for resilience but only document the current shape. Treat unknown variants as zero.

Source: <https://openrouter.ai/docs/guides/administration/usage-accounting>.

---

## Tool-call fragmentation

This is the load-bearing piece W3 must get right. The algorithm is canonical, lifted from `~/Nextcloud/DeepThought/Patterns/Streaming-Tool-Call-Assembly-Pattern.md` and `/Users/mapa/github.com/asolabs/hub/internal/client/openrouter/client.go`.

### Wire shape

Each delta carries `tool_calls[]`. Each entry has:

| Field                  | First-occurrence?  | Subsequent fragments?  |
| ---------------------- | ------------------ | ---------------------- |
| `index`                | required           | required               |
| `id`                   | usually present    | usually omitted        |
| `type`                 | usually `"function"` | omitted              |
| `function.name`        | present            | omitted                |
| `function.arguments`   | partial JSON       | partial JSON, concat   |

The provider may emit the `id` and `function.name` on the very first fragment for a given index, then send only `function.arguments` fragments thereafter — but some providers re-send the `id` on every fragment. The algorithm must tolerate either.

Source: <https://openrouter.ai/docs/guides/features/tool-calling> ("the provider maintains state for each tool call and emits events as they complete... supports parallel tool calls by tracking tool calls via an index").

### Algorithm

1. Maintain a `map[int]*partialToolCall` for the current assistant turn.
2. For each `toolCallFragment` in a `delta.tool_calls`:
   - Look up `partials[frag.Index]`. If absent, create with `id = frag.ID`, `name = frag.Function.Name`, empty `arguments` builder.
   - If `frag.ID` is non-empty and the existing `id` is empty, set `id = frag.ID` (handle the reverse-order case where id arrives later).
   - Same for `name`.
   - Append `frag.Function.Arguments` to the builder.
3. **Do NOT emit anything yet.**
4. When the choice's `finish_reason` arrives — `"tool_calls"`, `"stop"`, `"length"`, or `"error"` — and the partials map is non-empty, walk the partials in ascending `index` order and emit one `llm.Event{Kind: EventToolCall, Tool: ...}` per call. The emitted `Arguments` is the concatenated string; aikido does NOT parse it before emitting. Parsing is `tools.Registry.Dispatch`'s job (per `Patterns/Streaming-Tool-Call-Assembly-Pattern.md`, "the string is JSON; aikido does NOT parse it before emitting").
5. Reset the partials map. (In v1 there is exactly one assistant turn per `Stream` call, so this reset is mostly defensive — mid-turn the model rarely emits tools then keeps generating, but the algorithm should support it.)
6. Emit any pending `EventUsage`, then `EventEnd`.

### Worked example: one fragmented tool call

Wire (4 events):

```
data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"loc"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ation\":\"Berlin\"}"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":120,"completion_tokens":18,"total_tokens":138,"cost":0.000414}}

data: [DONE]
```

aikido emits, in order:

1. (nothing for the role-only chunk; empty content)
2. `EventToolCall{ID: "call_abc", Name: "get_weather", Arguments: "{\"location\":\"Berlin\"}"}` — emitted at finish-reason chunk.
3. `EventUsage{PromptTokens: 120, CompletionTokens: 18, CostUSD: 0.000414}`.
4. `EventEnd`.

### Worked example: two interleaved tool calls

Wire (6 events; calls at index 0 and 1 fragment in alternation):

```
data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read_file","arguments":""}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"list_files","arguments":""}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{}"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"README.md\"}"}}]}}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":150,"completion_tokens":42}}

data: [DONE]
```

Buffer state evolution:

| Step | partials[0]                                       | partials[1]                          |
| ---- | ------------------------------------------------- | ------------------------------------ |
| 1    | id=call_a, name=read_file, args=""                | (absent)                             |
| 2    | id=call_a, name=read_file, args=""                | id=call_b, name=list_files, args=""  |
| 3    | id=call_a, name=read_file, args=`{"path":`        | id=call_b, name=list_files, args=""  |
| 4    | id=call_a, name=read_file, args=`{"path":`        | id=call_b, name=list_files, args=`{}` |
| 5    | id=call_a, name=read_file, args=`{"path":"README.md"}` | id=call_b, name=list_files, args=`{}` |

Emit at step 6 (finish_reason): two `EventToolCall`s in index order, then `EventUsage`, then `EventEnd`.

### Worked example: text preamble + one tool call

Some models emit text "thinking aloud" before calling a tool:

```
data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"content":"Let me check the file."},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.md\"}"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":80,"completion_tokens":24}}

data: [DONE]
```

Emission order:

1. `EventTextDelta{Text: "Let me check the file."}` — emit immediately as it arrives.
2. `EventToolCall{ID: "call_x", Name: "read_file", Arguments: "{\"path\":\"a.md\"}"}` — at finish_reason.
3. `EventUsage{...}`.
4. `EventEnd`.

### Edge cases

- **Empty `function.arguments`** at first fragment, full JSON in second: handle naturally — concatenation works on empty strings.
- **No `id` ever** (rare, malformed provider): use `fmt.Sprintf("call_%d", index)` as a deterministic fallback. Log a warning.
- **`finish_reason: "stop"` with non-empty buffer**: emit the calls anyway. The provider returned text-then-tools but ended the turn. Treat identically to `tool_calls`.
- **Stream ends without `finish_reason`** (mid-stream drop): emit any buffered calls, then `EventError`, then `EventEnd`. See "Mid-stream errors" below.

---

## Error responses

### Error envelope (HTTP non-200)

OpenRouter wraps all error responses uniformly:

```json
{
  "error": {
    "code": 429,
    "message": "Rate limit exceeded for anthropic/claude-sonnet-4-6",
    "metadata": {
      "provider_name": "anthropic",
      "raw": "..."
    }
  },
  "user_id": "user_abc123"
}
```

The HTTP status code mirrors `error.code`. `metadata` shape varies by error type:

- Provider error: `{provider_name: string, raw: unknown}`.
- Moderation error (`403`): `{reasons: string[], flagged_input: string, provider_name: string, model_slug: string}` (`flagged_input` truncated to 100 chars).
- Rate-limit error: typically empty or carries the rate-limit window.

Source: <https://openrouter.ai/docs/api/reference/errors-and-debugging>.

### Status-code → llm error mapping

| HTTP | Trigger                                              | aikido wraps as                  |
| ---- | ---------------------------------------------------- | -------------------------------- |
| 400  | Bad Request — invalid params, malformed body         | `ErrInvalidRequest`              |
| 401  | Invalid credentials, expired/disabled API key        | `ErrAuth`                        |
| 402  | Insufficient credits                                 | `ErrAuth` (treat as auth-class — caller can't fix without action) |
| 403  | Moderation flag, model requires moderation           | `ErrInvalidRequest` (the input was rejected; the model isn't down) |
| 408  | Request timeout (server side)                        | `ErrServerError` (retryable)     |
| 413  | Payload too large                                    | `ErrInvalidRequest`              |
| 422  | Unprocessable entity                                 | `ErrInvalidRequest`              |
| 429  | Rate limited                                         | `ErrRateLimited` (retryable)     |
| 500  | Internal server error                                | `ErrServerError` (retryable)     |
| 502  | Bad Gateway — provider down or invalid response      | `ErrServerError` (retryable)     |
| 503  | Service unavailable, no provider matches routing     | `ErrServerError` (retryable)     |

The retry policy lives in `llm/openrouter/retry.go` (W3) and only fires on `ErrRateLimited` and `ErrServerError`. `ErrAuth` and `ErrInvalidRequest` short-circuit immediately — the user has to fix something before another attempt makes sense.

### Rate-limit headers

OpenRouter returns rate-limit metadata as headers on 429 responses:

```
X-RateLimit-Limit:     1000
X-RateLimit-Remaining: 0
X-RateLimit-Reset:     1741305600000   # unix milliseconds
Retry-After:           5                # seconds (integer)
```

`X-RateLimit-Reset` is **unix milliseconds** (per WebSearch finding 29.04.2026), not seconds — verify against actual responses in W3 smoke tests. `Retry-After` follows RFC 7231: integer seconds OR HTTP-date. aikido's retry policy reads `Retry-After` first (simpler), falls back to its exponential backoff if absent. `X-RateLimit-*` are observability-only in v1 (log them; don't decide retry timing from them).

Source: WebSearch 29.04.2026 + <https://openrouter.ai/docs/api/reference/errors-and-debugging>; live verification needed.

### Mid-stream errors

Once the HTTP response is `200 OK` and SSE has begun, an upstream provider failure cannot change the HTTP status. OpenRouter surfaces the error as a chunk with a top-level `error` field:

```
data: {"id":"gen-1","object":"chat.completion.chunk","created":1714402356,"model":"openai/gpt-4o","provider":"openai","error":{"code":"server_error","message":"Provider disconnected"},"choices":[{"index":0,"delta":{"content":""},"finish_reason":"error"}]}

data: [DONE]
```

aikido behavior:

- Emit any buffered tool calls (defensive — usually empty at this point).
- Emit `EventError` wrapping the error envelope. The `Err` field is `fmt.Errorf("openrouter mid-stream: %s: %w", e.Message, ErrServerError)`.
- Emit `EventEnd` and close the channel.
- **Do NOT retry** — bytes have already been billed (per `~/Nextcloud/DeepThought/Patterns/Backend/OpenRouter-SSE-Streaming-Retry-Semantics-Pattern.md`). The agent loop sees `EventError` and terminates the turn with `EndReason="error"` per [ARCHITECTURE.md](../../ARCHITECTURE.md).

If the connection drops without an error chunk (TCP reset, client timeout), behave the same way: emit `EventError{ErrServerError}` and `EventEnd`, do not retry.

Source: <https://openrouter.ai/docs/api/reference/errors-and-debugging> + `Patterns/Backend/OpenRouter-SSE-Streaming-Retry-Semantics-Pattern.md`.

### Retry policy — stream-start only

Per ADR-006 + the DeepThought pattern: retry only happens **before the first byte of the SSE body has been read**. Concretely:

- `httpClient.Do` returns. If the response is `429` or `5xx` (excluding 501/505 which are client-fault permanent), close the body and retry per the `retry.Policy`.
- If the response is `200` and we have started reading SSE lines, **never retry**. Mid-stream errors propagate as `EventError`.

aikido's `retry.Policy` defaults (per [API.md](../API.md)):

- `MaxAttempts: 3`
- `BaseDelay: 500ms`
- `MaxDelay: 30s`
- `Multiplier: 2.0`
- `Jitter: 0.2`
- `RetryOn`: `errors.Is(err, ErrRateLimited) || errors.Is(err, ErrServerError)`.

If `Retry-After` is present on a 429, sleep for that duration instead of computing the next backoff.

### HTTP-client timeout note

Per `~/Nextcloud/DeepThought/References/OpenRouter-Sonnet-Timeout-Tuning.md`: a hard `http.Client.Timeout` of 60s is too aggressive — Sonnet multi-tool turns regularly need 60-180s. aikido's default is `Timeout: 0` (no client-level timeout) per [API.md](../API.md), relying on `ctx` for cancellation. Callers wanting a wall-clock cap pass a context with deadline; the agent's `TurnTimeout` (default 120s) provides one such deadline by default.

---

## Model IDs

### Catalog endpoint

`GET {base}/models` returns the full upstream catalog. aikido does NOT call this in v1 — it ships a static seed catalog in `llm/catalog.go` (per [PLAN.md](../PLAN.md) W1). Dynamic catalog refresh is a v2 concern.

Source: <https://openrouter.ai/docs/api/reference/overview>.

### Hyphen-vs-dot normalization

Per `~/Nextcloud/DeepThought/Patterns/OpenRouter-Model-ID-Hyphen-Normalization-Gotcha.md` and `OpenRouter-Model-ID-Naming-Convention.md`:

- OpenRouter's catalog uses **hyphens** in model IDs: `anthropic/claude-sonnet-4-6`, `anthropic/claude-opus-4-7`, `openai/gpt-4o`.
- Human/config conventions use **dots** for semver: `anthropic/claude-sonnet-4.6`.
- A mismatch silently falls back to a different model or returns 400/404 — there is no helpful error.

`llm/openrouter/modelid.go` (W2) implements:

```go
func normalizeModelID(id string) string {
    return strings.ReplaceAll(id, ".", "-")
}
```

This runs at request-build time, after `llm.Catalog` lookup but before serializing to `chatRequest.Model`. The catalog itself stores hyphenated IDs; `llm.FindModel` normalizes the lookup input before matching.

### Family prefixes

The catalog prefixes by provider family. Common prefixes seen in v1 seed catalog:

- `anthropic/` — Claude models (`claude-sonnet-4-6`, `claude-opus-4-7`, `claude-haiku-4-5`).
- `openai/` — GPT, o-series (`gpt-4o`, `gpt-5`, `o1`, `o3`).
- `google/` — Gemini (`gemini-2.5-pro`, `gemini-2.5-flash`).
- `mistralai/` — Mistral (`mistral-large-2`, `mixtral-8x22b`).
- `meta-llama/` — Llama (`llama-3.3-70b-instruct`).
- `deepseek/` — DeepSeek (`deepseek-r1`, `deepseek-v3`).
- `x-ai/` — Grok (`grok-2`, `grok-vision-beta`).

Suffix `:free` denotes a free tier with stricter rate limits. v1 does not seed `:free` IDs.

Source: <https://openrouter.ai/docs/api/reference/overview>; live catalog at <https://openrouter.ai/api/v1/models>.

### Provider-routing flags aikido does NOT use in v1

OpenRouter exposes a `provider` block for routing control:

```json
{
  "provider": {
    "order": ["anthropic"],
    "only": ["anthropic"],
    "ignore": ["openai"],
    "allow_fallbacks": false,
    "data_collection": "deny",
    "zdr": true,
    "max_price": {"prompt": "0.50", "completion": "1.00"},
    "preferred_max_latency": 5.0,
    "sort": "throughput"
  }
}
```

Reference: `~/Nextcloud/DeepThought/Patterns/Agent/OpenRouter-Provider-Routing-Pattern.md`.

aikido v1 omits this entirely — model selection is pure model-string. v2 may surface a few of these (notably `provider.order` for cache stickiness when callers are bypassing aikido's own caching strategy, and `data_collection: "deny"` for compliance). For v1, the implicit default (auto-route, allow fallbacks, allow data collection) is correct.

### Plugins aikido does NOT use in v1

OpenRouter ships server-side plugins (web search, web fetch, image gen, datetime, model search) wired through the `plugins` request field and `openrouter:*` server-tool prefixes. aikido v1 ignores all of them — tool-use is local-first via `tools.Registry`. v2 may add a passthrough hook.

Source: <https://openrouter.ai/docs/api/api-reference/chat/send-chat-completion-request>.

---

## Cache control behavior

### Anthropic (full passthrough)

When the routed provider is Anthropic, `cache_control: {"type": "ephemeral"}` blocks are forwarded verbatim to the Messages API. The block's content is cached for ~5 minutes (or 1h with the `ttl` variant). Cache reads cost ~10% of fresh-input rate; cache writes cost ~125% (5min) or ~200% (1h).

OpenRouter applies **provider sticky routing** when a request uses caching — subsequent requests for the same model and conversation route to the same Anthropic endpoint to keep the cache warm. Stickiness is keyed on (account, model, conversation-hash where conversation-hash = hash(first system message + first non-system message)). Sticky routing disengages if the caller sets a manual `provider.order`.

Supported Claude models per docs: Opus 4.7, 4.6, 4.5, 4.1, 4; Sonnet 4.6, 4.5, 4; Haiku 4.5, 3.5.

Source: <https://openrouter.ai/docs/guides/best-practices/prompt-caching>.

### OpenAI / DeepSeek / Grok / Moonshot / Groq / Gemini 2.5

These providers cache **automatically** without `cache_control` directives. Sending `cache_control` is harmless (silently ignored) — the provider applies its own heuristic. aikido sending `cache_control` on these providers wastes a few bytes but does not error.

Source: <https://openrouter.ai/docs/guides/best-practices/prompt-caching>.

### Other providers

Behavior is "silently ignore the directive" in practice. No documented error for unknown providers seeing `cache_control`. aikido tolerates this — `Message.CacheHint` is a hint, not a guarantee, per [API.md](../API.md) and ADR-002.

### aikido stance — explicit

1. When `Message.CacheHint == true`, attach `cache_control: {"type": "ephemeral"}` to the **last content block of that message**, converting plain-text content to the array form if necessary.
2. Do not check provider support beforehand. Tolerate silent drops.
3. Surface `Usage.CacheReadTokens` and `Usage.CacheWriteTokens` so callers can verify caching is working post-hoc.
4. The 1-hour TTL is not exposed in v1.
5. Direct Anthropic provider with full caching API control is v2 (per ADR-002).

Source: ADR-002, [API.md](../API.md), `Patterns/Anthropic-Prompt-Caching-Cost-Control.md`.

---

## Test fixtures to record

All under `llm/openrouter/testdata/`. Each file is a **raw HTTP response body** — SSE format, with literal `\r\n` line endings if needed (Go's `httptest.Server` writes whatever bytes you give it). Tests read the file, serve it through `httptest.NewServer`, point an `openrouter.Client` at the server, and assert the resulting `<-chan llm.Event` shape.

Where a fixture needs an HTTP wrapper (status code, headers), pair the `.sse` with a `.json` describing the response envelope; the test harness loads both.

### `simple_text.sse`

Pure text completion, `finish_reason: "stop"`, usage on final chunk.

```
data: {"id":"gen-1","object":"chat.completion.chunk","created":1714402356,"model":"anthropic/claude-sonnet-4-6","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"gen-1","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"gen-1","choices":[{"index":0,"delta":{"content":", world"},"finish_reason":null}]}

data: {"id":"gen-1","choices":[{"index":0,"delta":{"content":"."},"finish_reason":null}]}

: OPENROUTER PROCESSING

data: {"id":"gen-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":18,"completion_tokens":4,"total_tokens":22,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"cost":0.000066}}

data: [DONE]

```

Expected events: 3× `EventTextDelta` (`"Hello"`, `", world"`, `"."`), 1× `EventUsage`, 1× `EventEnd`.

### `single_tool.sse`

Text preamble + one tool call assembled across 3 fragments + `finish_reason: "tool_calls"`.

```
data: {"id":"gen-2","object":"chat.completion.chunk","created":1714402400,"model":"anthropic/claude-sonnet-4-6","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"gen-2","choices":[{"index":0,"delta":{"content":"Let me check that file."},"finish_reason":null}]}

data: {"id":"gen-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"read_file","arguments":""}}]},"finish_reason":null}]}

data: {"id":"gen-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]},"finish_reason":null}]}

data: {"id":"gen-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"README.md\"}"}}]},"finish_reason":null}]}

data: {"id":"gen-2","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":92,"completion_tokens":24,"total_tokens":116,"cost":0.000348}}

data: [DONE]

```

Expected events: 1× `EventTextDelta("Let me check that file.")`, 1× `EventToolCall{ID:"call_abc", Name:"read_file", Arguments:"{\"path\":\"README.md\"}"}`, 1× `EventUsage`, 1× `EventEnd`.

### `multi_tool_interleaved.sse`

Two tool calls fragmented and interleaved by index.

```
data: {"id":"gen-3","object":"chat.completion.chunk","created":1714402500,"model":"anthropic/claude-sonnet-4-6","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"gen-3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read_file","arguments":""}}]},"finish_reason":null}]}

data: {"id":"gen-3","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"list_files","arguments":""}}]},"finish_reason":null}]}

data: {"id":"gen-3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]},"finish_reason":null}]}

data: {"id":"gen-3","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{}"}}]},"finish_reason":null}]}

data: {"id":"gen-3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.md\"}"}}]},"finish_reason":null}]}

data: {"id":"gen-3","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":140,"completion_tokens":36,"total_tokens":176,"cost":0.000528}}

data: [DONE]

```

Expected events: 2× `EventToolCall` in index order (`call_a` first with full JSON `{"path":"a.md"}`, `call_b` second with `{}`), 1× `EventUsage`, 1× `EventEnd`. The test asserts `json.Valid(toolCalls[0].Arguments)` and `json.Valid(toolCalls[1].Arguments)`.

### `429_then_success.sse`

Documents the **two-response sequence** the test sets up — this is HTTP-layer, not a single SSE file. Two artifacts in `testdata/`:

- `429_response.json` — first response: status 429, headers, body. Used by the test harness to fail the first request.
- `success_after_429.sse` — second response: full streaming success (mirror of `simple_text.sse`).

`429_response.json`:

```json
{
  "status": 429,
  "headers": {
    "Content-Type": "application/json",
    "X-RateLimit-Limit": "1000",
    "X-RateLimit-Remaining": "0",
    "X-RateLimit-Reset": "1741305600000",
    "Retry-After": "1"
  },
  "body": {
    "error": {
      "code": 429,
      "message": "Rate limit exceeded for anthropic/claude-sonnet-4-6",
      "metadata": {}
    }
  }
}
```

The test:

1. Configures `httptest.Server` to return `429_response.json` on the first call, `success_after_429.sse` (with status 200, content-type `text/event-stream`) on the second.
2. Configures the client with `Retry: &retry.Policy{MaxAttempts: 2, BaseDelay: 10ms, ...}`.
3. Calls `Stream`. Asserts events: same as `simple_text.sse`. Asserts the client made exactly 2 HTTP requests. Asserts the wait between requests ≥ 1 second (honoring `Retry-After`).

### `mid_stream_error.sse`

Partial content then a `data:` envelope with a top-level `error`, then `[DONE]`.

```
data: {"id":"gen-5","object":"chat.completion.chunk","created":1714402600,"model":"openai/gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"gen-5","choices":[{"index":0,"delta":{"content":"Working on it"},"finish_reason":null}]}

data: {"id":"gen-5","choices":[{"index":0,"delta":{"content":"..."},"finish_reason":null}]}

data: {"id":"gen-5","object":"chat.completion.chunk","provider":"openai","error":{"code":"server_error","message":"Provider disconnected"},"choices":[{"index":0,"delta":{"content":""},"finish_reason":"error"}]}

data: [DONE]

```

Expected events: 2× `EventTextDelta("Working on it", "...")`, 1× `EventError{Err: "openrouter mid-stream: Provider disconnected"}`, 1× `EventEnd`. The test asserts no retry happens and `errors.Is(ev.Err, llm.ErrServerError)` is true.

### Optional later additions

Not required for W3 exit but useful for hardening:

- `cancellation.sse` — long-running stream where the test cancels `ctx` mid-flight; assert producer goroutine exits within 100ms and channel closes.
- `unknown_field.sse` — a chunk with extra unknown JSON fields; assert decoder ignores them and continues.
- `cache_hit.sse` — usage chunk with `cached_tokens > 0`; assert mapping into `llm.Usage.CacheReadTokens`.

---

## Open questions

Things this research surfaced that PLAN.md / API.md / ADRs do not yet address. None blocks W3 — flagging for MaPa to decide on or defer.

1. **`finish_reason: "content_filter"` mapping.** Currently no typed error for "model refused to answer due to safety filter." Options: add `ErrContentFiltered` to `llm/errors.go` and route it via `EventError`; or treat as `EventEnd` with `EndReason="content_filter"` (this would require extending the agent's `EndReason` strings beyond `"stop"|"max_turns"|"error"|"timeout"`). v1 default: emit `EventError` wrapping a generic error. Locked-in API surface allows both — choose at implementation time.

2. **`refusal` field on assistant messages.** OpenAI structured-refusal returns `message.refusal: "I can't help with that."` separately from `content`. Not exposed in `llm.Event`. Likely fine to ignore for v1 — providers also surface refusals via plain text content.

3. **`reasoning` field on deltas.** Models like o1 / Claude with `reasoning.effort` set emit `delta.reasoning` content separately from `delta.content`. aikido's `EventTextDelta` carries only `content`. Options: discard reasoning tokens silently (smallest change); add a `Reasoning string` field on `Event` (breaks locked surface); treat as text and concatenate (muddles the output). v1 default: discard silently, log usage. Worth flagging in API.md as known limitation.

4. **`completion_tokens_details.reasoning_tokens` not represented in `llm.Usage`.** Reasoning tokens are billable but not counted in `CompletionTokens` on some providers. v1 `Usage` is locked to four token fields; extra reasoning tokens become invisible to the caller. Either add `ReasoningTokens int` to `llm.Usage` (breaks locked surface) or document the gap.

5. **Rate-limit header semantics.** WebSearch suggests `X-RateLimit-Reset` is unix-millis; OpenRouter's own docs don't confirm. aikido's smoke test in W2 should log the actual headers and verify. If reset turns out to be seconds elsewhere, the retry policy needs to handle both.

6. **Mid-stream error without an `error` envelope.** Some providers drop the connection silently (TCP RST, no final chunk). aikido handles this as `EventError + EventEnd`; worth confirming the `bufio.Scanner` returns a clean error in that case rather than a silent EOF.

7. **`stream_options` and request-body `usage`** truly being deprecated. The chat-completions reference page still shows `stream_options` in examples; the usage-accounting page says it's deprecated. aikido should NOT send either — but if the old behavior persists for some providers, sending `usage:{include:true}` might be the safer hedge. Recommend live-testing both and going with whichever makes usage chunks reliably appear for Anthropic + OpenAI + Gemini.

8. **`cost` precision in usage.** OpenRouter reports `cost` as a JSON number; observed values are 4–6 decimal places. Decoded as `float64`, this preserves full precision but float arithmetic is treacherous for billing. v1 stores it as-is in `Usage.CostUSD float64` per [API.md](../API.md) — fine for display, not fine for accounting downstream of aikido. Document the limitation in godoc.
