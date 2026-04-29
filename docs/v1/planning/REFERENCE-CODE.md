# Reference Code Mining

> ⚠️ **Divergence banner (30.04.2026).** This file reflects the *pre-second-grilling* surface in places. Reference-code lift-and-adapt advice remains valid as raw material; the *target shape* it adapts toward has changed. Specifically: target is `agent.Session` / `Run` / `NewLocalSession` (not `agent.NewAgent` / `Run(ctx, projectID, ...)`); concurrency target is the `agent.Locker` interface plus `LocalLocker` (not a per-Session struct mutex or per-project mutex); History target is variadic `Append(ctx, sessionID, msgs...)` flushed once at end of turn (not per-step); no `Tx` / `Snapshot` / `SchemaFromType` / `Catalog` lift targets in v1. Cross-check call shapes against `../API.md`.

Maps the production code aikido v1 generalizes. Use this when implementing a wave to know exactly what to copy, what to adapt, and where the original lives.

The waves in [PLAN.md](../PLAN.md) name the source patterns; this document names the source files. For each reference file: the verdict (lift / adapt / inspiration / anti-pattern), the line ranges that matter, and the diff between "what's there" and "what aikido needs."

The reference codebases are MaPa's working code. They are pre-aikido — they encode the patterns aikido extracts but they were not written with reuse in mind. Copying verbatim is rare; copying the *shape* and changing the *contracts* is the common path.

---

## Index

| Reference file | aikido target | Verdict |
|---|---|---|
| `asolabs/hub/sandbox/agent-test/agent/loop.go` | `agent/run.go`, `agent/agent.go`, `agent/event.go` | adapt — generalize, make stateless, add streaming |
| `asolabs/hub/sandbox/agent-test/agent/openrouter.go` | `llm/openrouter/api_types.go`, `llm/openrouter/client.go` | adapt — split request shape from request behavior; remove sandbox concerns |
| `asolabs/hub/sandbox/agent-test/agent/post.go` | (none — domain-specific) | drop — does not belong in aikido |
| `asolabs/hub/sandbox/agent-test/agent/tools.go` | `agent/tools_builtin.go`, `vfs/path.go` | adapt — `safePath` becomes `vfs.ValidatePath`; tool bodies become VFS-aware handlers |
| `asolabs/hub/internal/client/openrouter/client.go` | `llm/openrouter/client.go`, `llm/openrouter/api_types.go` | adapt — keep API-types layer + conversion functions; replace request methods with single `Stream` |
| `asolabs/hub/internal/client/openrouter/client_test.go` | `llm/openrouter/client_test.go` | inspiration — keep nil-request guards and conversion-function tests; add httptest replay |
| `asolabs/hub/internal/nexi/tools.go` | `agent/tools_builtin.go`, callers' tool-registration sites | adapt — explicit-schema style is the model; `executeTool` switch becomes `Registry.Dispatch` |
| `asolabs/hub/internal/nexi/agent.go` | `agent/run.go` | inspiration — emits typed events already; aikido replaces interface-typed events with channel-of-struct |
| `asolabs/hub/internal/nexi/types.go` | `agent/event.go` | inspiration — confirms event vocabulary, but aikido uses `Event{Kind: …}` not interface marker |
| `mxcd/go-basicauth/storage_interface.go` | `vfs/storage.go`, `vfs/txstorage.go` | lift the *idiom* — base interface + capability interface, plus comment style |
| `mxcd/go-basicauth/storage_memory.go` | `vfs/memory/storage.go` | lift the *shape* — `sync.RWMutex`, map-based, lowercase-normalized keys (paths instead of usernames) |
| `mxcd/go-basicauth/handler.go` | `agent/agent.go`, `notes/notebook.go`, every `NewX` constructor | lift the *constructor pattern* — `Options` validation, defaults application, error early |
| `mxcd/go-basicauth/types.go` | All `Options` and `Errors` definitions | inspiration — top-of-file `var (…)` error block, default-settings function |
| `wilde/.../evaluator/api/internal/job/ai.go` | (none — anti-pattern reference) | drop — JSON-Schema-mode + non-streaming is what aikido explicitly avoids |

---

## Per-reference deep dives

### 1. `asolabs/hub/sandbox/agent-test/agent/loop.go` → `agent/run.go`

**LOC:** 445 (file). The loop body itself is ~40 lines (L226–L267). The rest is system prompt (L13–L81), tool defs (L83–L196), and `executeTool` switch (L269–L440).
**Verdict:** **adapt.** This is the file aikido is supposed to generalize. The loop shape carries over; everything around it is replaced.

#### Lift verbatim

The control-flow skeleton at L229–L264 *is* the aikido loop. Same shape, same termination conditions. Aikido changes how each step is implemented — not the order of steps.

```go
// loop.go L229-L264 — production reference
for i := 0; i < maxIterations; i++ {
    resp, err := a.client.Chat(ctx, a.history, toolDefs)
    if err != nil {
        return "", fmt.Errorf("chat request failed: %w", err)
    }
    if len(resp.Choices) == 0 {
        return "", fmt.Errorf("no choices in response")
    }
    msg := resp.Choices[0].Message
    a.history = append(a.history, msg)

    // If no tool calls, we're done — return the text
    if len(msg.ToolCalls) == 0 {
        if msg.Content != nil { return *msg.Content, nil }
        return "", nil
    }

    // Execute each tool call
    for _, tc := range msg.ToolCalls {
        result := a.executeTool(tc.Function.Name, tc.Function.Arguments)
        if onToolCall != nil { onToolCall(tc.Function.Name, tc.Function.Arguments, result) }
        a.history = append(a.history, Message{
            Role:       RoleTool,
            Content:    strPtr(result),
            ToolCallID: tc.ID,
        })
    }
}
return "", fmt.Errorf("agent exceeded maximum iterations (%d)", maxIterations)
```

The aikido equivalent has the same six steps:

1. Call client (now `Stream` not `Chat`).
2. Drain events.
3. Append assistant message to history.
4. If no tool calls — emit `EventEnd("stop")`, return.
5. Dispatch each tool call (now via `tools.Registry`, not `switch`).
6. Append tool result to history.

After the loop: emit `EventEnd("max_turns")`.

#### Adapt

| Field of loop.go | Aikido replacement |
|---|---|
| `maxIterations = 25` (L11, hard-coded) | `Options.MaxTurns` defaulting to 20 (per ARCHITECTURE.md guardrails) |
| `a.client.Chat(...)` returning a non-streaming `ChatResponse` | `a.client.Stream(ctx, req)` returning `<-chan llm.Event`; agent drains and forwards |
| `a.history` field on `Agent` (L201, stateful) | Stateless: history is a local slice in `runLoop`; multi-turn callers use `RunWithMessages(ctx, projectID, history)` per ADR-011 |
| `(string, error)` return | `<-chan agent.Event, error` return per ADR-006 / `EventEnd` always last |
| `onToolCall func(name, args, result string)` callback (L226) | Replaced by `EventToolCall` + `EventToolResult` on the channel — caller filters; no callback parameter |
| `a.executeTool(name, argsJSON)` returning a single string (L269–L440) | `tools.Registry.Dispatch(ctx, call, env)` returning `(tools.Result, error)`; agent builds `ToolResult{OK, Content, Error}` from the pair |
| Tool dispatch directly on `a.basePath` filesystem | Tool dispatch through `tools.Env{Storage: opts.Storage}`; storage is the abstraction |
| No transaction wrapping | Wrap each turn's tool dispatches in `BeginTx`/`Commit` if `opts.Storage` satisfies `vfs.TxStorage`; rollback on any tool error |
| No per-project mutex | Acquire `sync.Map[uuid.UUID]*sync.Mutex` lock at start of `Run`; release on defer (per ADR-009) |
| No turn timeout | `ctx, cancel := context.WithTimeout(ctx, opts.TurnTimeout)`; cancel on defer |
| `fmt.Errorf("agent exceeded maximum iterations (%d)", ...)` | `emit(EventEnd{EndReason: "max_turns"})` — the agent does not return errors for end-condition reasons; it returns errors only for setup failures |
| Hard-coded 25-message system prompt embedded in package (L13–L81) | `Options.SystemPrompt` — caller supplies; library has no opinions about prompt content |

#### Drop

- The Nexi system prompt (L13–L81). Not aikido's concern.
- The `toolDefs` array (L83–L196). Tool registration is the *caller's* job; aikido provides built-in VFS tools via `RegisterVFSTools`.
- `executeTool`'s domain-specific cases: `create_post`, `list_posts`, `read_post_metadata`, `move_post`, `archive_path`, `archive_all`, `list_archives` (L321–L435). These belong in caller code, not aikido.
- `AppendAgentInstructions` (L216). Non-orthogonal — callers compose their own system prompt.
- `strPtr` helper (L442). Aikido `Message.Content` is `string` not `*string`; nil-vs-empty distinction goes away (`Content == "" + len(ToolCalls) > 0` covers the assistant-with-only-tool-calls case).

#### Snippet to mirror

The clean part — the loop body — should look like this in aikido:

```go
// agent/run.go (target shape)
for turn := 0; turn < a.opts.MaxTurns; turn++ {
    events, err := a.opts.Client.Stream(ctx, llm.Request{
        Model:    a.opts.Model,
        Messages: msgs,
        Tools:    a.opts.Tools.Defs(),
        // ...
    })
    if err != nil {
        emit(Event{Kind: EventError, Err: err})
        emit(Event{Kind: EventEnd, EndReason: "error"})
        return
    }

    text, calls, usage := drain(events, out)  // forwards TextDelta verbatim
    msgs = append(msgs, llm.Message{
        Role:      llm.RoleAssistant,
        Content:   text,
        ToolCalls: calls,
    })
    if usage != nil {
        emit(Event{Kind: EventUsage, Usage: usage})
    }

    if len(calls) == 0 {
        emit(Event{Kind: EventEnd, EndReason: "stop"})
        return
    }

    runToolCalls(ctx, calls, &msgs, out, env, txStorage)
}
emit(Event{Kind: EventEnd, EndReason: "max_turns"})
```

The original is 40 lines; the aikido version will be ~80 because of streaming, transactions, and event emission. That's fine — it's still well under the 200–300 LOC budget called out in ADR-003.

---

### 2. `asolabs/hub/sandbox/agent-test/agent/openrouter.go` → `llm/openrouter/api_types.go`, `llm/openrouter/client.go`

**LOC:** 148.
**Verdict:** **adapt.** Useful as a request-shape reference; the request *behavior* (sync POST, no SSE, no retry) is wrong for aikido.

#### Lift verbatim

The wire-shape types (L24–L82) match OpenRouter's actual JSON. They are the canonical reference for the unexported `api*` types in `llm/openrouter/api_types.go`:

```go
// openrouter.go L24-L82 — wire shapes that survive into aikido (renamed, unexported)
type Message struct {
    Role       Role        `json:"role"`
    Content    *string     `json:"content"`
    ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
    ToolCallID string      `json:"tool_call_id,omitempty"`
}
type ToolCall struct {
    ID       string       `json:"id"`
    Type     string       `json:"type"`
    Function FunctionCall `json:"function"`
}
type FunctionCall struct {
    Name      string `json:"name"`
    Arguments string `json:"arguments"`
}
type Tool struct {
    Type     string       `json:"type"`
    Function ToolFunction `json:"function"`
}
type ToolFunction struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    Parameters  json.RawMessage `json:"parameters"`
}
```

These map 1:1 to `apiMessage`, `apiToolCall`, `apiFunctionCall`, `apiTool`, `apiToolFunction` in aikido (they should be unexported because they are wire-only; conversion functions to/from `llm.Message`/`llm.ToolCall` live alongside).

#### Adapt

| openrouter.go | aikido `llm/openrouter` |
|---|---|
| `Message.Content *string` (pointer for omitempty) | `apiMessage.Content *string` for wire shape, but `llm.Message.Content string`; convert to `nil` when empty + ToolCalls present, else use the literal string |
| `Chat(ctx, messages, tools)` — single non-streaming POST | `Stream(ctx, req)` returning `<-chan llm.Event`; SSE under the hood (per W3) |
| `client.Timeout = 180 * time.Second` (L96) | `Options.HTTPClient` defaults to `&http.Client{Timeout: 0}` per API.md L168 — streams may be long-lived, so global timeout = wrong; use ctx |
| `ProviderPreferences` struct (L52–L56) and conditional inclusion (L107–L112) | Drop in v1 — `llm.Request` does not expose provider routing. Add `Options.ProviderOrder []string` if a caller requests it; defer to v2 if not |
| Direct `bytes.NewReader(body)` send | Wrap in `retry.Do(ctx, policy, func() error { ... })` for 429/5xx at stream-start (per W3) |
| Hard-coded `openRouterURL` (L13) | `Options.BaseURL` defaulting to `https://openrouter.ai/api/v1` (per API.md L165) |
| Plain `Authorization: Bearer` header (L125) | Same, plus optional `HTTP-Referer` and `X-Title` headers from `Options.HTTPReferer`/`XTitle` |

#### Drop

- The bundled `Agent` type (L198 in loop.go) — already covered above.
- `ChatResponse` shape (L65–L82) is *almost* right but it's the non-streaming response, not the SSE chunk shape. The streaming version is documented in `Streaming-Tool-Call-Assembly-Pattern.md` and needs a separate type (`apiStreamChunk` with a `delta` field).
- `model` and `provider` baked into the client struct (L86–L88). aikido takes both per-request from `llm.Request.Model`. The client is provider-keyed, not model-keyed.

#### Snippet to mirror

Construction shape:

```go
// llm/openrouter/client.go (target shape)
func NewClient(opts *Options) (*Client, error) {
    if opts == nil { return nil, fmt.Errorf("openrouter: options required") }
    if opts.APIKey == "" { return nil, fmt.Errorf("openrouter: APIKey required") }

    c := &Client{
        apiKey:  opts.APIKey,
        baseURL: stringOr(opts.BaseURL, defaultBaseURL),
        httpClient: opts.HTTPClient,
        // ...
    }
    if c.httpClient == nil {
        c.httpClient = &http.Client{} // no timeout — streams
    }
    return c, nil
}

var _ llm.Client = (*Client)(nil)
```

---

### 3. `asolabs/hub/sandbox/agent-test/agent/post.go` → (no aikido target)

**LOC:** 491.
**Verdict:** **drop.** This file is the post-domain logic (front-matter parsing, `WritePostFile`, `ListPosts`, `ArchiveAll`, etc.) for the Nexi business — not part of aikido. The only aikido-relevant takeaway is that *callers* will write files like this; aikido just provides the storage abstraction and the agent loop.

The cross-reference is for `notes` package design: `notes.Notebook.Add` writes notes as `_notes/{turn-uuid}.md`, which is conceptually similar to how `WritePostFile` writes posts as `posts/{YYYY-MM}/{platform}-{date}-{slug}.md`. Same pattern, different domain. aikido lives one level lower.

#### Drop

Everything. `safePath` is the *only* function from this file's neighbor (`tools.go`) that aikido needs — see #4 below. `parseFrontMatter`, `ListPosts`, `ArchiveAll`, etc. are caller concerns.

---

### 4. `asolabs/hub/sandbox/agent-test/agent/tools.go` → `agent/tools_builtin.go`, `vfs/path.go`

**LOC:** 153.
**Verdict:** **adapt.** Tool bodies become VFS-aware handlers; `safePath` becomes `vfs.ValidatePath`.

#### Lift verbatim (with rename + reframe)

`safePath` (L12–L34) is the path-validation primitive. aikido's `vfs.ValidatePath(path string) error` does the same job but on relative paths only — there is no `basePath` because the storage abstraction owns the namespace.

```go
// tools.go L12-L34 — the validation logic worth lifting
func safePath(basePath, relativePath string) (string, error) {
    cleaned := filepath.Clean(relativePath)
    if filepath.IsAbs(cleaned) {
        return "", fmt.Errorf("absolute paths are not allowed")
    }
    full := filepath.Join(basePath, cleaned)
    absBase, _ := filepath.Abs(basePath)
    absFull, _ := filepath.Abs(full)
    if !strings.HasPrefix(absFull, absBase+string(filepath.Separator)) && absFull != absBase {
        return "", fmt.Errorf("path %q escapes working directory", relativePath)
    }
    return absFull, nil
}
```

The aikido form is simpler because there is no real filesystem to escape:

```go
// vfs/path.go (target shape)
func ValidatePath(path string) error {
    if path == "" { return ErrPathInvalid }
    if filepath.IsAbs(path) { return ErrPathInvalid }
    cleaned := filepath.Clean(path)
    if cleaned == "." || strings.HasPrefix(cleaned, "../") || cleaned == ".." { return ErrPathInvalid }
    if strings.ContainsRune(path, 0) { return ErrPathInvalid }
    // length cap, allowed characters, etc.
    return nil
}
```

The pre-existing logic catches: absolute paths, `..` traversal, escape via `filepath.Join`. aikido must additionally catch: empty paths, null bytes, oversized paths, and (per ADR-008 & PLAN W5) reject `_*` *prefixes* only when the agent's `HideHiddenPaths` flag is on at *list/search time* — not at write time.

#### Adapt

| tools.go | aikido `agent/tools_builtin.go` |
|---|---|
| `ListFiles(basePath, relativePath)` walking `os.ReadDir` (L36–L56) | `list_files` handler calling `env.Storage.ListFiles(ctx, env.Project)`; flat list, not directory tree |
| `ReadFile(basePath, relativePath)` (L58–L69) | `read_file` handler calling `env.Storage.ReadFile(ctx, env.Project, path)`; returns `{content, content_type, size}` |
| `WriteFile(basePath, relativePath, content)` (L71–L91) | `write_file` handler calling `env.Storage.WriteFile(...)`; aikido does *not* hard-code "agent.md" protection — that's a caller convention |
| Hidden-file protection by hard-coded filename | `VFSToolOptions.HideHiddenPaths` filters `_*` from `list_files` and `search_files` (per ADR-008, hidden-by-prefix, not hidden-by-name) |
| `SearchFiles(basePath, query)` walking + scanning (L99–L148) | `search_files` handler iterating `env.Storage.ListFiles` then `ReadFile` per match; same case-insensitive substring; cap of 50 results lifts verbatim |
| `GetCurrentDate()` returning `time.Now()` formatted | aikido does *not* ship a `get_current_date` tool. It's caller-specific. Remove. (See open question #4.) |
| Result is a `string` returned via the `executeTool` switch | Result is `tools.Result{Content: any, Display: string, ChangedPaths: []string}`; agent serializes `Content` to JSON for the model |
| 50-result cap silently truncates (L143–L145) | Surface this in `Result.Display` (e.g., `"Truncated to 50 matches"`) so the model can ask for a refined query |

#### Drop

- "agent.md" hard-coded protection (L78–L80). Aikido's library does not know about specific filenames. If the caller wants to make `_config.md` read-only, they wrap `WriteFile` themselves or provide a `Storage` that rejects it.
- The `os` and `filepath` imports — `vfs/memory` deals in maps, not directories. Path components are byte-string keys, not directory entries.
- `dir+="/"` directory marker in `ListFiles` (L51). aikido lists files, not directories — flat namespace.

#### Snippet to mirror

The handler shape per built-in tool:

```go
// agent/tools_builtin.go (target shape)
func readFileHandler(ctx context.Context, args json.RawMessage, env tools.Env) (tools.Result, error) {
    var p struct { Path string `json:"path"` }
    if err := json.Unmarshal(args, &p); err != nil {
        return tools.Result{}, fmt.Errorf("read_file: invalid args: %w", err)
    }
    if err := vfs.ValidatePath(p.Path); err != nil {
        return tools.Result{}, fmt.Errorf("read_file: %w", err)
    }
    content, meta, err := env.Storage.ReadFile(ctx, env.Project, p.Path)
    if err != nil {
        return tools.Result{}, err
    }
    return tools.Result{
        Content: map[string]any{
            "content":      string(content),
            "content_type": meta.ContentType,
            "size":         meta.Size,
        },
        Display: fmt.Sprintf("read %d bytes from %s", meta.Size, p.Path),
    }, nil
}
```

This shape is the same for `write_file`, `list_files`, `delete_file`, `search_files`. Each one parses args via a tiny anonymous struct, validates, calls Storage, returns a Result. ~25 lines each.

---

### 5. `asolabs/hub/internal/client/openrouter/client.go` → `llm/openrouter/client.go` + `llm/openrouter/api_types.go`

**LOC:** 521.
**Verdict:** **adapt.** This is the production OpenRouter client. It has three methods (`Chat`, `ChatImage`, `ChatWithTools`); aikido collapses to one (`Stream`).

#### Lift verbatim

The two-layer architecture — public types vs. wire types — is exactly right and survives:

1. **Public types** (`client.ChatRequest`, `client.ChatMessage`, `client.ToolCall`, etc.) — what callers see. In aikido, these are `llm.Request`, `llm.Message`, `llm.ToolCall` (already in v1/API.md).
2. **Wire types** (`apiChatRequest`, `apiMessage`, `apiToolCall`, etc.) — what crosses the network. In aikido, these are unexported in `llm/openrouter/api_types.go`.
3. **Conversion functions** — `toAPIChatRequest`, `fromAPIChatResult`, etc. (L341–L467). These survive verbatim, renamed and adapted to the streaming response shape.

The conversion-function pattern (one `to…` function per request type, one `from…` function per response type) is clean and worth lifting:

```go
// client.go L386-L427 — the conversion function shape worth keeping
func toAPIToolChatRequest(req *client.ToolChatRequest) *apiToolChatRequest {
    msgs := make([]apiToolCallMessage, len(req.Messages))
    for i, m := range req.Messages {
        msgs[i] = apiToolCallMessage{
            Role:       m.Role,
            Content:    m.Content,
            ToolCallID: m.ToolCallID,
        }
        if len(m.ToolCalls) > 0 {
            tcs := make([]apiToolCall, len(m.ToolCalls))
            for j, tc := range m.ToolCalls { /* ... */ }
            msgs[i].ToolCalls = tcs
        }
    }
    tools := make([]apiTool, len(req.Tools))
    for i, t := range req.Tools { /* ... */ }
    return &apiToolChatRequest{Model: req.Model, Messages: msgs, Tools: tools}
}
```

Aikido needs `toAPIRequest(req llm.Request) *apiRequest` doing exactly this: copy fields, convert tool defs, wire up cache hints (new in aikido), set `stream: true`, return the wire-shape. ~40 lines.

#### Adapt

| client.go | aikido `llm/openrouter` |
|---|---|
| Three methods: `Chat`, `ChatImage`, `ChatWithTools` (L44, L84, L149) | One method: `Stream(ctx, req)`. `Collect` (in `llm/`) drains the stream. |
| `httpClient.Timeout = 120 * time.Second` (L37–L38) | No timeout on http.Client; per-request deadline via ctx |
| `Chat` returns `*ChatResult` with `Response` and `DurationMs` | `Stream` returns `<-chan llm.Event`; usage is one event among many; duration is the caller's measure |
| `ChatImage` decodes data URIs, image parts (L84–L146, L473–L520) | Drop in v1 — image generation is v2 (per ROADMAP). Keep the `imagePart`/`contentPart`/`extractImageDataURI`/`decodeDataURI` code as a v2 reference snippet. |
| `ChatWithTools` separates messages with tool-call shape (L149–L186) | Folded into `Stream` — there is one request type, `llm.Request`, that always supports tools (empty `Tools` slice = no tools) |
| `zerolog/log` calls scattered through (L49, L67, L78, …) | Use `slog` via `Options.Logger`; if `Logger == nil`, no logging. Hub uses zerolog by repo convention; aikido cannot impose a logger. |
| `compile-time interface check: var _ client.LLMClient = (*Client)(nil)` (L30) | Same idiom: `var _ llm.Client = (*Client)(nil)` at top of `client.go` (per API.md convention) |
| Status-code handling: anything not 200 → error with body (L66–L69, L106–L109, L171–L174) | Inspect status: 401/403 → `ErrAuth`; 429 → `ErrRateLimited` (with retry-after parsing); 5xx → `ErrServerError`; 400 → `ErrInvalidRequest`; else generic |
| `doRequest` returning `(body, statusCode, err)` (L192–L213) | Survives, but reads SSE stream incrementally instead of `io.ReadAll`. Use `bufio.Scanner` over `resp.Body` for line-based SSE parsing. |
| Single API endpoint `/chat/completions` | Same |

#### Drop

- The `ChatImage` method and all image-related types (L84–L146, L473–L520). Move the snippet to a v2 implementation note; do not import in v1.
- `MaxTokens` and `Temperature` only on non-tool path (L226–L229). aikido `llm.Request` always has `MaxTokens` and `Temperature` fields.
- `provider` field on `Client` struct. Aikido's `Client` is OpenRouter; the model and prefs come per-request.

#### Snippet to mirror

The `doRequest` shape — adapted for streaming — is the body of `Stream`:

```go
// llm/openrouter/client.go (target shape)
func (c *Client) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
    body, err := json.Marshal(toAPIRequest(req))
    if err != nil { return nil, fmt.Errorf("openrouter: marshal: %w", err) }

    out := make(chan llm.Event, 16)

    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
    if err != nil { return nil, fmt.Errorf("openrouter: build request: %w", err) }
    httpReq.Header.Set("Content-Type", "application/json")
    httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
    httpReq.Header.Set("Accept", "text/event-stream")

    var resp *http.Response
    err = retry.Do(ctx, c.retry, func() error {
        var doErr error
        resp, doErr = c.httpClient.Do(httpReq)
        if doErr != nil { return doErr }
        if resp.StatusCode == 429 || resp.StatusCode >= 500 {
            // close body, return retriable error
            io.Copy(io.Discard, resp.Body); resp.Body.Close()
            return errRetriable(resp.StatusCode)
        }
        return nil
    })
    if err != nil { return nil, classify(err) }
    if resp.StatusCode != 200 { return nil, classifyStatus(resp.StatusCode, mustReadBody(resp)) }

    go c.streamSSE(ctx, resp.Body, out)
    return out, nil
}
```

Then `streamSSE` does the SSE parse + tool-call assembly per the DeepThought pattern (`Streaming-Tool-Call-Assembly-Pattern.md`).

---

### 6. `asolabs/hub/internal/client/openrouter/client_test.go` → `llm/openrouter/client_test.go`

**LOC:** 154.
**Verdict:** **inspiration.** Has the right idea (test conversions, test interface conformance, nil-request guards) but no httptest replays.

#### Lift verbatim

- The compile-time interface check pattern (L10–L13) is correct and makes a test out of what's conventionally a top-of-file `var _ I = (*T)(nil)` line:
  ```go
  func TestClientImplementsLLMClient(t *testing.T) {
      c := NewClient("test-key")
      var _ client.LLMClient = c
  }
  ```
  aikido should keep this. Belt and suspenders: both the top-of-file assertion and the test, in case the assertion gets accidentally deleted.
- Nil-request guards (L15–L46): each public method tested with `nil` request returning `(nil, error)`. Lift verbatim into `llm/openrouter` for `Stream(ctx, llm.Request{})` with empty model.
- Conversion-function tests (`TestToAPIChatRequest`, `TestFromAPIUsage`, `TestFromAPIChatResult`, L48–L153): lift the structure — assert each field on each conversion function. The aikido versions are `TestToAPIRequest`, `TestFromAPIUsage`, `TestFromAPIStreamChunk`.

#### Adapt

The test set is *missing* what aikido needs: an SSE-replay test. Add `httptest.Server` running canned transcripts from `testdata/`:

```go
// llm/openrouter/client_test.go (new)
func TestStream_SimpleText(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/event-stream")
        f, _ := os.Open("testdata/simple_text.sse")
        io.Copy(w, f); f.Close()
    }))
    defer srv.Close()
    c, _ := openrouter.NewClient(&openrouter.Options{APIKey: "test", BaseURL: srv.URL})
    events, err := c.Stream(context.Background(), llm.Request{Model: "x", Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
    require.NoError(t, err)
    var got []llm.Event
    for ev := range events { got = append(got, ev) }
    // assert sequence
}
```

#### Drop

Nothing — the existing tests carry over wholesale into aikido's test suite, augmented with SSE replay.

---

### 7. `asolabs/hub/internal/nexi/tools.go` → `agent/tools_builtin.go` (and as a model for caller tool-registration sites)

**LOC:** 547.
**Verdict:** **adapt.** This is *the* explicit-schema reference. The tool-def style and the `executeTool` switch are exactly the patterns aikido formalizes — but as a `tools.Registry` not a switch.

#### Lift verbatim

The tool-def shape (L28–L161). The schema-as-`json.RawMessage`-literal idiom is the explicit-style aikido adopts (per ADR-005). The format is canonical OpenAI-compatible:

```go
// nexi/tools.go L31-L35 — explicit-schema style worth lifting verbatim into both aikido tooling and example code
{
    Type: "function",
    Function: client.ToolFunction{
        Name:        "list_documents",
        Description: "List all documents in the workspace. Optionally filter by path prefix...",
        Parameters:  json.RawMessage(`{"type":"object","properties":{"path_prefix":{"type":"string","description":"Path prefix filter, optional"}}}`),
    },
},
```

aikido's `tools.Object`/`tools.String`/etc. helpers (per W4) generate exactly this shape. The example registration in tools.go is the format every caller will use:

```go
// caller-side (target shape with aikido helpers)
reg.Register(llm.ToolDef{
    Name:        "list_documents",
    Description: "List all documents in the workspace. Optionally filter by path prefix.",
    Parameters: tools.Object(map[string]any{
        "path_prefix": tools.String("Path prefix filter, optional"),
    }),
}, listDocumentsHandler)
```

The descriptions themselves (L33, L41, L49, L56, L64, L73, L91, L102, L111, L123, L133, L141, L149, L156) are well-written: 1–3 sentences, mention required vs optional, avoid jargon. Use them as the *style guide* for aikido's built-in tool descriptions.

#### Adapt

| nexi/tools.go | aikido equivalent |
|---|---|
| `toolDefs() []client.Tool` (L27) returning a slice | aikido tool registration is *imperative*: caller calls `reg.Register(def, handler)` per tool. The slice form is fine for callers but aikido itself uses the registry. |
| `executeTool(ctx, tc, name, argsJSON) string` (L165–L226) — the giant switch | Replaced by `tools.Registry.Dispatch(ctx, call, env) (Result, error)`: each tool registers a closure as its handler. No central switch. |
| `ToolContext{DocRepo, Repo, BusinessID, UserID}` (L20–L24) — the shared state available to tools | `tools.Env{Storage, Project, TurnID, Logger, Now}` — same shape, different fields, passed into each handler |
| String returns from each `execX` function (e.g., L228–L241) | Structured returns: `tools.Result{Content: any, Display: string, ChangedPaths: []string}` — agent serializes `Content` to JSON for the model |
| `truncate(s string, maxLen int) string` (L523–L528) | Lift verbatim into a shared util — `Display` strings should stay short. |
| `toolDescription(name string) string` mapping tool names to human descriptions (L487–L520) | Drop. aikido does not have a parallel "what is this tool doing in plain English" mapping. The `Display` field on `Result` plays this role; for `EventToolCall`, the caller can render `call.Name` directly or maintain its own mapping. |

#### Drop

- All domain-specific tools: `create_post`, `list_posts`, `get_post`, `update_post_schedule`, `update_post_status`, `archive_document`, `list_archived`, `get_social_accounts`, `complete_onboarding`. Caller code in `aikido/examples/agent-vfs` will *not* register these.
- The `toDatabasePost`/`fromDatabasePost`/`fromDatabasePostFull` ↔ `client.X` conversions (L295, L351, L376). Caller concerns.
- `parseDateTime` (L531–L546). Caller concerns.

#### Snippet to mirror

How aikido's built-in tool registration looks:

```go
// agent/tools_register.go (target shape)
func RegisterVFSTools(reg *tools.Registry, opts VFSToolOptions) error {
    if opts.MaxFileBytes == 0 { opts.MaxFileBytes = 1 << 20 } // 1 MiB
    // …apply other defaults…

    if err := reg.Register(llm.ToolDef{
        Name:        "read_file",
        Description: "Read the contents of a file at the given path. Returns content, content_type, and size.",
        Parameters: tools.Object(map[string]any{
            "path": tools.String("Relative file path"),
        }, "path"),
    }, makeReadFileHandler(opts)); err != nil {
        return err
    }
    // …repeat for write_file, list_files, delete_file, search_files…
    return nil
}
```

The `makeXHandler(opts)` closures capture `opts` so `MaxFileBytes`, `AllowedExtensions`, `HideHiddenPaths` are enforced at the handler level, not at registration.

---

### 8. `asolabs/hub/internal/nexi/agent.go` → `agent/run.go`

**LOC:** 206.
**Verdict:** **inspiration.** This is the *next-most-mature* version of the agent loop after `loop.go` — same pattern, but with structured event emission. Worth reading before writing aikido's agent because it shows the natural progression (callback → typed events).

#### Lift verbatim

The event vocabulary (`TextDelta`, `ToolCallStart`, `ToolCallResult`, `Done`, `Error`) maps directly to aikido's `EventText`, `EventToolCall`, `EventToolResult`, `EventEnd`, `EventError`. Match for match. The fact that this code already evolved this vocabulary independently is validation that aikido's event taxonomy is correct.

The history conversion (`historyToLLMMessages`, `llmMessageToHistory`, L153–L201) confirms what aikido's `RunWithMessages` will need: callers store history in some `[]Message` shape; the agent converts to `[]llm.Message` for the request, then converts the *response* back. aikido sidesteps this by making `[]llm.Message` the canonical shape — there is no separate `schema.AgentMessage`.

#### Adapt

| nexi/agent.go | aikido `agent` |
|---|---|
| `emit func(Event)` callback parameter (L77) | `<-chan agent.Event` return — same data flow, different transport. Per ADR-006 the channel form is preferred (backpressure, native consumption). |
| `Event interface { eventMarker() }` (types.go L4–L6) | Concrete struct `Event{Kind: …, Text/ToolCall/ToolResult/Usage/Err: …}` per API.md L432 — discriminated union, not interface. Reasons: cheaper to construct, no interface conversion, JSON-friendly when v3 SSE wrapper lands. |
| `result.Response.Choices[0].Message` shape (L109) — assumes single choice | Same in aikido — multi-choice is not a v1 concern |
| `truncate(toolResult, 200)` for `Summary` (L137) | aikido emits the full structured `ToolResult{Content: any, Error: string}` on the channel; the caller decides whether to truncate for display |
| `maxIterations = 25` (L13, package const) | `Options.MaxTurns` (default 20). The "25 vs 20" discrepancy is fine — pick aikido's default per ADR/PLAN; document where it came from. |
| Conversation lives on `Agent.history` (L23) | aikido's `runLoop` uses a *local* `msgs` slice; multi-turn callers pass history in via `RunWithMessages` per ADR-011. |

#### Drop

- The `BusinessID`/`UserID` fields on `Agent`. aikido is not multi-tenant; tenancy lives in the caller's `Storage` impl.
- `AgentConfig.SystemPrompt` baking into history at construction (L43–L48). aikido's agent prepends `Options.SystemPrompt` at every `Run`, so callers can change it between turns if they want.
- `truncate` summary trimming. aikido emits structured data; the rendering layer (caller's CLI/UI) decides what to show.

#### Snippet to mirror

The way `runLoop` should drain LLM events into `agent.Event`s:

```go
// agent/run.go (target shape — drain helper)
func drain(events <-chan llm.Event, out chan<- agent.Event) (text string, calls []llm.ToolCall, usage *llm.Usage) {
    var b strings.Builder
    for ev := range events {
        switch ev.Kind {
        case llm.EventTextDelta:
            b.WriteString(ev.Text)
            out <- agent.Event{Kind: agent.EventText, Text: ev.Text}
        case llm.EventToolCall:
            calls = append(calls, *ev.Tool)
            out <- agent.Event{Kind: agent.EventToolCall, ToolCall: ev.Tool}
        case llm.EventUsage:
            usage = ev.Usage
        case llm.EventError:
            out <- agent.Event{Kind: agent.EventError, Err: ev.Err}
        }
    }
    return b.String(), calls, usage
}
```

Note: tool calls are *not* dispatched here — the agent dispatches them after the LLM stream closes for the turn (per the ARCHITECTURE.md flow). `drain` only forwards events and accumulates the assistant message.

---

### 9. `asolabs/hub/internal/nexi/types.go` → `agent/event.go`

**LOC:** 67.
**Verdict:** **inspiration.** Confirms the event vocabulary. aikido restructures from interface to discriminated union.

#### Lift verbatim

Event names: `TextDelta`, `ToolCallStart`, `ToolCallResult`, `Done`, `Error`. aikido renames `Done` → `EventEnd` (with `EndReason` field) and merges `ToolCallStart` into `EventToolCall` (since the aikido event already carries the `*llm.ToolCall` payload).

#### Adapt

```go
// nexi/types.go (interface marker style)
type Event interface { eventMarker() }
type TextDelta struct { Content string }
func (TextDelta) eventMarker() {}
type ToolCallStart struct { Name, ID, Description string }
// …
```

vs.

```go
// aikido/agent/event.go (discriminated-union style)
type EventKind int
const (
    EventText EventKind = iota
    EventToolCall
    EventToolResult
    EventUsage
    EventError
    EventEnd
)
type Event struct {
    Kind       EventKind
    Text       string
    ToolCall   *llm.ToolCall
    ToolResult *ToolResult
    Usage      *llm.Usage
    Err        error
    EndReason  string
}
```

The discriminated union wins for: zero allocations (no interface boxing), JSON-friendly (one type, omitempty fields), exhaustive `switch` on `Kind` is enforceable via `go vet`-style tooling. The interface form wins for: type-safe field access (you can't read `Text` on an event that's actually a Usage). aikido picks the union per API.md L432–L441.

#### Drop

- `platformPrefixes` and `platformFullToPrefix` maps (L46–L66) — domain-specific, not aikido's concern.

---

### 10. `mxcd/go-basicauth/storage_interface.go` → `vfs/storage.go` + `vfs/txstorage.go`

**LOC:** 32.
**Verdict:** **lift the idiom.** This is the canonical pattern for "base interface + capability interface, detected via interface assertion."

#### Lift verbatim

The structure:

```go
// storage_interface.go L5-L31 — the idiom that aikido lifts
type Storage interface {
    CreateUser(user *User) error
    GetUserByUsername(username string) (*User, error)
    // ...
}

// AtomicBackupCodeConsumer is an optional capability. If your Storage
// implements it, the library uses it to consume backup codes race-free
// instead of the default read-modify-write via UpdateUser. This matters
// when the same backup code could be submitted by two concurrent requests —
// an atomic implementation guarantees exactly one of them succeeds.
// SQL-backed stores typically implement this via a conditional UPDATE
// (e.g. `UPDATE ... SET backup_codes = array_remove(..., $hash)
// WHERE id = $id AND $hash = ANY(backup_codes)`).
type AtomicBackupCodeConsumer interface {
    ConsumeBackupCodeHash(userID uuid.UUID, hash string) (bool, error)
}
```

The doc-comment on `AtomicBackupCodeConsumer` (L18–L26) is the model for `vfs.TxStorage`'s doc-comment. It explains: (a) what the optional capability does, (b) what the library does without it, (c) why a real backend would want to implement it.

aikido's analog:

```go
// vfs/txstorage.go (target shape)
package vfs

// TxStorage is an OPTIONAL capability. If your Storage implements it,
// the agent runs each turn's tool dispatches inside a transaction and
// commits-or-rolls-back based on whether any tool errored. This matters
// for production storage backends (Ent, Postgres) where partial-state
// corruption from a mid-turn error would otherwise survive.
//
// Storages that don't implement TxStorage run without transactional
// isolation; partial writes can survive turn errors.
//
// SQL-backed stores typically wrap a *sql.Tx; the bundled in-memory
// store wraps copy-on-begin / swap-on-commit semantics.
type TxStorage interface {
    Storage
    BeginTx(ctx context.Context, projectID uuid.UUID) (Tx, error)
}

type Tx interface {
    Storage
    Commit() error
    Rollback() error
}
```

The detection idiom (interface assertion) is a single line in `agent/run.go`:

```go
txStorage, hasTx := opts.Storage.(vfs.TxStorage)
```

This is the *exact* idiom from go-basicauth's session/handler code. aikido inherits the pattern verbatim.

#### Adapt

The base `Storage` interface for aikido (in v1/API.md L292–L304) has different *methods* — files instead of users — but the same *shape*: small base interface, full set of CRUD-equivalent methods, well-documented per method.

#### Drop

Nothing — this is the cleanest reference file in the set. Lift the comment style, the small-interface discipline, and the capability-extension pattern wholesale.

#### Snippet to mirror

The doc-comment block on `AtomicBackupCodeConsumer` (L18–L26 above). Every capability interface in aikido should follow this format: 1 sentence purpose, 1 sentence "what we do without it," 1 sentence example/implementation hint.

---

### 11. `mxcd/go-basicauth/storage_memory.go` → `vfs/memory/storage.go`

**LOC:** 167.
**Verdict:** **lift the shape.** Map-backed, `sync.RWMutex`-protected, multi-index storage. Same pattern, different keys (paths instead of usernames).

#### Lift verbatim

Construction shape (L11–L24):

```go
// storage_memory.go L11-L24 — exact shape worth carrying over
type MemoryStorage struct {
    users           map[uuid.UUID]*User
    usersByUsername map[string]*User
    usersByEmail    map[string]*User
    mu              sync.RWMutex
}

func NewMemoryStorage() *MemoryStorage {
    return &MemoryStorage{
        users:           make(map[uuid.UUID]*User),
        usersByUsername: make(map[string]*User),
        usersByEmail:    make(map[string]*User),
    }
}
```

Lock-with-defer pattern (L26–L29, L62–L63, L86–L87, etc.):

```go
func (s *MemoryStorage) CreateUser(user *User) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    // …
}
```

Same for read methods using `RLock`. aikido's `vfs/memory.Storage` will have the same shape:

```go
// vfs/memory/storage.go (target shape)
type Storage struct {
    projects map[uuid.UUID]*projectState
    mu       sync.RWMutex
}

type projectState struct {
    files     map[string]*fileEntry        // path -> entry
    snapshots map[vfs.SnapshotID]*snapshot // for Restore
}
```

#### Adapt

| go-basicauth | aikido vfs/memory |
|---|---|
| `usersByUsername`, `usersByEmail` secondary indexes (lowercase-normalized) | aikido has only one index per project: `path -> entry`. No secondary indexes for v1; if `search_files` becomes hot, that's a v2 concern. |
| `ConsumeBackupCodeHash` is the only capability method | `BeginTx` is the only capability method; returns a `Tx` that wraps a copy-on-begin snapshot of `projectState` and swaps it back on `Commit` |
| `ErrUserAlreadyExists`, `ErrUserNotFound` | `ErrProjectNotFound`, `ErrFileNotFound`, `ErrPathInvalid`, `ErrFileTooLarge` (per API.md L334–L337) |
| Unexported entries (everything below `MemoryStorage`'s public methods is method-receiver code) | Same — internal `fileEntry`, `snapshot`, `txState` types are unexported |

#### Drop

- Username/email lowercase normalization (L34, L42, L62, L73). aikido paths are case-sensitive — `ValidatePath` does not normalize case (storage backends like ext4 are case-sensitive too).
- TFA-specific fields and methods. Not relevant.

#### Snippet to mirror

The atomic operation pattern for `ConsumeBackupCodeHash` (L127–L144) is the model for `vfs.TxStorage` semantics:

```go
// storage_memory.go L127-L144 — atomic op under write lock
func (s *MemoryStorage) ConsumeBackupCodeHash(userID uuid.UUID, hash string) (bool, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    user, exists := s.users[userID]
    if !exists { return false, ErrUserNotFound }
    for i, h := range user.BackupCodeHashes {
        if h == hash {
            user.BackupCodeHashes = append(user.BackupCodeHashes[:i:i], user.BackupCodeHashes[i+1:]...)
            user.UpdatedAt = time.Now()
            return true, nil
        }
    }
    return false, nil
}
```

aikido's `BeginTx` does the analogous thing but for the whole project state: lock, copy `projectState`, return a `Tx` that operates on the copy. `Commit` swaps the copy in under lock. `Rollback` discards the copy.

---

### 12. `mxcd/go-basicauth/handler.go` → `agent/agent.go`, `notes/notebook.go`, every constructor

**LOC:** 533.
**Verdict:** **lift the constructor pattern.** The first 80 lines are the canonical "validate options → apply defaults → return constructed object or error" idiom.

#### Lift verbatim

The `NewHandler` constructor pattern (L36–L79) is the model for every `NewX(opts *Options) (*X, error)` in aikido:

```go
// handler.go L36-L79 — the constructor pattern that aikido lifts wholesale
func NewHandler(options *Options) (*Handler, error) {
    if options.Engine == nil {
        return nil, errors.New("gin engine is required")
    }
    if options.Storage == nil {
        return nil, errors.New("storage implementation is required")
    }

    if options.Settings == nil {
        options.Settings = DefaultSettings()
    }

    if len(options.Settings.SessionSecretKey) != 64 {
        return nil, errors.New("session secret key must be 64 bytes (for HMAC-SHA256)")
    }
    // …more validations…

    store := sessions.NewCookieStore(/* … */)

    return &Handler{
        Options:      options,
        sessionStore: store,
    }, nil
}
```

Steps, in order:

1. Validate required fields are non-nil/non-empty. Return error with one-sentence reason.
2. Apply defaults (`if X == nil { X = DefaultX() }`, or per-field zero-checks).
3. Validate derived state (e.g., key length).
4. Construct internal helpers.
5. Return concrete struct + nil.

aikido's `agent.NewAgent`, `openrouter.NewClient`, `tools.NewRegistry`, `notes.NewNotebook`, `vfs/memory.NewStorage` all follow this shape.

The key discipline: **error returns have `error` as the second return**, **defaults are applied silently**, **validation errors are user-readable** (no `%w`-wrapping a stdlib error here — the caller is creating a misconfigured object, not handling a failure mid-operation).

#### Adapt

Aikido's `Options` structs are smaller and per-package. `BasicAuthSettings` (in types.go L127–L150) has 18 fields — aikido's `agent.Options` has 9, `openrouter.Options` has 6. Don't bloat.

#### Drop

The settings-cascade pattern (L44–L46, where `options.Settings` defaults to `DefaultSettings()`). aikido's options are flat — there's no `Options.Settings.SubSettings`. Per-package `Options` keeps the structure flat.

#### Snippet to mirror

```go
// agent/agent.go (target shape)
func NewAgent(opts *Options) (*Agent, error) {
    if opts == nil { return nil, fmt.Errorf("agent: options required") }
    if opts.Client == nil { return nil, fmt.Errorf("agent: Client required") }
    if opts.Storage == nil { return nil, fmt.Errorf("agent: Storage required") }
    if opts.Model == "" { return nil, fmt.Errorf("agent: Model required") }

    if opts.MaxTurns == 0 { opts.MaxTurns = 20 }
    if opts.TurnTimeout == 0 { opts.TurnTimeout = 120 * time.Second }
    if opts.MaxTokens == 0 { opts.MaxTokens = 8192 }
    if opts.Tools == nil { opts.Tools = tools.NewRegistry() }
    if opts.Logger == nil { opts.Logger = slog.Default() }

    return &Agent{
        opts:  opts,
        locks: &sync.Map{},
    }, nil
}
```

---

### 13. `mxcd/go-basicauth/types.go` → all aikido types files + `errors.go` files

**LOC:** 222.
**Verdict:** **inspiration.** The error block at L203–L221 is the model for every aikido `errors.go`. The default-settings function (L152–L201) is the model for places where defaults need to be discoverable.

#### Lift verbatim

The error-block style (L203–L221):

```go
var (
    ErrInvalidCredentials   = errors.New("invalid credentials")
    ErrUserAlreadyExists    = errors.New("user already exists")
    ErrUserNotFound         = errors.New("user not found")
    // …
)
```

Top-of-file or dedicated `errors.go` per package. aikido's `vfs/errors.go`, `tools/errors.go`, `llm/errors.go` follow this exact shape.

The naming convention: `Err` + descriptive lowercase phrase, no period at end, lowercase first letter (per Go convention for error strings).

#### Adapt

Aikido errors more often use `%w`-wrapping than `errors.New`, because they need to carry context (HTTP status code, file path, etc.). `llm.ErrRateLimited` for example will commonly be wrapped:

```go
return fmt.Errorf("openrouter: %w (retry after %s)", llm.ErrRateLimited, retryAfter)
```

Callers do `errors.Is(err, llm.ErrRateLimited)` to detect it. The base `var Err… = errors.New(…)` is just the sentinel; specific errors wrap it.

#### Drop

The `Messages` struct (L93–L111). aikido does not localize messages — error strings are English, stable, and meant for logs/programmatic detection, not UI. Localization is the caller's UI concern.

---

### 14. `wilde/.../evaluator/api/internal/job/ai.go` → (anti-pattern reference)

**LOC:** 123.
**Verdict:** **drop.** Documented in PATTERNS.md L53 as an anti-pattern. aikido's design notes make several decisions in opposition to this file's choices.

#### What this file does

- Uses `sashabaranov/go-openai` SDK (L8) — third-party OpenAI client, not direct HTTP.
- Uses JSON Schema response-format mode (L92–L100) instead of tool calling — model returns structured JSON keyed by a schema.
- Non-streaming `CreateChatCompletion` (L55) — no SSE, no token streaming.
- Schema generated from a Go struct via `jsonschema.GenerateSchemaForType` (L39) — reflection.
- Custom `Detection` struct with `Confidence` floats etc. (L15–L23).

#### Why aikido doesn't do this

| ai.go decision | aikido's opposite decision | Rationale |
|---|---|---|
| `sashabaranov/go-openai` SDK | Direct HTTP via `net/http` (no SDK dep) | Per ADR-002 + DECISIONS comment "watch out: OpenRouter forwards Anthropic cache_control blocks but other providers may silently ignore"; SDKs hide this. Also per `Working with MaPa` reference: minimal deps. |
| JSON Schema response-format mode | Tool-call-based interaction | aikido's model: agents emit text + tool calls. Structured-output tasks in aikido are achieved by registering a tool whose handler validates and stores the structured result. JSON Schema mode is OpenAI-only and doesn't compose with mid-turn tool use. |
| Non-streaming `CreateChatCompletion` | Streaming `Stream(ctx, req) <-chan Event` | Per ADR-006. Streaming is the only mode; `Collect` drains it for non-streaming callers. |
| Schema from struct via `jsonschema.GenerateSchemaForType` | Both: `tools.SchemaFromType[T]()` (opt-in) AND explicit helpers | Per ADR-005, both styles are supported but explicit is the recommended primary path. The reflection-only approach in ai.go is brittle for unions/optional/enum. |
| Domain-specific `Detection` struct embedded in the package | aikido is library-only; domain types live in caller | Aikido owns provider plumbing, not user data shapes. |
| Opaque error types — wrap `error` from SDK without classification | Typed errors: `ErrAuth`, `ErrRateLimited`, `ErrServerError`, `ErrInvalidRequest` (per API.md L137–L142) | Callers need to detect "should I retry?" vs "should I prompt user to fix request?". Opaque wrapping forces string-matching. |

#### What to mirror, by inversion

When implementing `llm/openrouter`, every place where this file does X, aikido should *not* do X:

```go
// ai.go L55-L57 — what aikido avoids
response, err := j.aiClient.CreateChatCompletion(ctx, *j.getDetectionRequest(...))

// aikido equivalent:
events, err := c.Stream(ctx, llm.Request{
    Model: model, Messages: msgs, Tools: nil, // streaming, classified error, no SDK
})
text, _, _, err := llm.Collect(ctx, c, req)
```

#### Snippet — what NOT to mirror

```go
// ai.go L92-L100 — JSON-schema response-format mode (DO NOT MIRROR)
ResponseFormat: &openai.ChatCompletionResponseFormat{
    Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
    JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
        Name:   "detection_result",
        Schema: j.aiData.detectionResultSchema,
        Strict: true,
    },
},
```

The aikido way to achieve "give me back this structured shape": register a `record_detection` tool with parameters matching the desired shape; the model calls the tool with structured args; the handler stores them. Same outcome, agent-first, streaming-compatible, multi-call composable.

---

## Aggregate insights

### Naming conventions observed across MaPa's code

- **Receiver names are short, lowercase, repeating-first-letter:** `c *Client`, `s *Storage`, `a *Agent`, `h *Handler`. Carries into aikido verbatim.
- **Errors as package-level vars:** `var ErrFoo = errors.New("foo")`, named `Err` + descriptive lowercase phrase, gathered in `errors.go` or top of types.go. Already documented in DECISIONS.md style; lift across all aikido packages.
- **Compile-time interface assertions** at top of file: `var _ I = (*T)(nil)`. Used in `internal/client/openrouter/client.go` L30. aikido's API.md L178/L361/L362 already adopts this — keep.
- **Options structs go in `Options` *not* `Config`:** `openrouter.Options`, `agent.Options`, `notes.Options`, `BasicAuthSettings` (note: even the outlier "Settings" at the *handler* layer is wrapped by `Options`). Carries into aikido.
- **Constructors are `NewX(opts *Options) (*X, error)`** — never `NewX(field1, field2, …)` for objects with more than 1–2 inputs. Strict.
- **JSON tags use snake_case:** the wire types in `client.go` use `"prompt_tokens"` / `"completion_tokens"` / `"tool_call_id"`. aikido's wire types follow.
- **Boolean fields lean toward "Enable" / "Has" / "Required"** rather than "Disable*" — `EnableUsernameLogin`, `EnableTFA`, `Required`, etc. (handler.go types.go L128–L130).
- **`MaxX` for caps, `MinX` for floors:** `MaxIterations`, `MaxTurns`, `MaxFileBytes`, `MaxAttempts`, `MinLength`. aikido follows.
- **Preferred `time.Duration` over `int`** for timeouts: `SessionExpiration time.Duration`, never `SessionExpirationSeconds int`. aikido follows.
- **`Now func() time.Time` for injectable clocks** (per API.md L211 in `tools.Env`). Already in aikido — does not appear in references but is a Go-canonical pattern that fits the style.

### Error-wrapping idiom in production code

- `fmt.Errorf("doing X: %w", err)` on every layer. Specifically:
  - `loop.go` L231: `fmt.Errorf("chat request failed: %w", err)` — carries the cause
  - `openrouter.go` L116: `fmt.Errorf("marshal request: %w", err)` — short verb-noun phrase as prefix
  - `internal/client/openrouter/client.go` L57: `fmt.Errorf("marshaling request: %w", err)` — gerund form is also seen; mostly verb-noun
- The prefix is *never* the package name (no `"openrouter: marshal: %w"`); the call-site context tells you which package. aikido may want package-prefix for ambiguity reasons (`fmt.Errorf("openrouter: marshal request: %w", err)`) — confirm with MaPa, but the body of evidence here is no-prefix.
- Errors that cross the public API boundary (return from `Chat`, `Stream`, `Run`, etc.) are *always* wrapped at least once; never raw stdlib errors leak.
- **Status-code errors are formatted with body:** `fmt.Errorf("API returned status %d: %s", statusCode, string(respBody))` (client.go L68, L108). aikido refines this by classifying first (auth vs rate-limit vs server-error) and wrapping the typed sentinel.

### Test patterns worth carrying

- **Compile-time assertion as a test:** `func TestClientImplementsLLMClient(t *testing.T) { var _ client.LLMClient = c }` (client_test.go L10–L13). aikido carries this — both as test and as top-of-file `var _` line.
- **Nil-input guards have dedicated tests:** `TestChat_NilRequest`, `TestChatImage_NilRequest`, `TestChatWithTools_NilRequest` (client_test.go L15–L46). aikido does the same for `Stream(ctx, llm.Request{})` with empty model, etc.
- **Conversion functions get individual tests:** `TestToAPIChatRequest`, `TestFromAPIUsage`, `TestFromAPIChatResult` (client_test.go L48–L153). One test per conversion, asserting each field. aikido's `to/fromAPI…` functions get the same treatment.
- **No `httptest.Server`-replay style in the references** — this is *new* for aikido, mandated by W3 of PLAN.md. The reference codebase does not have canned-transcript tests for the OpenRouter client; aikido pioneers this. Inspiration source: `Streaming-Tool-Call-Assembly-Pattern.md` already lays out the test shape (cited in W3 risks).
- **No conformance suites** — aikido's `vfs.RunConformance` (per W5) is also new. The closest reference is unit tests on `MemoryStorage` directly; aikido extracts these into a reusable suite.

---

## Open questions surfaced

Things in the reference code that suggest a v1 detail not yet captured in PLAN.md or DECISIONS.md. Surfaced for MaPa to decide.

### Q1. `ProviderPreferences` — included in v1 or deferred?

**Surfaced from:** `agent-test/agent/openrouter.go` L52–L56 + L107–L112.

The production sandbox client supports OpenRouter's `provider` field for routing preference (`{order: ["anthropic"], allow_fallbacks: true}`). The aikido `llm.Request` per API.md L77–L85 has no provider-routing field. PLAN.md does not mention provider preferences.

**Tension:** OpenRouter's whole *value proposition* is provider routing. Callers may want to pin to a specific upstream when latency or cache-fidelity matter. Without this in v1, callers fall back to OpenRouter's default routing.

**Options:**
- **(a)** Defer to v2 (when direct providers land — provider routing matters less because callers can pick `llm/anthropic` directly).
- **(b)** Add `Options.ProviderOrder []string` on `openrouter.Options` (provider-specific, doesn't change `llm.Request`). v1-additive.
- **(c)** Add `Request.Extras map[string]any` for provider-specific passthrough fields. Most flexible, least typed.

**Recommendation:** (b) — least surface area, OpenRouter-specific, doesn't touch `llm.Client`. A 5-line addition to `openrouter.Options`.

### Q2. Error-wrapping prefix: package name or not?

**Surfaced from:** `agent-test/agent/openrouter.go` (no prefix: `"marshal request: %w"`) vs. emerging aikido convention in PLAN.md / API.md (which doesn't show actual error strings).

References use call-site context, not package prefix. But aikido's typed errors (`ErrAuth`, `ErrRateLimited`) are package-level — when wrapped by callers, ambiguity grows. Without prefix: `"openrouter request failed: rate limited"`. With prefix: `"openrouter: request failed: rate limited"`.

**Tension:** consistency with reference style vs. clarity in stack traces.

**Recommendation:** Prefix package name where the error originates. `fmt.Errorf("openrouter: marshal request: %w", err)`. Slightly more verbose, more debuggable. Confirm with MaPa.

### Q3. Tool-result content type: `string` or `any`?

**Surfaced from:** `executeTool` in `agent-test/agent/loop.go` L269 and `nexi/tools.go` L165 — both return `string`. API.md L218 (`Result.Content any`) and L425–L429 (`ToolResult.Content any`) — aikido uses `any`.

**Tension:** Production code returns `string` everywhere because the LLM API takes a string for tool messages. aikido's `any` then gets serialized to JSON — but if the model expects a string-formatted result, the JSON-serialized form may be uglier than a hand-written string. E.g., `tools.Result{Content: "wrote 42 bytes"}` JSON-serializes to `"wrote 42 bytes"` (with quotes) — fine. But `tools.Result{Content: map[string]any{"size": 42}}` serializes to `{"size":42}` — also fine, but the model has to parse it.

**Question:** does aikido accept that handler authors should return `map[string]any` / structs and aikido serializes? Or should `Result.Content` be `string` and the handler does its own formatting?

**Recommendation:** Keep `any`. Handlers that want a string return `tools.Result{Content: "..."}`; aikido serializes correctly (a string `any` json-marshals to `"..."`). Handlers that want structured output return a struct or map. The flexibility is cheap.

### Q4. Built-in `get_current_date` tool?

**Surfaced from:** Both `loop.go` L319 (`GetCurrentDate`) and `nexi/tools.go` L189 (`get_current_date`) include this tool. aikido's `agent/tools_builtin.go` per PLAN W7 does *not* include it (the table at L211–L216 lists only `read_file`, `write_file`, `list_files`, `delete_file`, `search_files`).

**Tension:** Both production agents needed it. Most agent loops will. But aikido-as-library should be opinionated about *not* injecting tools the caller didn't ask for.

**Recommendation:** No built-in. Document in `EXAMPLES.md` how callers register their own clock tool — this matches aikido's "library, not framework" stance per ADR-007 and ROADMAP.md. The 8 lines of code for a clock tool are not a burden for callers.

### Q5. `MaxIterations = 25` vs. aikido's `MaxTurns = 20`

**Surfaced from:** `loop.go` L11 and `nexi/agent.go` L13 both use 25; ARCHITECTURE.md (and PLAN W6) defaults to 20.

**Tension:** Trivial but worth flagging. If the production loops never hit 25 in practice, 20 is fine. If they have, the default should match.

**Recommendation:** Stick with 20 (per ADR's table). Document in PLAN.md or ADR that it's a conservative default; callers raise if they need to. Confirm with MaPa.

### Q6. `Message.Content *string` (pointer) vs. `string` (value)

**Surfaced from:** All reference code uses `*string` (pointer) for `Content` so it can be nil when the assistant emits only tool calls. API.md L52 has `Content string` (value).

**Tension:** Pointer = explicit nil for "no content", clear in JSON marshalling (`null` vs `""`). Value = simpler, but then "empty string + tool calls" is ambiguous from "no content + tool calls."

**Recommendation:** API.md is correct — `string`. The wire form (in `apiMessage` types) uses `*string` for JSON-null compatibility, but the public type is value-semantics. Rationale in DECISIONS could be added if MaPa wants belt-and-suspenders documentation.

### Q7. `ProviderPreferences` is the same shape as `Tools`/`ProviderOrder` — should this become a generic "extras" mechanism?

**Surfaced from:** `agent-test/agent/openrouter.go` L52–L56 + the broader observation that providers grow features at different paces (Anthropic adds caching options; OpenAI adds structured outputs; OpenRouter adds provider routing). A typed field per feature creates breaking-change pressure on `llm.Request`.

**Tension:** v1's `llm.Request` is locked (per W1). New provider features need somewhere to go. Either (a) every new feature is a new field on `llm.Request`, (b) provider-specific fields go on the provider's `Options`, (c) `llm.Request` gains a `Extras map[string]any` escape hatch.

**Recommendation:** (b) for v1. `openrouter.Options.ProviderOrder` is the prototype. If a future provider needs request-time (not client-time) overrides, that's a v2 design discussion.

### Q8. Storage path-validation strictness — write-time vs read-time

**Surfaced from:** `safePath` in `agent-test/agent/tools.go` L12 validates at every operation. aikido's `vfs.ValidatePath` is called inside built-in tools but not necessarily *inside* `Storage.WriteFile` itself.

**Tension:** If a custom `Storage` implementation skips path validation, malicious paths can leak through. The PATTERNS.md guidance ("hidden-path filter for `_*` defaults to on") is about display, not validation.

**Recommendation:** Make `vfs.ValidatePath(path)` *also* called inside `vfs/memory.Storage.WriteFile` etc. (defensive — even though the agent's tools call it first, the conformance suite then verifies that any backend behaves correctly when a malicious path bypasses the tool layer). Document this in W5's conformance suite.

### Q9. `Tools` field on `agent.Options` — pointer or value, mutable after construction?

**Surfaced from:** `agent-test/agent/loop.go` uses a package-level `var toolDefs` — fixed at compile time. `nexi/agent.go` calls `toolDefs()` per turn (L97) — implicitly fresh. aikido's API.md L391 has `Tools *tools.Registry` — a pointer, mutable.

**Tension:** Can callers mutate the registry between `Run` calls? E.g., add a tool mid-conversation? The pointer says yes; the docs don't say.

**Recommendation:** Document in `agent.NewAgent` godoc that the registry is read at `Run` time (not at `NewAgent` time), so callers may mutate between runs. But document also that mutating *during* a run is undefined (mid-loop tool addition is unsupported in v1). Confirm with MaPa.

---

## Summary of work

When implementing v1, the implementer should:

1. **Open `loop.go` while writing `agent/run.go`** — same flow, six steps. Replace each step's implementation per the table above. Aim for ~150 LOC in `run.go`.
2. **Open `internal/client/openrouter/client.go` while writing `llm/openrouter/client.go`** — keep the public-vs-wire two-layer architecture. Add SSE parsing per `Streaming-Tool-Call-Assembly-Pattern.md`. Remove `ChatImage`. Aim for ~250 LOC in `client.go` plus ~150 LOC in `api_types.go`.
3. **Open `nexi/tools.go` while writing `agent/tools_builtin.go`** — copy the explicit-schema description style for built-in tool descriptions. Replace `executeTool` switch with handler closures. Aim for ~200 LOC.
4. **Open `go-basicauth/storage_interface.go` while writing `vfs/storage.go` + `vfs/txstorage.go`** — lift the comment style on `AtomicBackupCodeConsumer` for `TxStorage`'s godoc. Aim for ~80 LOC across both.
5. **Open `go-basicauth/storage_memory.go` while writing `vfs/memory/storage.go`** — same constructor + sync.RWMutex pattern. Replace user-by-X indexes with project-by-uuid + path-keyed file maps. Aim for ~250 LOC.
6. **Open `go-basicauth/handler.go` (lines 36–79 only) while writing every aikido constructor** — same validate → defaults → return shape.
7. **Pin `evaluator/api/internal/job/ai.go` open as the negative reference** — every time you reach for an SDK or non-streaming or JSON-schema-response-mode, stop and reconsider.

The references are short and clean. Most of aikido v1 is mechanical lift-with-rename. The new code (SSE parsing, retry helper, conformance suite, `notes` package) is small and well-scoped.
