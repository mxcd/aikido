# v1 Public API Reference

Source of truth is the godoc on each type and function. This file is the human-readable summary of the v1 public surface — useful for design review, for users skimming the library before importing it, and for catching breakage during refactors.

This document reflects the **post-second-grilling** v1 surface (see [`planning/GRILL-RESULTS.md`](planning/GRILL-RESULTS.md) D12–D23 and ADRs 012–025 in [`../DECISIONS.md`](../DECISIONS.md)). The ADR-016 paper-mapping gate is closed: all surface changes derived from the Anthropic walk are folded in below.

The conventions throughout:

- Every constructor is `NewX(opts *Options) (*X, error)`.
- Compile-time interface conformance is asserted with `var _ I = (*T)(nil)` lines at the top of implementing files.
- Errors are wrapped with context at every layer boundary (`fmt.Errorf("...: %w", err)`).
- `Scope`, `SessionID`, and similar identifiers are opaque `string` values. Callers that use UUIDs serialize them via `String()` before handoff.
- `EventKind` is `string`-typed (not `iota`) so additions are order-independent and serialize cleanly to logs.
- Optional configuration fields use pointer-to-primitive (`*float32`) or pointer-to-struct (`*CacheBreakpoint`) to distinguish "unset / use provider default" from "explicitly set."

---

## Package `llm`

Provider-agnostic types and the `Client` interface every provider satisfies.

```go
package llm

import (
    "context"
    "encoding/json"
)

// Role is a chat message role.
type Role string

const (
    RoleSystem    Role = "system"
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
    RoleTool      Role = "tool"
)

// ImagePart is one image attached to a message.
type ImagePart struct {
    URL         string // remote URL or data URI
    ContentType string // populated when constructing from bytes
}

// ToolCall is one tool invocation from the model. Arguments is complete JSON
// (assembled client-side from streaming fragments).
type ToolCall struct {
    ID        string
    Name      string
    Arguments string
}

// CacheTTL enumerates Anthropic's two ephemeral-cache TTLs. Empty TTL on a
// CacheBreakpoint means "5m" by provider convention. Other providers may
// silently ignore cache breakpoints (OpenRouter forwards to Anthropic-routed
// models; OpenAI direct ignores).
type CacheTTL string

const (
    CacheTTL5Min  CacheTTL = "5m"
    CacheTTL1Hour CacheTTL = "1h"
)

// CacheBreakpoint marks a message as a cache breakpoint on providers that
// support it. The breakpoint lands on the last content block of the message.
// Multi-block-positioned breakpoints are a v1.x consideration.
//
// nil = no breakpoint. &CacheBreakpoint{} = breakpoint with default 5m TTL.
type CacheBreakpoint struct {
    TTL CacheTTL
}

// Message is one entry in the conversation.
type Message struct {
    Role       Role
    Content    string             // empty allowed (assistant with only tool calls)
    Images     []ImagePart        // user / assistant only
    ToolCalls  []ToolCall         // assistant only
    ToolCallID string             // tool role only — ID this message replies to
    Cache      *CacheBreakpoint   // nil = no breakpoint; non-nil = anthropic-style cache_control hint
}

// ToolDef is one tool the model may call. Parameters is a JSON Schema.
type ToolDef struct {
    Name        string
    Description string
    Parameters  json.RawMessage
}

// Usage records token consumption and cost for one provider call.
type Usage struct {
    PromptTokens     int
    CompletionTokens int
    CacheReadTokens  int
    CacheWriteTokens int  // populated by providers that report cache writes (Anthropic); 0 elsewhere
    CostUSD          float64 // populated by providers that report cost (OpenRouter); 0 elsewhere
}

// ThinkingEffort is the OpenAI-style coarse reasoning-effort enum. Empty
// string is unset (use provider default).
type ThinkingEffort string

const (
    ThinkingEffortLow    ThinkingEffort = "low"
    ThinkingEffortMedium ThinkingEffort = "medium"
    ThinkingEffortHigh   ThinkingEffort = "high"
)

// ThinkingConfig configures provider-side thinking / reasoning. Use one of
// the constructors to build one — the internal fields are unexported so
// callers cannot accidentally set both an effort and a budget.
//
// Provider mapping:
//   - Anthropic direct: budget wins. If only effort is set, the client
//     derives a budget (low: 1024, medium: 8192, high: 32768).
//   - OpenAI / OpenRouter (OpenAI-shape): effort wins as `reasoning_effort`.
//     Budget is ignored.
type ThinkingConfig struct {
    effort ThinkingEffort
    budget int
}

// ThinkingByEffort returns a ThinkingConfig configured by coarse effort.
func ThinkingByEffort(e ThinkingEffort) *ThinkingConfig

// ThinkingByBudget returns a ThinkingConfig configured by an explicit
// Anthropic-style token budget. Non-Anthropic providers fall back to a
// derived effort or ignore.
func ThinkingByBudget(n int) *ThinkingConfig

// Float32 returns a pointer to v. Convenience helper for SessionOptions
// fields that take *float32 (Temperature) so callers can write inline values.
func Float32(v float32) *float32

// Request is one provider call.
type Request struct {
    Model         string
    Messages      []Message
    Tools         []ToolDef
    MaxTokens     int
    Temperature   *float32         // nil = provider default; non-nil = explicit, clamped per provider range
    Thinking      *ThinkingConfig  // nil = no thinking; non-nil per the ThinkingConfig docs
    StopSequences []string
}

// EventKind identifies the kind of a streaming event. String-valued so new
// kinds can be added in any order without renumbering existing callers.
type EventKind string

const (
    EventTextDelta EventKind = "text_delta"
    EventToolCall  EventKind = "tool_call"   // emitted only after fragments fully assembled
    EventThinking  EventKind = "thinking"    // emitted by providers that surface reasoning fragments
    EventUsage     EventKind = "usage"
    EventError     EventKind = "error"
    EventEnd       EventKind = "end"         // always last; channel closes after
)

// Event is one streaming event emitted by a Client.
type Event struct {
    Kind  EventKind
    Text  string     // EventTextDelta or EventThinking
    Tool  *ToolCall  // EventToolCall
    Usage *Usage     // EventUsage
    Err   error      // EventError
}

// Client is the only interface a provider must implement.
type Client interface {
    Stream(ctx context.Context, req Request) (<-chan Event, error)
}

// Collect drains a stream into a final result. Useful for non-streaming callers.
// Returns text accumulated from EventTextDelta, all complete tool calls,
// final Usage if the provider emitted one, and the first error encountered.
// EventThinking text is not included in the returned text.
func Collect(ctx context.Context, c Client, req Request) (text string, calls []ToolCall, usage *Usage, err error)

// Errors returned by providers and helpers.
var (
    ErrAuth           error // wrap when provider returns 401/403
    ErrRateLimited    error // wrap when provider returns 429
    ErrServerError    error // wrap when provider returns 5xx
    ErrInvalidRequest error // wrap when provider returns 400
)
```

> **No model catalog in v1.** Per ADR-025, `Catalog`, `FindModel`, `Model`, and `ErrUnknownModel` are not part of the v1 surface. Model facts and pricing are caller-side concerns; OpenRouter returns `usage.cost` natively, which is sufficient for v1's single-provider scope. Per-provider catalogs are a v2 concern.

---

## Package `llm/openrouter`

OpenRouter implementation. Streaming-first; tool-call assembly; 429/5xx retry at stream-start. SSE parser is inlined inside this package (ADR-022 — not extracted as `internal/sseparse/`).

```go
package openrouter

import (
    "context"
    "net/http"

    "github.com/mxcd/aikido/llm"
)

// Options configure the OpenRouter client.
type Options struct {
    APIKey        string         // required
    BaseURL       string         // default "https://openrouter.ai/api/v1"
    HTTPClient    *http.Client   // default: timeout 0 — streams may be long-lived
    HTTPReferer   string         // optional OpenRouter ranking attribution
    XTitle        string         // optional OpenRouter ranking attribution
    ProviderOrder []string       // optional OpenRouter `provider` routing preference
}

// Client is an OpenRouter implementation of llm.Client.
type Client struct{ /* ... */ }

// NewClient constructs an OpenRouter client. Returns an error if APIKey is empty.
func NewClient(opts *Options) (*Client, error)

var _ llm.Client = (*Client)(nil)

// Stream sends one request to OpenRouter and yields events as they arrive.
// The channel closes when the stream terminates (EventEnd always emitted last).
func (c *Client) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error)
```

---

## Package `llm/llmtest`

Public test helpers. Promoted from `internal/testutil/` so library consumers can use the same stub `llm.Client` aikido itself uses for agent tests (ADR-022).

```go
package llmtest

import (
    "context"

    "github.com/mxcd/aikido/llm"
)

// StubClient is a scriptable llm.Client for tests.
// Each Run call consumes one TurnScript from the script.
type StubClient struct{ /* ... */ }

// TurnScript is the events the stub emits for one Stream call.
// Tests build []TurnScript to script multi-turn conversations.
type TurnScript struct {
    Events []llm.Event
}

// NewStubClient returns a StubClient that will play the given turn scripts in order.
func NewStubClient(turns ...TurnScript) *StubClient

// Stream emits the next TurnScript's events. Returns ErrStubExhausted if no
// scripts remain.
func (s *StubClient) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error)

var _ llm.Client = (*StubClient)(nil)

// ErrStubExhausted is returned when Stream is called with no remaining scripts.
var ErrStubExhausted error
```

---

## Package `tools`

Tool registry, dispatch, and schema helpers (explicit only — ADR-018).

```go
package tools

import (
    "context"
    "encoding/json"

    "github.com/google/uuid"
    "github.com/mxcd/aikido/llm"
)

// Env carries per-call execution context to a Handler. Slimmed per ADR-021:
// only fields that genuinely change per dispatch live here. Tools that need a
// logger, a clock, or a storage handle capture them at registration via closure.
type Env struct {
    SessionID string
    TurnID    uuid.UUID
}

// Result is what a Handler returns. Content is JSON-serialized to the model.
type Result struct {
    Content any
    Display string // optional human-readable summary
}

// Handler is the function signature every tool implements.
type Handler func(ctx context.Context, args json.RawMessage, env Env) (Result, error)

// Registry holds tool definitions and their handlers.
type Registry struct{ /* ... */ }

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry

// Register adds a tool. Returns ErrDuplicateTool if the name is taken.
func (r *Registry) Register(def llm.ToolDef, h Handler) error

// Defs returns all registered ToolDefs in registration order.
func (r *Registry) Defs() []llm.ToolDef

// Dispatch routes a ToolCall to its handler.
// Returns ErrUnknownTool if the name is not registered.
func (r *Registry) Dispatch(ctx context.Context, call llm.ToolCall, env Env) (Result, error)

// Has reports whether a tool is registered.
func (r *Registry) Has(name string) bool

// Schema helpers — explicit style.
func Object(props map[string]any, required ...string) json.RawMessage
func String(description string) map[string]any
func Integer(description string) map[string]any
func Number(description string) map[string]any
func Boolean(description string) map[string]any
func Enum(description string, values ...string) map[string]any
func Array(items any, description string) map[string]any

// Errors.
var (
    ErrDuplicateTool error
    ErrUnknownTool   error
)
```

For struct-driven schemas, callers use `invopop/jsonschema` directly (recipe in `tools/doc.go`):

```go
import jsonschema "github.com/invopop/jsonschema"
schema, _ := json.Marshal(jsonschema.Reflect(&FooArgs{}))
```

---

## Package `vfs`

Pluggable storage for AI-managed projects. Minimal four-method base contract (ADR-017) with optional capabilities for scoping (ADR-013) and search (ADR-020).

```go
package vfs

import (
    "context"
    "time"
)

// FileMeta describes one file's metadata.
type FileMeta struct {
    Path        string
    ContentType string
    Size        int64
    UpdatedAt   time.Time
}

// Storage is the base contract every VFS backend implements.
type Storage interface {
    ListFiles(ctx context.Context) ([]FileMeta, error)
    ReadFile(ctx context.Context, path string) ([]byte, FileMeta, error)
    WriteFile(ctx context.Context, path string, content []byte, contentType string) error
    DeleteFile(ctx context.Context, path string) error
}

// ScopedStorage is an OPTIONAL capability. Backends that multiplex tenants
// (or any other namespace) implement Scope to return a scope-bound Storage.
// The library never sees a scope parameter on Storage methods — callers
// pre-bind via Scope and hand the resulting Storage to RegisterVFSTools.
type ScopedStorage interface {
    Scope(scope string) Storage
}

// Searchable is an OPTIONAL capability. Backends that support native search
// implement it; the built-in `search` tool is registered only when the
// supplied storage satisfies this interface. SearchSyntax returns
// human-readable documentation of the query language the backend accepts;
// the tool description embeds it so the model knows what queries are valid.
type Searchable interface {
    Search(ctx context.Context, query string) (paths []string, err error)
    SearchSyntax() string
}

// ValidatePath enforces aikido-wide structural invariants on a path:
// no `..` segments, no absolute paths, no null bytes, length <= 512 bytes,
// no empty path. Caller-policy filters (max bytes, allowed extensions,
// hidden-path filtering) live on agent.VFSToolOptions, not here.
func ValidatePath(path string) error

// RunConformance executes the conformance test suite against a Storage factory.
// Implementations test their backend by calling this from a TestX function.
// Searchable-related sub-tests run only when the factory returns a Storage
// that satisfies vfs.Searchable.
func RunConformance(t *testing.T, factory func() Storage)

// Errors.
var (
    ErrPathInvalid     error
    ErrFileNotFound    error
    ErrFileTooLarge    error
)
```

---

## Package `vfs/memory`

In-memory backend. Satisfies `Storage` and `Searchable` (case-insensitive substring with optional glob filter — see `SearchSyntax()` output for the documented form).

```go
package memory

import (
    "github.com/mxcd/aikido/vfs"
)

// Storage is the in-memory implementation.
type Storage struct{ /* ... */ }

// NewStorage returns an empty in-memory Storage.
func NewStorage() *Storage

var (
    _ vfs.Storage    = (*Storage)(nil)
    _ vfs.Searchable = (*Storage)(nil)
)
```

---

## Package `agent`

Session-based streaming agent loop, pluggable Locker, History, and built-in VFS tools.

```go
package agent

import (
    "context"
    "log/slog"
    "time"

    "github.com/mxcd/aikido/llm"
    "github.com/mxcd/aikido/tools"
    "github.com/mxcd/aikido/vfs"
)

// History is the pluggable conversation-history interface (ADR-014).
// SessionID is opaque to aikido; implementations use it as a key into
// whatever store the caller has. v1 ships an in-memory backend under
// agent/history/memory.
//
// Append is variadic so the agent can flush a turn's worth of messages
// (user, assistant, tool results) in a single backend round-trip.
type History interface {
    Append(ctx context.Context, sessionID string, msgs ...llm.Message) error
    Read(ctx context.Context, sessionID string) ([]llm.Message, error)
}

// Locker provides mutual exclusion keyed by session ID. The Session acquires
// a lock on its ID before each Run begins (covering History.Read at turn
// start) and releases it after EventEnd. The unlock function returned by
// Lock must be called exactly once; the agent always calls it via defer.
//
// Implementations must be safe for concurrent use. v1 ships
// agent.NewLocalLocker for in-process coordination. Multi-replica
// deployments implement Locker themselves (Redis-backed, etc.) without
// changing the agent.Session call shape; v1.1 ships agent/locker/redis.
type Locker interface {
    Lock(ctx context.Context, sessionID string) (unlock func(), err error)
}

// LocalLocker is the in-process implementation of Locker. Suitable for
// single-replica production use and tests. Memory grows with the number of
// distinct session IDs seen; call Forget(id) to drop a key once the session
// is finished.
type LocalLocker struct{ /* ... */ }

// NewLocalLocker constructs an in-process Locker.
func NewLocalLocker() *LocalLocker

func (l *LocalLocker) Lock(ctx context.Context, sessionID string) (unlock func(), err error)
func (l *LocalLocker) Forget(id string)

var _ Locker = (*LocalLocker)(nil)

// SessionOptions configure a Session.
type SessionOptions struct {
    ID           string          // required; opaque; History keys on this
    Client       llm.Client      // required
    Tools        *tools.Registry // optional; nil means a Session with no tools
    History      History         // required; use agent/history/memory.NewHistory() for in-process
    Locker       Locker          // required; use agent.NewLocalLocker() for single-replica
    Model        string          // required
    SystemPrompt string

    MaxTurns        int           // default 20
    RunTimeout      time.Duration // default 10m; total wall-clock cap for one Run; 0 = no cap
    LLMCallTimeout  time.Duration // default 180s; per-provider-call cap; 0 = no cap
    MaxTokens       int           // default 16384; provider-call output cap
    Temperature     *float32      // nil = provider default; use llm.Float32(0) for explicit deterministic

    Logger *slog.Logger // optional; the Session may log internal events at its discretion (no contract in v1)
}

// Session bundles a session ID, a model + system prompt, a tool registry,
// History plug-in, and Locker plug-in. Multiple Run calls share the
// session's history, serialized by the Locker.
type Session struct{ /* ... */ }

// NewSession constructs a Session. Returns an error if any required field
// (Client, History, Locker, Model, ID) is empty.
func NewSession(opts *SessionOptions) (*Session, error)

// NewLocalSession is a convenience constructor for single-replica deployments
// and tests. It is equivalent to NewSession but auto-supplies in-memory
// implementations for History and Locker if the caller leaves them unset.
//
// For multi-replica deployments, or any setup needing a custom Locker
// (e.g., Redis-backed in v1.1) or a persistent History (e.g., Postgres-backed),
// use NewSession directly and supply your own plug-ins.
//
// History defaults to an in-memory store; conversation is lost when the
// process restarts.
func NewLocalSession(opts *SessionOptions) (*Session, error)

// EventKind identifies the kind of an agent.Event. String-valued for
// order-independent additions (see llm.EventKind for rationale).
type EventKind string

const (
    EventText       EventKind = "text"
    EventThinking   EventKind = "thinking"
    EventToolCall   EventKind = "tool_call"
    EventToolResult EventKind = "tool_result"
    EventUsage      EventKind = "usage"
    EventError      EventKind = "error"
    EventEnd        EventKind = "end"
)

// EndReason values emitted on EventEnd.
const (
    EndReasonStop      = "stop"
    EndReasonMaxTurns  = "max_turns"
    EndReasonError     = "error"      // also covers History I/O errors and Lock-acquire timeouts
    EndReasonTimeout   = "timeout"    // RunTimeout or LLMCallTimeout exhaustion
    EndReasonCancelled = "cancelled"
)

// ToolResult is one tool execution result.
type ToolResult struct {
    CallID  string
    Name    string
    OK      bool
    Content any
    Error   string
}

// Event is one streaming event from a session run.
type Event struct {
    Kind       EventKind
    Text       string         // EventText or EventThinking
    ToolCall   *llm.ToolCall  // EventToolCall
    ToolResult *ToolResult    // EventToolResult
    Usage      *llm.Usage     // EventUsage
    Err        error          // EventError
    EndReason  string         // EventEnd
}

// Run executes one agent turn from a single user message. The user message
// and the assistant's response (including any tool calls and tool results)
// are accumulated for one variadic History.Append at the end of the turn.
// The returned channel closes after EventEnd.
//
// On EndReasonError (including History I/O failure or Lock-acquire timeout)
// the History is NOT updated for this turn — the caller knows to retry,
// and the durable transcript stays consistent.
func (s *Session) Run(ctx context.Context, userText string) (<-chan Event, error)

// RunWithMessages is the lower-level escape hatch for callers that maintain
// their own history shape (trimming, branched conversations, agent-as-subroutine).
// The history slice is the conversation so far, excluding the system prompt
// (which Run prepends from SessionOptions.SystemPrompt). RunWithMessages does
// not append to History — the caller owns the message log here and uses
// agent.Drain to assemble the produced messages.
func (s *Session) RunWithMessages(ctx context.Context, history []llm.Message) (<-chan Event, error)

// Drain consumes events from the channel until it closes and returns the
// assembled assistant message followed by tool-result messages, in the
// order they were produced this turn. Returns the first error encountered
// on EventError.
//
// Drain is the recommended way for RunWithMessages callers to obtain the
// turn's output messages for appending to their own history store. Callers
// who also need streaming (e.g., to a UI) fan out the channel themselves
// before passing one branch to Drain.
func Drain(events <-chan Event) ([]llm.Message, error)

// VFSToolOptions configure the built-in VFS tools.
type VFSToolOptions struct {
    Storage           vfs.Storage // required; pre-bound to scope if multi-tenant
    HideHiddenPaths   bool        // hide _* and . paths from list/search; default true
    AllowedExtensions []string    // nil = allow all
    MaxFileBytes      int64       // default 1 MiB
}

// RegisterVFSTools registers read_file, write_file, list_files, delete_file
// into the supplied registry. If opts.Storage satisfies vfs.Searchable, also
// registers `search`. Tool handlers capture opts.Storage via closure (ADR-021).
func RegisterVFSTools(reg *tools.Registry, opts *VFSToolOptions) error
```

---

## Package `agent/history/memory`

Bundled in-memory History backend. Suitable for single-replica deployments and tests; messages are lost when the process restarts.

```go
package memory

import (
    "github.com/mxcd/aikido/agent"
)

// History is the in-memory implementation.
type History struct{ /* ... */ }

// NewHistory returns an empty in-memory History.
func NewHistory() *History

var _ agent.History = (*History)(nil)
```

The conformance suite for `History` lives in `agent/history.RunConformance(t, factory)`.

---

## What is intentionally not in v1

Each has its deferral rationale in [DECISIONS.md](../DECISIONS.md):

- `notes` — note-then-consolidate package (ADR-015). Pattern recipe lives in `PATTERNS.md`.
- `vfs.TxStorage` and `vfs.Tx` — transactional turn semantics (ADR-019). Candidate for v1.x.
- `vfs.Storage.Snapshot` / `Restore` / `HashState` — caller-facing utilities the library never used (ADR-017). Caller-side concern.
- `tools.SchemaFromType[T]()` — reflective schema generation (ADR-018). Use `invopop/jsonschema` directly.
- `retry` as a public package — moved to `internal/retry/` (ADR-022).
- `internal/sseparse/` — SSE parser stays inline in `llm/openrouter/` (ADR-022).
- `llm.Catalog`, `llm.FindModel`, `llm.Model`, `llm.ErrUnknownModel` — no v1 catalog (ADR-025). Token / cost tracking infrastructure is a v1.x or v2 concern, likely per-provider.
- `agent/locker/redis` — Redis-backed Locker (ADR-024). Ships in v1.1 with an abstract `Client` interface so aikido does not import a Redis client library. Multi-replica callers can implement `agent.Locker` themselves before v1.1 lands.
- `cmd/aikido` — CLI binary (ADR-010). Joins v2.
- `image`, `audio/stt`, `audio/tts` — modalities. Joins v2.
- `llm/anthropic`, `llm/openai` — direct providers. Joins v2.
- `vfs/local`, `vfs/postgres` — additional storage backends. v1 ships memory only (ADR-008).
- Structured-event logging contract on `Logger` — `Logger` is plumbed but its emission contract is intentionally not specified in v1.0 (lean stance). Real `OnEvent` hooks and a stable slog event vocabulary land in v1.x once a real caller proves the need.
