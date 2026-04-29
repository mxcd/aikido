# Architecture

How aikido fits together. Read [README.md](README.md) for the elevator pitch and [v1/API.md](v1/API.md) for the public surface in detail.

## Package map

```
github.com/mxcd/aikido/
├── llm/                          # provider-agnostic types + Client interface
│   ├── openrouter/               # first provider implementation (with inline SSE parser)
│   └── llmtest/                  # public test helpers — StubClient, canned scripts
├── tools/                        # tool registry + dispatch + explicit schema helpers
├── vfs/                          # Storage interface (4 methods) + capabilities + path validation
│   └── memory/                   # in-process Storage implementation
├── agent/                        # Session-based streaming loop, Locker, History, built-in VFS tools
│   ├── history/
│   │   └── memory/               # in-memory History backend
│   └── locker/                   # (v1.1) agent/locker/redis lands here with abstract Client interface
├── internal/
│   └── retry/                    # internal exponential backoff helper
└── examples/
    ├── chat-oneshot/             # one-shot completion via llm.Collect
    └── agent-vfs/                # session over memory VFS with built-in tools
```

The dependency graph is strict: `llm` knows nothing about `vfs`, `vfs` knows nothing about `agent`, `agent` orchestrates all three plus `tools`. `agent.History` and `agent.Locker` are parallel pluggable interfaces, both shipped with bundled in-memory backends. Nothing depends on `examples`.

Cut from v1 (deferred to v1.x or later, per DECISIONS.md):

- `notes/` package — see ADR-015. Pattern documented as a recipe in `PATTERNS.md`.
- `retry/` as a public package — see ADR-022. Now `internal/retry/`.
- `internal/sseparse/` — see ADR-022. SSE parser stays inline in `llm/openrouter/`.
- `internal/testutil/` — see ADR-022. `StubClient` promoted to public `llm/llmtest/`.
- `agent/locker/redis` — see ADR-024. Ships in v1.1 with an abstract `Client` interface.
- `llm.Catalog` / `llm.FindModel` / `llm.Model` — see ADR-025. No v1 catalog; per-provider catalogs are a v2 concern.

## Data flow

A single `Session.Run` call produces this flow:

```
caller ──► session.Run(ctx, userText)
            │
            ├── derives RunTimeout-bounded ctx
            ├── Locker.Lock(ctx, sessionID)         # blocks if another Run holds the lock
            ├── reads existing history via History.Read(ctx, sessionID)
            ├── builds llm.Request from history + system prompt + tool defs + new user message
            ├── llm.Client.Stream(ctx, req)         # wrapped in LLMCallTimeout-bounded ctx
            │      └─► provider implementation streams provider events
            │             └─► assembles partial tool-call fragments
            │                    └─► emits llm.Event{TextDelta|Thinking|ToolCall|Usage}
            │
            ├── forwards llm events to caller's <-chan agent.Event
            ├── accumulates assistant message (text + tool calls) in a local slice
            ├── if model emitted tool calls:
            │      ├── tools.Registry.Dispatch(call, env) for each call
            │      │      └─► tool handler uses closure-captured state (e.g., scope-bound Storage)
            │      ├── tool results accumulated; emitted to caller as agent.EventToolResult
            │      └── (no transactional wrap — each storage op is atomic at backend level; ADR-019)
            │
            ├── loop until model emits no tool calls (end_turn)
            │       or MaxTurns or RunTimeout reached
            │
            ├── History.Append(ctx, sessionID, userMsg, assistantMsg, ...toolMsgs)   # single variadic flush
            └── unlock()                            # via defer
```

The caller never sees raw provider events. The agent's `Event` channel is the public surface; provider events stay internal to `llm/openrouter`.

## The agent loop

Pseudocode (Go-shaped):

```
Session.Run(ctx, userText) -> <-chan Event:
    runCtx, cancel := WithTimeout(ctx, s.opts.RunTimeout)
    out := make(chan Event, 16)
    go runLoop(runCtx, cancel, out, userText)
    return out

runLoop:
    defer close(out)
    defer cancel()

    unlock, err := s.opts.Locker.Lock(runCtx, s.id)
    if err: emit(EventError, EventEnd(EndReasonError)); return
    defer unlock()

    history, err := s.opts.History.Read(runCtx, s.id)
    if err: emit(EventError, EventEnd(EndReasonError)); return     # strict policy, ADR-023

    msgs := append([systemMsg(s.opts.SystemPrompt)], history...)
    msgs = append(msgs, userMsg(userText))

    appended := []llm.Message{userMsg(userText)}                   # accumulator for end-of-turn flush

    for turn := 0; turn < s.opts.MaxTurns; turn++:
        turnID := uuid.New()
        callCtx, callCancel := WithTimeout(runCtx, s.opts.LLMCallTimeout)

        events, err := s.opts.Client.Stream(callCtx, llm.Request{
            Model: s.opts.Model, Messages: msgs, Tools: s.opts.Tools.Defs(),
            MaxTokens: s.opts.MaxTokens, Temperature: s.opts.Temperature, ...
        })
        if err: callCancel(); emit(EventError, EventEnd(EndReasonError)); return

        text, thinking, calls, usage := drain(events, out)         # forwards TextDelta/Thinking verbatim
        callCancel()

        assistantMessage := assistantMsg(text, calls)
        msgs = append(msgs, assistantMessage)
        appended = append(appended, assistantMessage)

        if usage != nil: emit(EventUsage, usage)

        if len(calls) == 0:
            err := s.opts.History.Append(runCtx, s.id, appended...)
            if err: emit(EventError, EventEnd(EndReasonError)); return
            emit(EventEnd(EndReasonStop)); return

        env := tools.Env{SessionID: s.id, TurnID: turnID}

        for call in calls:
            res, err := s.opts.Tools.Dispatch(runCtx, call, env)
            tr := buildToolResult(call, res, err)
            emit(EventToolCall(call), EventToolResult(tr))
            toolMsg := toolResultMsg(call.ID, tr)
            msgs = append(msgs, toolMsg)
            appended = append(appended, toolMsg)
            # Tool errors do not abort the loop — model self-corrects on next turn.

    # MaxTurns exhausted
    err := s.opts.History.Append(runCtx, s.id, appended...)
    if err: emit(EventError, EventEnd(EndReasonError)); return
    emit(EventEnd(EndReasonMaxTurns))
```

Notes on this shape:

- **Locker scope is the entire `Run`.** Acquired before `History.Read` at turn-start, released after `EventEnd`. Two concurrent `Run` calls with the same session ID serialize via the Locker — even if they execute on two separate `*Session` values (the natural webapp shape: fresh `*Session` per HTTP request, shared `Locker` from app startup). Multi-replica deployments swap in a distributed `Locker` (Redis-backed in v1.1, or a custom impl built on the `Locker` interface).
- **History flush is once per turn.** Variadic `Append` carries the user message, the assistant message, and any tool-result messages produced this turn — one backend round-trip instead of K+2 (ADR-014 amendment). On `EndReasonError` the flush does **not** happen, keeping the durable transcript consistent (no half-recorded turns).
- **History I/O errors are terminal.** Any `History.Read` or `History.Append` error emits `EventError + EventEnd(EndReasonError)` and the channel closes (ADR-023). The agent does not retry History calls; retry budget belongs to the `History` impl.
- **Two timeouts apply.** `RunTimeout` bounds the entire `Run` call (default 10m). `LLMCallTimeout` bounds each individual provider call (default 180s) so a stuck LLM call cannot consume the whole `RunTimeout` budget. Both exhaust to `EndReasonTimeout`.
- **No transactional wrap.** Each tool's storage op is atomic at the backend level (ADR-019). If a use case needs multi-write atomicity, ship a single tool that performs the multi-write atomically.
- **No projectID anywhere.** Tools that need scoping captured a scope-bound `Storage` at registration via closure (ADR-013).
- **`tools.Env` carries only `SessionID` and `TurnID`** — fields that genuinely change per dispatch (ADR-021). Tools needing logger / clock / storage capture them via closure at registration.

### Hard guardrails

The agent enforces these unconditionally; the values are caller-tunable defaults but always active.

| Guard | Default | Purpose |
|-------|---------|---------|
| `MaxTurns` | 20 | Prevent runaway tool loops. |
| `RunTimeout` | 10m (`0` = no cap) | Total wall-clock cap for one `Run` call. |
| `LLMCallTimeout` | 180s (`0` = no cap) | Per-provider-call cap; protects `RunTimeout` from a single stuck call. |
| `MaxTokens` | 16384 | Per-LLM-call output cap. |
| Per-session lock | `agent.Locker` | One `Run` at a time per session ID, regardless of how many `*Session` values exist. |

Tool errors do **not** abort the loop. They are returned to the model as tool results so the model can correct course. The agent only ends on: `end_turn` from model (`stop`), exhausted turns (`max_turns`), wall-clock exceeded (`timeout` — RunTimeout or LLMCallTimeout), unrecoverable provider / History / Locker error (`error`), or caller cancellation (`cancelled`).

### Concurrency model

Concurrency is bounded at the session granularity, via a pluggable `Locker` interface (ADR-024). The contract: `Locker.Lock(ctx, sessionID) (unlock, err)` blocks until the lock is acquired (or `ctx` cancels), and `unlock()` must be called exactly once. The agent always calls `unlock` via `defer`.

v1 ships `agent.LocalLocker` (in-process, per-id `sync.Mutex` map). It is production-grade for single-replica deployments. For multi-replica deployments, callers implement `Locker` themselves (against Redis, etcd, Postgres advisory locks, or anything else); v1.1 ships `agent/locker/redis` with an abstract `Client` interface so aikido does not import a Redis client library.

The previous "per-Session struct mutex" design (locked by ADR-012, before this revision) only worked when callers cached one `*Session` instance per ID. Pluggable `Locker` removes that hidden invariant: the lock lives behind a shared interface that even fresh-per-request `*Session` values converge on, as long as they were constructed with the same `Locker`. Naive callers — webapps that build a new `*Session` per HTTP request — get correct concurrency for free.

### Retry policy

LLM call retries happen at **stream-start only**. Once the provider has begun emitting tokens, retrying replays the same tokens and re-bills the user; the agent forbids it. Mid-stream errors propagate as `EventError` and end the turn with `EndReason = "error"`. The provider client (`llm/openrouter`) handles 429 and 5xx with exponential backoff before stream open via the internal `retry` package (ADR-022).

The agent never retries `History` or `Locker` calls. Both have errors that are terminal for the current `Run` (ADR-023). Backends that want retry-on-transient-error implement it inside their `History` / `Locker` impls.

## Storage capability model

The `vfs.Storage` interface is the minimal four-method contract: `ListFiles`, `ReadFile`, `WriteFile`, `DeleteFile` (ADR-017). Optional capabilities extend it via interface assertion or registration-time binding:

```go
// Optional: tenant/namespace multiplexing — backends that scope by an
// external identifier implement this and return a scope-bound Storage.
type ScopedStorage interface {
    Scope(scope string) Storage
}

// Optional: backend-native search — registers the built-in `search` tool
// only when the supplied storage satisfies this interface.
type Searchable interface {
    Search(ctx context.Context, query string) (paths []string, err error)
    SearchSyntax() string
}
```

Callers wire scope at registration time: `storage := backend.Scope("business-42")` returns a plain `Storage` that the VFS tool registration captures via closure. The library never sees a scope parameter on any storage method (ADR-013).

There is no transactional `Tx` capability in v1. Each tool's storage operation is atomic at the backend level (whatever atomicity `WriteFile` / `DeleteFile` provide individually); per-turn rollback is not part of the agent's contract (ADR-019). Use cases that need atomic-multi-write semantics ship the multi-write as a single tool. `TxStorage` is a candidate to re-introduce additively in v1.x if a second use case proves the abstraction is real.

This mirrors `go-basicauth`'s capability pattern: the base interface is small, optional capabilities extend it, consumers detect them via interface assertion or registration-time binding.

### Path validation: structural vs. policy

Two kinds of constraints apply to paths the model emits, and they live in two different places on purpose:

- **Structural invariants** (`vfs.ValidatePath`). aikido-wide: no `..` segments, no absolute paths, no null bytes, length cap, no empty path. Every backend must respect these. They are properties of "what is a valid aikido path," not of any particular caller's policy.
- **Caller-policy filters** (`agent.VFSToolOptions`). Per-use-case: max bytes (`MaxFileBytes`), allowed extensions (`AllowedExtensions`), hidden-path filtering (`HideHiddenPaths`). Different deployments want different limits; these live on the per-tool-options struct and are enforced inside the built-in tool handlers, not the `vfs` package.

`vfs.ValidatePath` runs in two places: inside the built-in VFS tools (before any storage call) and inside `vfs/memory.Storage`'s write paths as defense-in-depth. User-implemented `Storage` backends should call `vfs.ValidatePath` on entry to protect against bugs in custom tools.

## Streaming model

Both the LLM client and the agent surface streams as `<-chan Event` rather than callbacks or io.Readers. Reasons:

- Native Go consumption — `for ev := range stream` is the idiomatic shape.
- Backpressure for free — buffered channel (cap 16 internally) blocks the producer when the consumer is slow.
- Cancellation through context — when the caller cancels, the producer goroutine sees it on the next `select` and closes the channel.
- A future SSE-over-HTTP wrapper (v3) becomes a thin adapter that reads the channel and writes `text/event-stream` lines.

The provider implementation is responsible for assembling streaming fragments into complete events. Specifically: tool-call arguments arrive in fragments indexed by tool-call position; the client buffers them and emits `EventToolCall` only when the model marks the choice done. Consumers always receive complete tool calls with valid JSON arguments.

Callers that want the assembled message log produced by a `Run` — typically `RunWithMessages` callers — use `agent.Drain(events) ([]llm.Message, error)`, which consumes the channel and returns the assistant message followed by tool-result messages in order. Callers who also want streaming-to-UI fan out the channel themselves before passing one branch to `Drain`.

## Configuration model

aikido uses **per-package Options structs** with no global state.

```go
client, _   := openrouter.NewClient(&openrouter.Options{APIKey: "sk-..."})
storage     := vfsmem.NewStorage()
registry    := tools.NewRegistry()
_ = agent.RegisterVFSTools(registry, &agent.VFSToolOptions{Storage: storage})

// Single-replica quickstart (in-memory History + LocalLocker auto-supplied):
session, _ := agent.NewLocalSession(&agent.SessionOptions{
    ID:           "session-789",
    Client:       client,
    Tools:        registry,
    Model:        "anthropic/claude-sonnet-4-6",
    SystemPrompt: "You are a helpful research assistant.",
})

events, _ := session.Run(ctx, "Make a notes file with three facts about Go.")
```

Every constructor takes `*Options` and returns `(*X, error)`. Options carry only the fields that constructor needs; defaults are applied inside the constructor. No init() side effects, no environment reads — callers do that themselves (typically via `mxcd/go-config`).

For multi-tenant backends, the caller binds scope at registration time:

```go
backend  := hub.NewVFS()                       // implements ScopedStorage
storage  := backend.Scope("business-42")       // returns scope-bound Storage
agent.RegisterVFSTools(registry, &agent.VFSToolOptions{Storage: storage})
```

For multi-replica deployments, the caller supplies a custom `Locker` and (typically) a persistent `History`:

```go
session, _ := agent.NewSession(&agent.SessionOptions{
    ID:      sessionID,
    Client:  client,
    Model:   "anthropic/claude-sonnet-4-6",
    History: pghistory.NewHistory(db),     // caller-implemented agent.History
    Locker:  redislocker.NewLocker(opts),  // caller-implemented agent.Locker (or v1.1's bundled redis Locker)
})
```

The `Session` itself is unaware of scope. Library invariant: scope is opaque, caller-defined, library-uninterpreted (ADR-013).

## Note-then-consolidate pattern (recipe, not a v1 package)

The note-then-consolidate pattern — write atomic notes during a conversation, consolidate them into a coherent document on demand — is documented as a recipe in `PATTERNS.md`. Per ADR-015, it is not a first-class v1 package; the library's primitives (`tools.Registry` + `vfs.Storage` + `llm.Client`) are sufficient to implement it in caller code. The pattern is a candidate for v1.x promotion once at least one production validation outside the original PRD generator exists.

## Testing strategy

The library is testable without network access.

- **`vfs/conformance_test.go`** — a reusable test suite any `Storage` implementation can run against. The bundled memory backend uses it; users can use it on their own backends. Tests for the optional `Searchable` capability run only when the factory returns a backend that satisfies it.
- **`agent/history/conformance_test.go`** — a reusable test suite any `History` implementation can run against (parallel to the VFS conformance suite).
- **Locker tests in `agent`** — verify that two `Run` calls on different `*Session` values with the same ID serialize when they share a `Locker`, and run in parallel for different IDs.
- **Canned SSE transcripts** under `llm/openrouter/testdata/` — pre-recorded provider responses (simple text, single tool, multi-tool interleaved fragments, 429-then-success, mid-stream error). Tests run against an `httptest.Server` replaying these.
- **`llm/llmtest.StubClient`** — a public scriptable `llm.Client` for agent tests, also reusable by aikido consumers testing their own integrations. Tests script the model's behavior turn-by-turn and assert the session's event sequence and final VFS state.
- **Examples must build** — every file under `examples/` compiles in CI.
- **Real smoke tests** — manual, with `OPENROUTER_API_KEY` set: run `examples/chat-oneshot` and `examples/agent-vfs` against the real provider; assert event-sequence shape.

See [CONTRIBUTING.md](CONTRIBUTING.md) for dev setup and PR conventions.
