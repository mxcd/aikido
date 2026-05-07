# Architecture Decisions

Numbered, dated ADRs. Each one paragraph context, one paragraph decision, one paragraph consequences. New decisions append; superseded decisions are marked but not deleted.

---

## ADR-001 — Single Go module, multiple packages

**Date:** 29.04.2026
**Status:** Accepted

**Context.** aikido is a library plus, eventually, a CLI binary. A multi-module repo (separate `go.mod` for `cmd/aikido`) avoids forcing library consumers to download cobra and friends. A single-module repo halves release friction.

**Decision.** Single `go.mod` at the repo root. The CLI binary, when it lands in v2, lives under `cmd/aikido/`. Go's per-package dependency tracking ensures library consumers who only import `github.com/mxcd/aikido/llm` (etc.) do not transitively pull cobra.

**Consequences.** One version tag covers everything. The `cmd/aikido` directory is built only by `go build ./cmd/aikido`; library users never see it. If we ever publish the CLI separately (e.g., via Homebrew with its own version cadence), we can split into multi-module then — Go modules handle this gracefully.

---

## ADR-002 — OpenRouter as the v1 provider

**Date:** 29.04.2026
**Status:** Accepted (supplemented by ADR-016: paper-mapping pass against direct Anthropic before W1 freeze validates the abstraction shape)

**Context.** Direct provider clients (Anthropic, OpenAI, Mistral) give cache-control fidelity and provider-native features. OpenRouter is one HTTP API surface that fronts dozens of models with a stable OpenAI-compatible shape. v1 needs to ship; supporting multiple direct providers in v1 multiplies the surface area without proportional value.

**Decision.** v1 ships exactly one provider: `llm/openrouter`. Direct provider packages (`llm/anthropic`, `llm/openai`) are v2.

**Consequences.** v1 callers get model portability for free — `Model: "anthropic/claude-sonnet-4-6"` to `Model: "openai/gpt-4o"` is one string change. Callers who need provider-specific features (Anthropic's full prompt caching API, OpenAI's structured outputs) wait for v2. The `llm.Client` interface is provider-agnostic; v2 packages slot in without breaking v1 callers. **Watch out:** OpenRouter forwards Anthropic `cache_control` blocks but other providers may silently ignore them. v1 passes `Message.CacheHint` through and tolerates silent ignores; v2's `llm/anthropic` is the fallback when caching fidelity matters.

---

## ADR-003 — Custom Go agent loop, not Claude Agent SDK

**Date:** 29.04.2026
**Status:** Accepted

**Context.** The Claude Agent SDK is a Node-based loop with battle-tested streaming and reliability. Adopting it would mean shipping a Node sidecar in MaPa's single-binary Go deployment model. A custom Go loop is roughly 200–300 LOC (per `Custom-Go-Agent-Loop-Over-SDK-Pattern.md`) and gives full control over caching, metering, and concurrency — but loses the SDK's tested edge cases.

**Decision.** Custom Go agent loop in `agent/`. Rationale: single-binary deploy with no Node sidecar, specialized tool set (VFS-aware), full control over prompt caching and metering, and per-project transaction semantics that the SDK does not model.

**Consequences.** aikido owns its loop's correctness. Tests must cover error paths the SDK normally covers: provider mid-stream errors, tool dispatch errors, max-turn exhaustion, concurrent turn rejection. Reference loop already runs in production at `asolabs/hub/sandbox/agent-test/agent/loop.go` — aikido generalizes that, it does not invent.

---

## ADR-004 — Interface-based Storage, not callback functions

**Date:** 29.04.2026
**Status:** Accepted (capability-pattern idiom retained; specific `TxStorage` capability dropped from v1 per ADR-019; scoping capability `ScopedStorage` added per ADR-013)

**Context.** The user's spec said "callback system like in go-basicauth." On inspection, `github.com/mxcd/go-basicauth` is interface-based, not callback-based: users implement a `Storage` interface and inject the value via an `Options` struct. Callback-style (`StoreUser func(...) error`, `GetUser func(...) (*User, error)`) gives marginal flexibility and trades away type-safety, discoverability via godoc, and grouped-method semantics.

**Decision.** Mirror the go-basicauth idiom: aikido defines a `vfs.Storage` interface; users implement it; users inject the implementation via `agent.Options.Storage`. Optional capabilities (transactions) extend the base interface as separate interfaces (`vfs.TxStorage`) detected via Go interface assertion.

**Consequences.** Users get compile-time guarantees that their backend satisfies the contract. godoc lists the methods together with their docs. Test mocks are easy. The capability-interface pattern (`TxStorage`, future `vfs.Searchable`, etc.) lets the library grow without breaking existing implementations.

---

## ADR-005 — JSON Schema for tools: explicit primary, reflection optional

**Date:** 29.04.2026
**Status:** Superseded in part by ADR-018 — v1 ships explicit-only; reflective `SchemaFromType[T]()` is removed and the `invopop/jsonschema` dependency is dropped. Reflection may return additively in v1.x.

**Context.** Tool registration needs a parameter schema. Two options: explicit JSON Schema literals (verbose, type-loose, full control) or reflection from Go struct tags (terse, type-safe-ish, brittle for unions/optional/enum). MaPa's existing tool registrations in `asolabs/hub` use explicit literals.

**Decision.** Both. Explicit helpers (`tools.Object`, `tools.String`, `tools.Enum`, ...) are the primary path and match existing code. A generic `tools.SchemaFromType[T]() json.RawMessage` helper, backed by `invopop/jsonschema`, is shipped as an opt-in alternative for users who prefer struct-driven schemas for simple tools.

**Consequences.** The library covers both ergonomics styles without forcing one. The reflection helper carries a third-party dep (`invopop/jsonschema`) but it is only pulled in when users actually call `SchemaFromType`. Documentation makes the explicit form the recommended default; reflection is documented as "use when your input shape is genuinely a flat Go struct."

---

## ADR-006 — Streaming-first LLM client

**Date:** 29.04.2026
**Status:** Accepted

**Context.** Provider APIs offer both streaming and non-streaming responses. A library could make non-streaming the default and offer streaming as a separate method; or make streaming the only mode and expose a `Collect` helper that drains a stream into a final result.

**Decision.** Streaming is the only mode. `llm.Client.Stream(ctx, req)` returns `<-chan llm.Event`. A `llm.Collect` helper drains a stream into a `(text, calls, usage)` triple for callers who don't need streaming.

**Consequences.** All consumers — including non-streaming callers — flow through the same code path. There is no second non-streaming path to keep in sync. Streaming use cases (TTS pipelines, live UI updates) are first-class. Non-streaming callers pay a tiny channel-overhead cost that is dwarfed by network latency.

---

## ADR-007 — Notes as a first-class library construct

**Date:** 29.04.2026
**Status:** Superseded for v1 by ADR-015 — notes package is deferred to v1.x. The pattern is documented as a recipe in `PATTERNS.md` until N≥2 production validations exist outside the PRD generator.

**Context.** The user identified a load-bearing pattern: agents work better when they write atomic short notes during interactions and consolidate them into a target document on demand, rather than rewriting the full document every turn. This pattern can live in user code (with documentation) or in the library (with a dedicated package).

**Decision.** First-class. The `notes` package ships a `Notebook` type plus three pre-built tools (`add_note`, `list_notes`, `consolidate_notes_into_doc`). Notes are stored as VFS files under `_notes/{turn-uuid}.md`. Consolidation reads all notes, asks the LLM to merge them into a target document, writes back, and clears the consumed notes.

**Consequences.** The friction the library is meant to remove (wiring boilerplate) is gone with one `notes.RegisterTools(registry, notebook)` call. Notes share a storage backend with documents, so snapshots cover them and users can browse them. The default consolidation prompt is opinionated; users override via the `instructions` argument.

---

## ADR-008 — v1 ships memory VFS only

**Date:** 29.04.2026
**Status:** Accepted

**Context.** Bundled VFS backends (`vfs/memory`, `vfs/local`, `vfs/postgres`) lower the barrier to entry but expand the v1 surface and pull dependencies into CI. Users running aikido in production will supply their own Ent-based storage anyway.

**Decision.** v1 ships exactly one backend: `vfs/memory`. It satisfies both `Storage` and `TxStorage` (memory transactions are trivial). Users implement the `Storage` interface for their production backend.

**Consequences.** Smallest possible v1 surface. The conformance test suite (`vfs/conformance_test.go`) is reusable by user backends. Anyone wanting to play with the agent locally without a database has the memory backend; anyone wanting persistence wires their own. v2 may add reference backends if user demand justifies it.

---

## ADR-009 — Per-project mutex, in-process; defer multi-instance to v3

**Date:** 29.04.2026
**Status:** Superseded by ADR-012 — concurrency control moves from "per-project mutex" to "per-Session mutex." Two `Session.Run` calls on the same `Session` serialize; different sessions run in parallel. Multi-instance coordination remains a v3 concern (now session-scoped, not project-scoped).

**Context.** Concurrency is bounded per projectID: one turn at a time prevents partial-state corruption. The mutex can be in-process (`sync.Map[uuid.UUID]*sync.Mutex`) or distributed (Redis-backed via `redislock`). In-process is simpler and works for single-instance deployments; distributed is required for horizontal scaling.

**Decision.** v1 uses an in-process per-project mutex. The agent does not expose a `Locker` interface in v1. v3 introduces `agent.Options.Locker` as an additive option and ships a Redis-backed default; in-process mutex remains the default if `Locker` is nil.

**Consequences.** v1 deployments are explicitly single-instance. Documentation says so. Horizontally-scaled deployments wait for v3, or implement their own coordination outside aikido in the meantime.

---

## ADR-010 — Library only in v1; CLI joins v2

**Date:** 29.04.2026
**Status:** Accepted

**Context.** The user said "image generation is the first thing for the CLI" and also "image gen is v2." v1 could ship a chat/agent CLI now and add image gen in v2, or skip the CLI entirely until v2.

**Decision.** No CLI in v1. The `cmd/aikido` directory does not exist until v2.

**Consequences.** v1 has zero CLI-related dependencies (cobra etc.) anywhere in the module. v2 adds the CLI as a single new directory; all existing v1 packages are imported as-is. The user's framing — image gen as the headline CLI use case — is honored without the v1 scope creeping.

---

## ADR-011 — Stateless agent turns; caller maintains history

**Date:** 29.04.2026
**Status:** Superseded by ADR-012 (Session model) and ADR-014 (History as pluggable interface). Reason: forcing every caller to write the same `messages`-table adapter, the same turn-ID issuance, and the same pruning logic is exactly the boilerplate the library is meant to remove. v1 ships a `History` interface with a bundled memory backend, parallel to the `vfs.Storage` idiom. `RunWithMessages` is retained as an escape hatch for callers needing full control of what the LLM sees per turn.

**Context.** An agent loop must work with conversation history. The library could persist history itself (forcing a `messages` table or equivalent on every storage backend) or expect callers to maintain history and pass it in.

**Decision.** Stateless. `agent.Run(ctx, projectID, userText)` creates a one-turn conversation (system prompt + user message). For multi-turn, callers use `agent.RunWithMessages(ctx, projectID, history)` where `history` is a `[]llm.Message` they manage themselves.

**Consequences.** The `vfs.Storage` interface stays clean: it deals with files only, not message history. Callers who already have a `messages` table (typical in production) reuse it. Callers who do not get to choose where history lives. The agent's surface area stays minimal in v1; agent-managed history is a v1.5+ concern with its own storage interface.

---

## ADR-012 — Session as the multi-turn unit; `Scope` opaque and optional; `projectID` removed

**Date:** 29.04.2026
**Status:** Accepted

**Context.** The locked surface keyed on `projectID uuid.UUID` everywhere — `agent.Run(ctx, projectID, userText)`, `Storage.WriteFile(ctx, projectID, ...)`, the per-project mutex, the snapshot key, the notes scope key. A single typed parameter was doing four jobs at once with no single coherent meaning, and the term "project" leaked from `asolabs/hub`'s domain model. In aikido, callers' domain identifiers vary: ASO Nexus Hub uses business UIDs, the PRD generator uses PRD session IDs, a chatbot over a shared knowledge base has no boundary at all.

**Decision.** Two orthogonal concepts replace `projectID`:

- **`Session`** — the unit of multi-turn interaction. A Session bundles a session ID (opaque string), a model + system prompt, a tool registry, and a `History` plug-in (ADR-014). Multiple `Run` calls on the same Session share its history. This is the OpenAI-Assistants/Thread shape. The per-project mutex becomes a per-session mutex on the `Session` struct.
- **`Scope`** — an opaque caller-defined string used only by tools that need a tenant or namespace boundary. Library never interprets it. Callers map it to whatever domain term they have. Scope is orthogonal to session: a session may have no scope, one scope, or different scopes per scoped tool.

The `projectID uuid.UUID` parameter is removed from every public surface. Each of its four old jobs either moves to its rightful owner or disappears.

**Consequences.** `agent.Agent` / `NewAgent` / `Run(ctx, projectID, userText)` / `RunWithMessages` are restructured into `agent.Session` / `NewSession` / `Session.Run`. `vfs.Storage.CreateProject` and `ProjectExists` are removed. The `tools.Env.Project uuid.UUID` field is removed. `agent/safety.go` per-project mutex map disappears (mutex lives on the `Session` struct). Supersedes ADR-009 and ADR-011.

---

## ADR-013 — Storage scoping is a capability, not a parameter

**Date:** 29.04.2026
**Status:** Accepted

**Context.** Two real shapes coexist: (a) shared knowledge bases with no tenant boundary (chatbot reading company mission docs), and (b) multi-tenant backends scoped by external identifiers (Hub writing per-business files). Forcing (a) to invent a fake "global" scope on every method is ergonomic theatre; forcing (b) to embed scope inside path strings loses the boundary.

**Decision.** The `vfs.Storage` interface is scope-agnostic. Backends that multiplex tenants implement an additional capability interface, `vfs.ScopedStorage`, with a single method `Scope(scope string) Storage` returning a scope-bound view of the backend. Callers wire scope at registration time, then hand the resulting plain `Storage` to `agent.RegisterVFSTools`. The library never sees a scope parameter on any storage method.

**Consequences.** Every `Storage` method signature loses its scope/projectID parameter. `Scope(scope string)` is documented as expected to be cheap (a thin wrapper / view, not a connection or setup call). VFS tool implementations always call `storage.WriteFile(ctx, path, content, ct)` regardless of whether the backend is scoped. Mirrors the `TxStorage` capability pattern from ADR-004; same idiom, different capability.

---

## ADR-014 — History is a pluggable interface; v1 bundles a memory backend

**Date:** 29.04.2026
**Status:** Accepted (amended 30.04.2026 — variadic `Append`, single flush at end of turn, History required, narrow contract honestly named)

**Context.** ADR-011 made callers own history. Every realistic caller would write a `messages`-table adapter, the same turn-ID issuance, and the same pruning logic. Some of that — the messages-table adapter shape and turn-ID issuance — is real boilerplate the library should remove. Pruning policy, on closer read, is *not* — it varies fundamentally per app (token-budget vs semantic-importance vs sliding-window vs LLM-summarize), and the library has neither the information nor the right to pick one.

**Decision.** Library defines:

```go
type History interface {
    Append(ctx context.Context, sessionID string, msgs ...llm.Message) error
    Read(ctx context.Context, sessionID string) ([]llm.Message, error)
}
```

`sessionID` is opaque; the implementation uses it as a key. The `Session` reads history at turn start, calls the LLM with that history plus the new user message, accumulates the turn's produced messages (assistant + tool results) in a local slice, and flushes them all in **one variadic `Append` call** at end of turn. v1 ships an in-memory backend under `agent/history/memory`. `History` is **required** in `SessionOptions` — no nil-fallback. `agent.NewLocalSession` auto-supplies the in-memory backend for single-replica use. `RunWithMessages` is retained as an escape hatch for callers needing full control of what the LLM sees per turn (trimming, branched conversations, agent-as-subroutine); it does not append to History — the caller drains the resulting events via `agent.Drain` and stores the messages on their own.

**What this buys callers (honestly):**
- The canonical `[]llm.Message` shape and a sessionID-keyed contract — so every caller's persistence adapter looks the same.
- An in-memory backend and conformance suite for tests.
- One backend round-trip per turn instead of K+2 (variadic Append).

**What it does NOT buy callers:** pruning is still implemented by each caller's `History.Read` (a Postgres-backed History trims to its own policy before returning). The library cannot remove that work — every app's policy differs. The `History` interface is genuinely narrow.

**On error.** Any `History.Read` or `History.Append` failure is terminal for the current `Run`; see ADR-023.

**Consequences.** New `agent.History` interface and bundled memory implementation. Conformance suite (same shape as `vfs/conformance.go`). Supersedes ADR-011. The "we removed all the boilerplate" framing of the original ADR-014 was an overclaim — corrected here.

---

## ADR-015 — Notes deferred to v1.x

**Date:** 29.04.2026
**Status:** Accepted

**Context.** ADR-007 promoted note-then-consolidate to a first-class library construct. The pattern is real and the efficiency win is real, but it is N=1 — proven only in the PRD generator. Generalizing N=1 patterns into a library tends to bake in incidental decisions of the original use case (default consolidation prompt, `_notes/{turn-uuid}.md` path layout, atomic-note-per-turn assumption).

**Decision.** Cut the `notes` package from v1. v1 ships waves W0–W7 (no W8). Notes lands as additive in v1.x once aikido has 2+ production validations that justify a generalized shape. The pattern is documented as a recipe in `docs/PATTERNS.md` so callers can hand-roll it on top of `tools.Registry` + `vfs.Storage` + `llm.Client`.

**Consequences.** W8 deleted from `v1/PLAN.md`. `notes` package, `examples/notes-consolidate/`, default consolidation prompt all deferred. `docs/PATTERNS.md` gains a "Note-then-consolidate" recipe section. Supersedes ADR-007 for v1.

---

## ADR-016 — Provider abstraction validated by paper-mapping pass before W1 freeze

**Date:** 29.04.2026
**Status:** Accepted; gate closed 30.04.2026 — paper-mapping pass produced concrete v1 surface changes, all folded into `v1/API.md`.

**Context.** ADR-002 ships only OpenRouter in v1. The `llm.Client` surface is locked at v1.0 per the versioning policy. The locked surface was originally designed against exactly one provider (OpenAI-compatible-shaped). Three fields predicted to fail when v2's direct Anthropic lands: `Message.CacheHint bool` (too coarse for per-message breakpoints with TTL), `Request.ThinkingEffort string` (loses Anthropic's explicit `thinking: { budget_tokens: N }` variant), and the absence of an `EventThinking` variant (silently discards reasoning tokens — lossy for observability).

**Decision.** Produce `docs/v1/planning/ANTHROPIC-MAPPING.md` before W1 begins. Walk every field of `llm.Request`, `llm.Message`, `llm.Event`, `llm.Usage` through the direct-Anthropic mapping. Land all surface changes derived from the walk in one composite pre-W1 doc PR.

**Resolution (30.04.2026).** Five changes folded into `v1/API.md`:

1. `Message.CacheHint bool` → `Message.Cache *CacheBreakpoint` with a typed-string `CacheTTL` enum (`CacheTTL5Min`, `CacheTTL1Hour`). `nil` = no breakpoint; empty TTL on a non-nil breakpoint defaults to 5m.
2. `Request.ThinkingEffort string` → `Request.Thinking *ThinkingConfig` with unexported fields and constructor union `ThinkingByEffort(ThinkingEffort) / ThinkingByBudget(int)`. Effort values are typed (`ThinkingEffortLow/Medium/High`). Provider precedence is encoded in the constructor choice, not at the call site.
3. `EventThinking` added to `EventKind` so providers that surface reasoning fragments (Anthropic) emit them in-band.
4. `EventKind` (in both `llm` and `agent`) switched from `iota` to typed string. Order-independent additions; clean log serialization.
5. `Request.Temperature float32` → `Request.Temperature *float32` with `llm.Float32` helper. Eliminates the zero-value foot-gun for deterministic-output callers.

**Consequences.** ADR-016's resolution checklist is closed. The composite pre-W1 doc PR also raised `MaxTokens` default to 16384 (Grill #12a — independent of the Anthropic mapping but bundled for one frozen surface) and added `RunTimeout` + `LLMCallTimeout` (Grill #3 — split of the old single `TurnTimeout`). Supplements ADR-002.

---

## ADR-017 — `Storage` reduced to four methods; Snapshot/Restore/HashState removed

**Date:** 29.04.2026
**Status:** Accepted

**Context.** Tracing call sites in the locked design: `Restore` and `HashState` are never called by aikido — they are caller-facing utilities the locked surface forced every backend to implement. `Snapshot` is called by the agent loop after a successful turn but the returned `SnapshotID` is never used by aikido (no public API surfaced it). Forcing backend authors to implement three methods aikido itself does not consume is a tax with no return.

**Decision.** The `vfs.Storage` interface is reduced to four methods:

```go
type Storage interface {
    ListFiles(ctx context.Context) ([]FileMeta, error)
    ReadFile(ctx context.Context, path string) ([]byte, FileMeta, error)
    WriteFile(ctx context.Context, path string, content []byte, contentType string) error
    DeleteFile(ctx context.Context, path string) error
}
```

`Snapshot`, `Restore`, `HashState` are dropped from the library. The `SnapshotID` type is removed. Callers wanting audit-trail or undo-this-turn semantics build them on top: wrap their `Storage` in a snapshot-recording adapter, or expose a tool that records snapshots, or rely on their DB's own tx history.

**Consequences.** Storage backend authors implement a 4-method contract. The `vfs/conformance.go` suite shrinks. `vfs/memory` simplifies. Combined with ADR-019 (no `Tx` in v1), the entire `vfs.Storage` family becomes minimal: base `Storage` (four methods), capability `ScopedStorage` (one method), capability `Searchable` (two methods).

---

## ADR-018 — One canonical schema style: explicit only

**Date:** 29.04.2026
**Status:** Accepted

**Context.** ADR-005 shipped both explicit helpers and reflective `SchemaFromType[T]()`. Two ways to do the same thing in the same v1 surface costs more than it gives: a third-party dependency (`invopop/jsonschema`), a doc burden ("which style?" in every example), reflective edge cases (unions, pointer-as-optional, enum tags) leaking back as aikido issues, and inconsistent registries when users mix the two styles. The reflective wrapper is essentially a one-line convenience over invopop, not a feature.

**Decision.** v1 ships one tool-schema style: explicit helpers (`tools.Object`, `tools.String`, `tools.Integer`, `tools.Number`, `tools.Boolean`, `tools.Enum`, `tools.Array`). The reflective `tools.SchemaFromType[T] json.RawMessage` is removed. Users who prefer struct-driven schemas call `invopop/jsonschema` directly. The pattern is documented as a recipe in `tools/doc.go`.

**Consequences.** `tools/schema_reflect.go` removed from W4 deliverables. `invopop/jsonschema` removed from v1 dependencies. W4 unit test for `SchemaFromType[FooArgs]` removed. INDEX.md Open Questions 7 (version pin) and 8 (panic-on-unsupported-types) become moot. Reconsider in v1.x if there is concrete demand and a clearly-better shape than the invopop one-liner.

---

## ADR-019 — `Tx` dropped from v1; agent loop has no transactional wrap

**Date:** 29.04.2026
**Status:** Accepted

**Context.** ADR-017 already removed `Snapshot` from the library. With snapshots gone, the only remaining job for the per-turn `Tx` wrap was rollback-on-tool-error — a semantic that discards successful writes from the same turn, forcing the model to redo work, while the model's natural self-correction loop would simply retry the failed call. Without rollback, model and storage state stay in sync; with rollback, they diverge until the model regenerates the lost writes. Each individual `WriteFile` / `DeleteFile` is already atomic at the backend level.

**Decision.** Remove the `vfs.TxStorage` capability and the `vfs.Tx` interface from v1. The agent loop never calls `BeginTx`. Use cases that genuinely need atomic multi-write semantics ship the multi-write as a single tool (`write_files` taking `[]File`), not a hidden tx wrap around the whole turn. Backends that want internal write-batching for efficiency do it without library coordination.

**Consequences.** Agent loop pseudocode in `ARCHITECTURE.md` simplifies — no `BeginTx` / `Commit` / `Rollback` / `anyErr` branches. W5 conformance sub-tests for tx behavior removed. W6 test scenarios for "TxStorage detection" removed. `vfs/memory` no longer implements `TxStorage`. `TxStorage` is a candidate for v1.x re-introduction if a second use case proves the abstraction is real. INDEX.md Open Question 16 (`BeginTx` mid-turn error) becomes moot.

---

## ADR-020 — Search via `Searchable` capability; semantic search and summary search deferred

**Date:** 29.04.2026
**Status:** Accepted

**Context.** A built-in `search_files` tool implemented as "ListFiles → ReadFile → grep" is correct only against the bundled memory backend; against any production backend (Postgres, S3, anything network-bound) it round-trips every file per search and is unusable on real data. Shipping it as a uniformly-available built-in advertises a feature that breaks under real load. Backend-shaped search needs a backend-shaped contract.

**Decision.** v1 ships a built-in tool named `search` (renamed from `search_files`) backed by a new optional capability:

```go
type Searchable interface {
    Search(ctx context.Context, query string) (paths []string, err error)
    SearchSyntax() string  // human-readable syntax docs, used in tool description
}
```

Input is a single `query string`; the backend interprets it (substring, glob, regex, full-text — whatever it supports). Output is a list of file paths. The tool description embeds `Searchable.SearchSyntax()` so the model knows what it can send. `agent.RegisterVFSTools` registers `search` only when the supplied storage satisfies `Searchable`.

Two related features deferred: `semantic_search` (RAG/vector retrieval, v1.x or v2) and document-summary search (returns summary text, not full content; v1.x once the summary shape is designed).

**Consequences.** Memory backend implements `Searchable` as case-insensitive substring (with optional glob filter). Production backends implement it natively. Built-in tool list becomes 5 tools: `read_file`, `write_file`, `list_files`, `delete_file`, `search`. INDEX.md regex-vs-substring open question is moot.

---

## ADR-021 — `tools.Env` slimmed to dynamic-only fields

**Date:** 29.04.2026
**Status:** Accepted

**Context.** The locked `tools.Env` carried `Storage`, `Project`, `TurnID`, `Logger`, and `Now`. ADR-012 removed `Project`. ADR-013 made `Storage` registration-time-bound (closure capture in scoped tools), so the agent has no canonical storage to inject — different tools register with different storage handles. `Logger` and `Now` are convenience fields most tools ignore.

**Decision.** Slim `tools.Env` to fields that genuinely change per dispatch:

```go
type Env struct {
    SessionID string
    TurnID    uuid.UUID
}
```

Tools that need a logger, a clock, or a storage handle capture them at registration via closure.

**Consequences.** VFS tool implementations register with their (scope-bound) `Storage` in closure rather than reading from `env`. Identical clarity at the call site, smaller library surface. W4 / W6 / W7 implementations adjust.

---

## ADR-022 — Public/internal split: `retry` internal, `sseparse` un-extracted, `llmtest` public

**Date:** 29.04.2026
**Status:** Accepted

**Context.** v1 should commit to public API surface only for packages with actual external consumers. The locked design had `retry` public (for "users implementing custom providers" — none exist in v1), `internal/sseparse/` extracted (for "future providers" — v2's direct Anthropic uses a different SSE format entirely), and `internal/testutil.StubClient` hidden (despite being directly useful to every aikido consumer testing their own integrations).

**Decision.** Three calls:

1. `retry/` moves to `internal/retry/`. Used only by `llm/openrouter` in v1 (and future direct providers in v2). v2 may promote back to public if external provider authors materialize.
2. `internal/sseparse/` is not extracted. The SSE line parser stays inline in `llm/openrouter/`. Extract when a second provider validates the abstraction, not before.
3. `StubClient` and its scriptable script types are promoted from `internal/testutil/` to a public package — `llm/llmtest/` (or similar; naming TBD). Same shape as Go stdlib `httptest`. Library users testing their own integrations get a stub `llm.Client` out of the box.

**Consequences.** `retry` removed from v1 public godoc. `internal/sseparse/` package never created — W3 deliverable list shrinks. New public `llm/llmtest/` package. Reduces the v1 stable-API commitment surface to APIs with actual users.

---

## ADR-023 — `History` errors are terminal for the current `Run`

**Date:** 30.04.2026
**Status:** Accepted

**Context.** ADR-014 made `History` a pluggable interface returning `error` from `Read` and `Append`. Neither the ADR nor `ARCHITECTURE.md` specified what the agent does with those errors. The hot path has multiple Append points (one per turn after Grill #8 / the ADR-014 amendment). Drift between the in-memory `msgs` slice the LLM sees and the durable `History` log is an invisible failure mode in development (memory backend never errors) and a haunting one in production (transient DB blip → silently drifted transcript → next turn's model sees holes and behaves erratically).

**Decision.** Any `History.Read` or `History.Append` failure inside a `Run` is **terminal**: the agent emits `EventError` (with the wrapped History error) followed by `EventEnd(EndReasonError)` and the channel closes. The agent does **not** retry History calls — retry budget belongs to the `History` impl. On `EndReasonError` the durable transcript is **not** updated for this turn (the variadic flush is the last step of a successful turn; if it fails, History stays in its pre-turn state).

This applies symmetrically to `Locker` errors: `Lock` failures (including acquire-timeout) emit `EventError + EventEnd(EndReasonError)`. No separate `EndReasonLockTimeout` — the existing `EndReasonError` carries the wrapped error.

**Rationale.** Lenient ("log and continue") creates a correctness bug class invisible in tests and impossible to reproduce in prod. Consistent with the no-transactional-rollback stance from ADR-019: each storage / History op is atomic at the backend level; if it fails, the agent fails. Flaky backends are the backend's responsibility to make resilient (retry-with-backoff in the Postgres adapter), not the agent's.

**Consequences.** `ARCHITECTURE.md` pseudocode wraps every `History.Read`, `History.Append`, and `Locker.Lock` call in a strict-error branch. W6 tests include "stub History returns error on Nth Append → assert `EventError` + `EventEnd(EndReasonError)` + channel closes." Documentation in `agent.History` godoc names the contract: errors are terminal; retry inside the impl if you need it.

---

## ADR-024 — `Locker` is a pluggable interface in v1; in-memory implementation ships now, Redis ships in v1.1

**Date:** 30.04.2026
**Status:** Accepted; supersedes the v3 deferral in ADR-009 / ADR-012.

**Context.** ADR-012 moved concurrency control from a per-project mutex to a per-Session struct mutex. The premise was "v1 single-instance, struct mutex is fine; v3 swaps in a Redis-backed `agent.Locker`." On review, the per-Session struct mutex only works when callers cache one `*Session` instance per logical session ID across requests. That invariant was undocumented. Naive callers — webapps that build a fresh `*Session` per HTTP request — get two distinct mutexes for the same session ID and silently lose concurrency control. The bug is invisible in dev (one Session per test) and lethal in prod (split-brained transcripts under concurrent same-session traffic).

**Decision.** `agent.Locker` is a pluggable interface in **v1**, not v3:

```go
type Locker interface {
    Lock(ctx context.Context, sessionID string) (unlock func(), err error)
}
```

`Locker` is **required** in `SessionOptions` (no nil-fallback, no package-level default — symmetric with every other v1 plug-in). v1 ships `agent.NewLocalLocker()` returning an in-process `*LocalLocker` (per-id `sync.Mutex` map, with a `Forget(id)` method for long-running services). It is production-grade for single-replica deployments, not just tests. `agent.NewLocalSession` auto-supplies it.

For multi-replica deployments, the implementing application provides the backend by implementing `agent.Locker` — typically Redis-backed, but anything with mutual-exclusion-by-key works (etcd, Postgres advisory locks, Consul, etc.). v1.1 ships `agent/locker/redis` with an abstract `Client` interface (two methods: `SetNX` + `Eval`) so aikido does not import any specific Redis client library; callers wire their existing `go-redis` / `redigo` / `rueidis` client through a small adapter.

**Lock scope is the entire `Run`.** Acquired before `History.Read` at turn-start, released after `EventEnd`. Anything narrower allows two concurrent `Run`s on the same ID to interleave their History reads/writes between turns and split-brain the conversation.

**On error.** Any `Lock` failure (including acquire-timeout) is terminal for the current `Run`: `EventError + EventEnd(EndReasonError)`. See ADR-023.

**Consequences.** Per-Session struct mutex removed from the `Session` struct (no `mu sync.Mutex` field). Pseudocode in `ARCHITECTURE.md` wraps `runLoop` in `unlock, _ := s.opts.Locker.Lock(ctx, s.id); defer unlock()`. Webapp shape — fresh `*Session` per request, shared `Locker` from app startup — works correctly for free. v3's "Locker arrives later" promise is honored two milestones early. `vfs/postgres`-style "no production backends bundled" posture (ADR-008, ADR-014) deliberately broken for `agent/locker/redis` in v1.1, because the Redis lock algorithm (token-matched Lua-script unlock, refresh-on-tick) is a correctness primitive aikido owns better than each caller does. Supersedes ADR-009 and the multi-replica deferral in ADR-012.

---

## ADR-025 — v1 ships no model catalog; tokens and cost tracking are caller-side

**Date:** 30.04.2026
**Status:** Accepted

**Context.** The original `llm/catalog.go` (W1 deliverable) shipped a `Model` struct + `Catalog()` + `FindModel()` with a hardcoded seed list of model IDs, family classifications, capability booleans (`SupportsTools`, `SupportsCaching`), and pricing in USD per million tokens. Tracing call sites across the post-grilling design: nothing in the agent loop, the OpenRouter client, or the tools package consumes the catalog. It is purely caller-facing. Hardcoded model lists rot fast (model lineups change every few weeks; pricing changes more often). The shape is OpenAI-pricing-shaped and cannot represent Anthropic's per-cache-tier rates without a v2 breaking change. Self-hosted, fine-tuned, and routing-variant models cannot appear in a static seed at all.

**Decision.** v1 ships **no** model catalog. `llm.Catalog`, `llm.FindModel`, `llm.Model`, `llm.ErrUnknownModel`, the `llm/catalog.go` deliverable, and the seed-pricing table are all dropped. Callers using `Model` strings as literals from provider docs already had what they needed; the catalog wrapper added rot risk without capability.

The dot-to-hyphen `normalizeModelID` helper was kept in v0.1/v0.2 but **removed in v0.2.1** — see ADR-026 below.

**Token consumption + cost tracking is a tracked future requirement** owned by v1.x or v2. The natural home is per-provider: each direct provider package ships its internal pricing table for cost computation. v1's single provider is OpenRouter, which returns `usage.cost` natively in every response — `llm.Usage.CostUSD` carries it through. Production callers wanting cost dashboards in v1 read it directly from `Usage`. v2's `llm/anthropic` and `llm/openai` packages will compute cost per-call from per-provider catalogs.

**Consequences.** `W1` deliverable list shrinks (no `llm/catalog.go`, no seed table, no `FindModel` helpers). `v1/API.md`'s `llm` package section drops the catalog block. INDEX.md Q23 (catalog seed pricing drift) becomes moot. PATTERNS.md gains a "bring your own catalog" recipe section if/when callers demonstrate they need one.

---

## ADR-026 — Drop blanket dot→hyphen model-ID normalization

**Date:** 07.05.2026
**Status:** Accepted (v0.2.1)

**Context.** v0.1 / v0.2 shipped `llm/openrouter/modelid.go` with `normalizeModelID(id string) string` that did `strings.ReplaceAll(id, ".", "-")` on every outbound request. The intent (per ADR-025 and the matching pattern note) was to absorb a perceived OpenRouter quirk where catalog model IDs were hyphenated even when human/marketing names used dots (e.g. `claude-sonnet-4.6` → `claude-sonnet-4-6`).

In practice OpenRouter's catalog is **not** uniformly hyphenated. Newer image-capable models like `google/gemini-2.5-flash-image-preview` are rejected with `400 "is not a valid model ID"` when sent in the dashed form `google/gemini-2-5-flash-image-preview`. Older models with `2.0` accepted both forms, so the gotcha looked stable until 2.5+ landed. The blanket conversion silently broke any caller that picked a newer model.

**Decision.** Drop `normalizeModelID` entirely (v0.2.1). The OpenRouter client now sends `req.Model` verbatim. `llm/openrouter/modelid.go` and its test are deleted. The PATTERNS.md entry is removed. Callers pass the canonical OpenRouter model ID — exactly the string from OpenRouter's catalog page — and we don't second-guess it.

**Consequences.** Callers that relied on the dot form being silently converted will get a 400 from OpenRouter on first request — clear, immediate, fixable in the caller. The downstream hub project (asolabs/hub) had to update its `AISettings.PostImage.Model` default to the catalog-canonical `google/gemini-2.5-flash-image-preview`. No other callers exist; this is a non-breaking-in-practice change for the only known consumer. Future provider quirks should be solved per-model in a documented helper rather than a blanket transformation.

---

## ADR-027 — Promote retry to a public package + add llm.CollectWithRetry

**Date:** 07.05.2026
**Status:** Accepted (v0.2.2)

**Context.** ADR-022 made `retry` deliberately internal — the retry surface was an implementation detail of `llm/openrouter` for stream-start failures. Mid-stream failures (e.g. SSE RST during image generation against preview models) are not retried at the library level: once `Stream` returns a channel and SSE bytes start flowing, a transport drop surfaces as `EventError{Err: ErrServerError}` and the channel closes. Callers that want to recover have to call `Stream` (or `Collect`) again themselves — but they had no shared retry policy to lean on, since `retry` was internal.

Real-world traces from `asolabs/hub` show ~20% per-attempt failure rate on `google/gemini-3.1-flash-image-preview` due to upstream Google flake. End-user-facing image generation requires a retry policy.

**Decision.** Promote `internal/retry` → `retry`. The package surface (`Policy`, `Do`, `RetryAfterError`, `DefaultPolicy`) is unchanged. Add `llm.CollectWithRetry(ctx, client, req, retry.Policy)` and `llm.IsTransientServerError(err)` to give callers a one-line retry wrapper around `llm.Collect`. Add `llm.DefaultStreamingRetryPolicy()` returning a 5-attempt 2s-base 30s-cap policy tuned for image-gen preview models. The OpenRouter client's existing internal use of `retry.Do` continues unchanged — same package, just at the public path.

**Consequences.** Callers wrapping streaming operations get the same retry primitives provider clients already use. The retry surface is now part of the v1 API and follows the v1 stability promise (additive changes only until v2). Cost note: failures still bill for partial tokens emitted before the abort; tune `MaxAttempts` with that in mind. Per-model retry policies (e.g., disable retry for non-preview models that are reliable) remain a caller-side concern.
