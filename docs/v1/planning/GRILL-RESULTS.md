# Grilling Results

Decisions captured from a stress-test pass over the locked v1 design. Each entry summarizes one resolved question, the reasoning, and what concretely changes in the codebase or the locked design surface.

Open question still in flight at the bottom.

---

## D1 — `Session` is the unit of multi-turn interaction; `Scope` is an opaque optional namespace; `projectID` is removed everywhere

**Decision.** The library exposes a `Session` type as the unit of multi-turn interaction. A session bundles: a session ID (opaque string), a model + system prompt, a tool registry, and a `History` plug-in. Multiple `Run` calls on the same session share its history — this is the OpenAI-Assistants/Thread model, not a single API call.

A separate concept, `Scope` (also an opaque caller-defined string), exists *only* for tools that need a tenant or namespace boundary (VFS being the canonical example). Scope is orthogonal to session: a session may have no scope (chatbot over a shared knowledge base), one scope (per-business workspace in ASO Nexus Hub), or scope-per-tool (different scoped tools in the same session). Library never interprets it.

The `projectID uuid.UUID` parameter goes away from every public surface. It was doing four jobs at once (file-scope key, lock key, snapshot key, notes key) and had no single coherent meaning. Each job either moves to its rightful owner (Session for locking, Storage for file scoping, Tx for snapshots) or disappears.

**Rationale.** "Project" was leaking from `asolabs/hub`'s domain model. In ASO Nexus Hub, scope translates to a business UID; in a PRD generator, scope translates to a PRD session ID; in a public chatbot, there is no scope at all. A neutral string is more omnipotent than a typed `uuid.UUID` — callers serialize their own UUIDs to string before handoff if they want them.

**API impact.** Materially different from the locked surface in `v1/API.md`:

```go
// New top-level shape (sketch — not final)
session, _ := agent.NewSession(&agent.SessionOptions{
    ID:           "session-789",     // opaque; history keyed on this
    Client:       client,
    Tools:        registry,           // scope-bound where needed (D2)
    History:      myHistory,          // pluggable interface (D3)
    Model:        "anthropic/claude-sonnet-4-6",
    SystemPrompt: "...",
})

events, _ := session.Run(ctx, "user message")
events, _ := session.Run(ctx, "follow-up")  // history persists
```

Concrete deletions/renames from the locked surface:

- `agent.Agent`, `agent.NewAgent`, `agent.Run(ctx, projectID, userText)`, `agent.RunWithMessages` — gone. Replaced by `agent.Session`, `agent.NewSession`, `Session.Run`.
- `vfs.Storage.CreateProject`, `vfs.Storage.ProjectExists` — gone. Storage doesn't create scopes; the user's backend handles scope lifecycle externally.
- All `projectID uuid.UUID` parameters on `Storage` methods, `Snapshot`, `Restore`, `HashState` — gone.
- `tools.Env.Project uuid.UUID` — gone. Replaced by whatever the scoped tool needs at registration time (closure capture; see D2).
- Per-project mutex in `agent/safety.go` — gone. Concurrency control moves onto the `Session` struct: two `session.Run` calls on the same session serialize; different sessions run in parallel.

**Supersedes.** ADR-009 (per-project mutex) — concurrency is now per-session, not per-project. ADR-011 (stateless agent, caller owns history) — reversed; agent is stateful via the `History` interface (D3).

**Status.** Resolved.

---

## D2 — Storage scoping is a capability, not a parameter

**Decision.** The `vfs.Storage` interface is scope-agnostic. Every method operates on a storage that has already been bound to whatever scope it needs (or none). Backends that multiplex tenants implement an additional capability interface, `vfs.ScopedStorage`, with a single method `Scope(scope string) Storage` that returns a scope-bound view of the backend.

Callers wire scope at registration time:

```go
// Chatbot with shared knowledge base — unscoped backend
storage := mycorp.NewWebsiteKB()  // implements Storage directly
agent.RegisterVFSTools(registry, &agent.VFSToolOptions{Storage: storage})

// Multi-tenant hub — scoped backend, caller picks the scope
backend := hub.NewVFS()                       // implements ScopedStorage
storage := backend.Scope("business-42")       // returns Storage
agent.RegisterVFSTools(registry, &agent.VFSToolOptions{Storage: storage})
```

The library never sees a scope parameter on any storage method. VFS tool handlers always call `env.Storage.WriteFile(ctx, path, content, ...)` — uniform regardless of whether the underlying backend is scoped.

**Rationale.** A chatbot reading the company's mission docs has no tenant boundary; forcing it to invent a fake "global" scope to satisfy a `scope string` parameter on every method would be ergonomic theatre. A multi-tenant hub has a real boundary and needs it enforced. Two different shapes; both must be first-class. The capability pattern (already in use for `TxStorage`, per ADR-004) is the right idiom: small base contract, optional extensions detected by interface assertion or invoked at construction time.

**API impact.** The `vfs.Storage` interface in `v1/API.md` is materially different. Sketch:

```go
type Storage interface {
    ListFiles(ctx context.Context) ([]FileMeta, error)
    ReadFile(ctx context.Context, path string) ([]byte, FileMeta, error)
    WriteFile(ctx context.Context, path string, content []byte, contentType string) error
    DeleteFile(ctx context.Context, path string) error
    Snapshot(ctx context.Context, turnID uuid.UUID, paths []string) (SnapshotID, error)
    Restore(ctx context.Context, snapshotID SnapshotID) error
    HashState(ctx context.Context) (string, error)
}

type ScopedStorage interface {
    Scope(scope string) Storage
}

type TxStorage interface {
    Storage
    BeginTx(ctx context.Context) (Tx, error)
}

type Tx interface {
    Storage
    Commit() error
    Rollback() error
}
```

`Scope(scope string)` is documented as expected to be cheap — a thin wrapper / view, not a connection or setup call. Same convention as `db.WithContext(...)` in ORM-land.

**Supersedes.** Eight method signatures in `v1/API.md` § Package `vfs`. ADR-008 (v1 ships memory VFS only) is unaffected — `vfs/memory` simply implements the new interface.

**Status.** Resolved.

---

## D3 — `History` is a pluggable interface; v1 bundles a memory backend; conformance suite optional but recommended

**Decision.** Conversation history is a first-class plug-in interface, parallel to `vfs.Storage`. Library calls it; callers implement it. v1 ships a bundled memory backend.

```go
type History interface {
    Append(ctx context.Context, sessionID string, msg llm.Message) error
    Read(ctx context.Context, sessionID string) ([]llm.Message, error)
}
```

`sessionID` is opaque to aikido; the implementation uses it as a key into whatever store the caller has (`messages` table in Postgres, file on disk, `map[string][]Message` in memory). The library never inspects it.

The agent's `Session.Run(ctx, userText)` reads history at turn start, calls the LLM with it plus the new user message, then appends the assistant message (and any tool calls / tool results) at turn end. Across multiple `Run` calls on the same session, history accumulates.

`agent.RunWithMessages` (lower-level history-passing variant from the original API.md) is kept as an escape hatch for callers who want full control over what history the LLM sees on a given turn — e.g., for trimming, branched conversations, agent-as-subroutine.

**Rationale.** The original ADR-011 ("stateless agent, caller owns history") was eating exactly the boilerplate aikido is meant to remove. Every realistic caller would write the same `messages` table adapter, the same turn-ID issuance, the same pruning logic. By making history a pluggable interface — same idiom as `vfs.Storage` — callers swap an in-memory store for their production backend with one type, and aikido gets the boilerplate-removal benefit it claimed in the README.

**API impact.** New `agent.History` interface (or `history` sub-package — naming TBD). New `vfs/memory`-style bundled backend. New conformance test suite (recommended; same shape as `vfs/conformance.go`). `agent.RunWithMessages` retained.

**Supersedes.** ADR-011 (stateless agent turns). The justification in ADR-011 — "the `vfs.Storage` interface stays clean: it deals with files only" — is a non-reason; history doesn't have to live inside `vfs.Storage`, it lives in its own interface.

**Status.** Resolved.

---

## D4 — Notes package deferred to v1.x

**Decision.** The `notes` package (W8 in the v1 plan) is cut from v1. v1 ships W0–W7. Notes lands as additive in v1.x, after at least one production validation outside the PRD generator.

The note-then-consolidate pattern is documented as a recipe in `PATTERNS.md` so callers can hand-roll it on top of the v1 primitives (`tools.Registry`, `vfs.Storage`, `llm.Client`) until aikido has 2+ use cases that justify generalizing.

**Rationale.** The pattern is real and the efficiency win is real (writing+rewriting the full document every turn is expensive in tokens and latency, slow in wall-clock time, and lossy for accuracy). But it is N=1 — proven only in the PRD generator. Generalizing N=1 patterns into a library tends to bake in incidental decisions of the original use case (default consolidation prompt, `_notes/{turn-uuid}.md` path layout, atomic-note-per-turn assumption). Better to wait for a second user, see what shape actually generalizes, and ship it as v1.x.

The library's core value prop survives without notes: provider-portable agent loop over pluggable storage with explicit tool dispatch. Notes is a delight-add, not load-bearing.

**API impact.** W8 deleted from `v1/PLAN.md`. `notes` package not in v1. `examples/notes-consolidate/` deleted from v1. `docs/PATTERNS.md` gains a "Note-then-consolidate" recipe section.

**Supersedes.** Cuts ADR-007 (notes as a first-class library construct) from v1 scope. Re-promotes for v1.x.

**Status.** Resolved.

---

## D5 — Provider abstraction validated by paper-mapping pass before W1 freeze

**Decision.** Before W1 implementation begins, produce `docs/v1/planning/ANTHROPIC-MAPPING.md` — a paper-only mapping doc walking every field of `llm.Request`, `llm.Message`, `llm.Event`, and `llm.Usage` through "what would the direct Anthropic client send/receive here?" No code. Just the mapping table. Then fix the gaps in v1 *before* the W1 surface is locked.

The same exercise extends opportunistically to direct OpenAI for the OpenAI-shaped fields where OpenRouter and direct-OpenAI diverge (e.g., `reasoning_effort`).

**Rationale.** The `llm.Client` surface is locked at v1.0 per the versioning policy in `ROADMAP.md`. The locked surface is currently being designed against exactly one provider (OpenRouter, OpenAI-compatible-shaped). Three fields predicted to hurt v2:

1. `Message.CacheHint bool` — too coarse for Anthropic's per-message breakpoints with TTL options.
2. `Request.ThinkingEffort string` — loses Anthropic's explicit `thinking: { budget_tokens: N }` variant.
3. No `EventThinking` variant — silently discards reasoning tokens; lossy for observability.

Half a day of paper design now is cheaper than retrofitting a locked surface in v1.x.

**API impact.** New artifact: `docs/v1/planning/ANTHROPIC-MAPPING.md`. The actual fix to `Request` / `Message` / `Event` is determined by the mapping pass, not pre-decided. Likely: replace `CacheHint bool` with `Cache *CacheBreakpoint`, add `EventThinking`, and either keep `ThinkingEffort string` with documented per-provider mapping or replace with `*ThinkingConfig`.

**Supersedes.** INDEX.md Open Question 3 (silently discard reasoning tokens) — re-decided by the mapping pass. ADR-002 watch-out about `CacheHint` passthrough — the field shape itself changes.

**Status.** Resolved (action item: produce the mapping doc before W1 starts).

---

## D6 — `Storage` is a 4-method contract; `Snapshot` / `Restore` / `HashState` are dropped

**Decision.** The `vfs.Storage` interface is reduced to four methods:

```go
type Storage interface {
    ListFiles(ctx context.Context) ([]FileMeta, error)
    ReadFile(ctx context.Context, path string) ([]byte, FileMeta, error)
    WriteFile(ctx context.Context, path string, content []byte, contentType string) error
    DeleteFile(ctx context.Context, path string) error
}
```

`Snapshot`, `Restore`, and `HashState` are dropped from the library entirely. The agent's per-turn transactional semantics are preserved through `TxStorage.BeginTx` + `Tx.Commit/Rollback` (capability pattern, unchanged).

**Rationale.** Tracing call sites: `Restore` and `HashState` are never called by aikido at all — they are caller-facing utilities the locked surface forced every backend to implement. `Snapshot` is called by the agent loop after a successful turn, but the returned `SnapshotID` is never used by aikido — the agent records a checkpoint the agent itself does not consume. Forcing backend authors to implement three methods aikido does not use is a tax with no return.

Callers who want audit trails or undo-this-turn semantics build them on top: wrap their `Storage` in a snapshot-recording adapter, or expose a tool that records snapshots, or rely on their DB's own tx history. The library does not pretend to provide a feature it cannot actually expose (no public API surfaced the `SnapshotID` for callers to use anyway).

**API impact.** Three methods removed from `Storage`. `SnapshotID` type removed. Storage backend authors implement a 4-method contract; `TxStorage` and `ScopedStorage` remain optional capabilities. The conformance suite (W5) shrinks by the snapshot/restore/hash sub-tests.

**Supersedes.** `vfs.Storage` shape in `v1/API.md` (further-reduced beyond D2). Implicit cleanup of `vfs/conformance.go` test scenarios in `WAVES-LATE.md`.

**Status.** Resolved.

---

## D7 — One canonical schema style: explicit only

**Decision.** v1 ships exactly one tool-schema style: explicit helpers (`tools.Object`, `tools.String`, `tools.Integer`, `tools.Number`, `tools.Boolean`, `tools.Enum`, `tools.Array`). The reflective `tools.SchemaFromType[T]() json.RawMessage` is removed.

Users who prefer struct-driven schemas call `invopop/jsonschema` directly. The pattern is documented as a recipe in `tools/doc.go`:

```go
import jsonschema "github.com/invopop/jsonschema"
schema, _ := json.Marshal(jsonschema.Reflect(&FooArgs{}))
```

Reconsider in v1.x if there is concrete demand and a clearly-better shape than the invopop one-liner.

**Rationale.** Two ways to do the same thing in the same v1 surface costs more than it gives: a third-party dep on every aikido user (even if Go's per-package tracking limits transitive pull-in, the dep still lives in `go.mod` and accrues maintenance), a doc burden ("which style?" in every example), reflective edge cases (unions, optional fields, enums) leaking back as aikido issues, and inconsistent registries when users mix the two styles. The reflective wrapper adds essentially zero value over calling invopop directly — it is a one-line convenience, not a feature.

The explicit style matches what is already proven in `asolabs/hub` production code. v1 ships what works.

**API impact.** `tools.SchemaFromType[T]()` removed from `v1/API.md`. `tools/schema_reflect.go` removed from W4 deliverables. `invopop/jsonschema` removed from v1 dependencies. W4 unit test for `SchemaFromType[FooArgs]` removed.

**Supersedes.** ADR-005 (JSON Schema for tools: explicit primary, reflection optional) — reflection part is dropped from v1. Open Question 7 (`invopop/jsonschema` version pin) and Open Question 8 (`SchemaFromType[chan int]()` panic-vs-error) are moot.

**Status.** Resolved.

---

## D8 — `Tx` is dropped from v1; agent loop has no transactional wrap

**Decision.** The `vfs.TxStorage` capability and `vfs.Tx` interface are removed from v1. The agent loop never calls `BeginTx`. Each tool call's storage operation is atomic at the backend level (whatever atomicity `WriteFile` / `DeleteFile` provide individually); there is no per-turn transactional wrap and no rollback path.

If a use case genuinely needs atomic-multi-write semantics, the right shape is a single tool that does the multi-write atomically (e.g., a custom `write_files` tool taking `[]File`), not a hidden tx wrap around the whole turn. Backends that want internal write-batching for efficiency do it without library coordination.

`TxStorage` is a candidate to re-add as additive capability in v1.x if a second use case proves the abstraction is real and the right shape.

**Rationale.** D6 already removed `Snapshot` from `Storage`. With snapshots gone, the only remaining job for the per-turn `Tx` wrap was rollback-on-tool-error — and that semantic is questionable: it discards successful writes from the same turn, forcing the model to redo work, while the model's natural self-correction loop would simply retry the failed call. Without rollback, model and storage state stay in sync; with rollback, they diverge until the model regenerates the lost writes.

Removing `Tx` from v1 simplifies the agent loop (no capability assertion, no rollback path), shrinks the Storage interface family (no `Tx`, no `TxStorage`), reduces the W5 conformance suite, and removes test scenarios from W6. v1 surface gets smaller. v1.x can re-add cleanly if needed.

**API impact.** `vfs.TxStorage` interface removed. `vfs.Tx` interface removed. Agent loop pseudocode in `ARCHITECTURE.md` simplifies — no `BeginTx` / `Commit` / `Rollback` / `anyErr` branches. W5 conformance sub-tests for tx-related behavior removed. W6 test scenarios for "TxStorage detection" removed. `vfs/memory` no longer needs to implement `TxStorage` (it was already supposed to, trivially).

**Supersedes.** ADR-004 partially (capability pattern is still the idiom; `TxStorage` specifically is dropped from v1). INDEX.md Open Question 16 (`BeginTx` returns error mid-turn — abort or fall back?) is moot.

**Status.** Resolved.

---

## D9 — Built-in `search` tool backed by a `Searchable` capability; `semantic_search` and summary-search are deferred

**Decision.** v1 ships a built-in tool named `search` (renamed from `search_files`) with this shape:

- **Input:** a single `query string` parameter.
- **Output:** a list of file paths.
- **Query syntax:** opaque to aikido. The backend documents what it supports — substring, glob, regex, or anything else feasible — and the tool description embeds that documentation so the model knows what it can send.

The tool is backed by a new optional `vfs.Searchable` capability:

```go
type Searchable interface {
    Search(ctx context.Context, query string) (paths []string, err error)
    SearchSyntax() string  // human-readable syntax docs, used in tool description
}
```

`agent.RegisterVFSTools` registers `search` only when `opts.Storage` satisfies `vfs.Searchable`. Backends that don't implement search simply don't expose the tool to the model. Memory backend implements `Searchable` as case-insensitive substring (with optional glob filter); production backends implement it natively (Postgres full-text, pg_trgm, ElasticSearch, etc.).

Two related features are deferred:

1. **`semantic_search`** — RAG/vector-search-based retrieval. Ships as a separate built-in tool in v1.x or v2 once the embedding-store surface is designed.
2. **Document summaries in the VFS** — search variant that returns summary text per matched file (not the full content). Two candidate shapes: a parallel `_summaries/{path}` convention in VFS, or an optional `Summary string` field on `FileMeta` plus a `Summarizable` capability. Deferred to v1.x; flag it now so the v1 `Searchable` shape leaves room.

**Rationale.** Search is real but backend-shaped. The naive "ListFiles → ReadFile → grep" implementation is correct only for memory; on any production backend (Ent + Postgres, S3, etc.) it round-trips every file per search and is unusable beyond toy data. Shipping a one-size-fits-all `search_files` would advertise a feature that breaks under any real load.

The capability pattern handles this cleanly: aikido defines the contract, backends decide whether and how to satisfy it. The model sees a uniform `search` tool; the backend decides what query syntax that tool can answer. This matches the existing `ScopedStorage` idiom from D2 and keeps v1 honest about what is uniformly supported vs backend-conditional.

Returning paths only (not snippets/line matches) keeps the result shape simple and lets the model decide what to read next via `read_file`. Snippets are a v1.x consideration if usage data shows the model wastes turns reading files it would have skipped given a snippet.

**API impact.** Built-in tool list becomes 5 tools, all uniformly named: `read_file`, `write_file`, `list_files`, `delete_file`, `search`. New `vfs.Searchable` interface (capability, not required). `RegisterVFSTools` does conditional registration based on capability. `agent/VFSToolOptions.SearchDescription` (or similar) likely *not* needed — `Searchable.SearchSyntax()` provides the description text. W7 spec changes accordingly.

**Open within D9 (flag for later):** confirmation that document-summaries should be designed alongside `Searchable` so the v1 shape doesn't preclude it. If summaries warrant first-class treatment, `Searchable.Search` may want a `(paths []string, summaries map[string]string, err error)` return shape from day one. Punting until summary design lands; v1 ships paths-only.

**Supersedes.** W7 sub-table for `search_files` in `v1/PLAN.md` and `WAVES-LATE.md`. INDEX.md regex-vs-substring open question is moot — backend decides.

**Status.** Resolved (with summary-search shape flagged for v1.x design pass).

---

## D10 — `tools.Env` slimmed to dynamic-only fields

**Decision.** `tools.Env` is reduced to fields that genuinely change per dispatch. Tools that need anything else capture it at registration via closure.

```go
type Env struct {
    SessionID string
    TurnID    uuid.UUID
}
```

Removed: `Storage`, `Logger`, `Now`. Tools that need a logger, a clock, or a storage handle capture them at registration. The library only injects what truly varies per call (session and turn IDs).

**Rationale.** Post-D2, `Storage` could not be canonically injected by the agent anyway — different tools register with different (possibly scope-bound) storage handles. `Logger` and `Now` are convenience fields that bloat every tool's surface even when most tools touch neither. The cleanest contract is: library injects the dynamic stuff, tools bring the rest.

VFS tool implementations go from `env.Storage.WriteFile(...)` to `storage.WriteFile(...)` (closure-captured `storage`). Identical clarity at the call site; one fewer field on the library's surface.

**API impact.** `tools.Env` shrinks to two fields. `RegisterVFSTools` pattern becomes:

```go
func RegisterVFSTools(reg *tools.Registry, opts *VFSToolOptions) error {
    storage := opts.Storage  // captured by closure into each handler below
    
    err := reg.Register(readFileDef, func(ctx context.Context, args json.RawMessage, env tools.Env) (tools.Result, error) {
        // uses storage from closure, env.TurnID for logging
        ...
    })
    // ... etc for write_file, list_files, delete_file, search
}
```

Built-in tool handler implementations in W7 adjust accordingly (storage captured by closure rather than read from `env`).

**Supersedes.** `tools.Env` shape in `v1/API.md`. Affects W4, W6, W7 implementation files.

**Status.** Resolved.

---

## D11 — Public-vs-internal split: `retry` internal, `sseparse` un-extracted, `testutil` public

**Decision.** Three calls:

1. **`retry`** moves to `internal/retry/`. Used by `llm/openrouter` only in v1 (and by future direct providers in v2). No public consumers exist in v1; promoting to public would commit aikido to a stable API for an audience of zero. v2 may promote back to public if external provider authors materialize.

2. **`internal/sseparse/`** is not extracted. The line parser stays inline inside `llm/openrouter/` (or, if it deserves its own file, `llm/openrouter/internal/sseparse/`). v2's direct Anthropic uses a wholly different SSE shape (`content_block_delta`, `message_delta`, `tool_use`) — extracting "for sharing with future providers" is N=1 abstraction without validation.

3. **`testutil.StubClient`** is promoted to a public package: `llm/llmtest/` (or similar — naming TBD). Library users testing their own integrations want a stub `llm.Client`. Same shape as Go stdlib `httptest`. Internal hiding forces consumers to re-implement.

**Rationale.** v1's public surface should commit only to APIs that have actual external consumers. `retry` has none. `sseparse` has zero — provider 2 doesn't even use the same wire format. `StubClient` has many — every aikido consumer testing their own code wants it.

The "extract for future use" pattern is the same N=1 trap addressed in D4 (notes deferral) and D5 (paper-mapping over speculation). Extract when the second use case arrives, not before.

**API impact.** `retry/` package directory moves to `internal/retry/`. Public API surface shrinks (no more `retry.Policy`, `retry.Do`, `retry.DefaultPolicy` in godoc). `internal/sseparse/` package never created — line-parsing code lives inside `llm/openrouter/`. New public package `llm/llmtest/` (or `testutil/`) houses `StubClient` and its scriptable script types. W3 deliverable list shrinks by one package.

**Supersedes.** `retry` package layout in `v1/API.md` § Package `retry`. W3 deliverable line "`internal/sseparse/` — extracted line parser shared with future providers." `internal/testutil` package designation in `ARCHITECTURE.md`.

**Status.** Resolved.

---

## Wrap (Round 1)

The grilling has resolved the load-bearing decisions. What remains is detail: hard-guard defaults (MaxTurns / TurnTimeout / MaxTokens), redundant field naming (`Request.MaxTokens` vs `agent.Options.MaxTokens`), typed-string `EndReason` consts (INDEX.md Q24), `ValidatePath` placement (INDEX.md Q10), examples/quickstart rewrite.

These are configurable tweaks, not architecture. They get decided in the next pass when ADRs are reconciled and `v1/API.md` is rewritten.

The biggest meta-task: the locked planning docs (`v1/PLAN.md`, `v1/API.md`, `WAVES-EARLY.md`, `WAVES-LATE.md`, several ADRs in `DECISIONS.md`) are materially out of date with this file. Before W0 starts, a reconciliation sweep should:

1. Add new ADRs (012+) for each D-decision; mark superseded ADRs (002, 004, 005, 007, 008, 009, 011) accordingly.
2. Rewrite `v1/API.md` against the new surface.
3. Refresh `v1/PLAN.md` waves W4–W7; delete W8.
4. Produce `docs/v1/planning/ANTHROPIC-MAPPING.md` (D5 action item) before W1 implementation starts.

---

# Round 2 — Second-grilling pass (30.04.2026)

A second stress-test pass after Round 1 reconciliation, producing a further 12 grills resolved into 12 decisions (D12–D23). Round 1 already adopted ADRs 012–022 and produced an authoritative API.md / ARCHITECTURE.md / DECISIONS.md / PLAN.md. Round 2 found additional soft spots and locked them.

---

## D12 — `History` interface stays narrow; ADR-014's "remove the boilerplate" framing was an overclaim and gets rewritten honestly

**Decision.** Keep `History` as `Append` + `Read([]llm.Message)`. Do not widen `Read` with hints, and do not add a `Compactor` capability. Pruning policy is genuinely caller-specific (token-budget vs. semantic-importance vs. sliding-window vs. LLM-summarize); the library lacks the information to choose.

**Rationale.** The original ADR-014 claimed History promotion would remove "the same `messages`-table adapter, the same turn-ID issuance, the same pruning logic." On closer read, only turn-ID issuance is genuinely removed; the messages-table adapter is just renamed `History`, and pruning still happens inside each caller's `Read` implementation. The honest wins of `History` as a plug-in are: opaque-sessionID + canonical message-log shape + bundled memory backend + conformance suite. Worthwhile, but not "all the boilerplate."

**API impact.** None — surface stays as locked. ADR-014 paragraph rewritten to name the wins honestly. Future v1.x can add a `Compactor` capability additively if a real caller proves the need.

**Status.** Resolved.

---

## D13 — `History` errors are terminal for the current `Run` (strict policy, ADR-023)

**Decision.** Any `History.Read` or `History.Append` failure inside a `Run` emits `EventError` (wrapping the History error) followed by `EventEnd(EndReasonError)` and the channel closes. The agent does not retry History calls. Symmetric for `Locker.Lock` failures, including acquire-timeout (no separate `EndReasonLockTimeout`).

**Rationale.** Lenient ("log and continue") creates a correctness bug class invisible in dev (memory backend never errors) and impossible to reproduce in prod (transient DB blip → silent transcript drift → next-turn model behavior haunting). Strict makes failure in-band: caller's `EventError` handler trips, durable transcript stays consistent. Consistent with the no-transactional-rollback stance from ADR-019.

**API impact.** ADR-023 added. ARCHITECTURE.md pseudocode wraps every `History.Read`, `History.Append`, and `Locker.Lock` call in a strict-error branch. W5 tests for "stub History errors on Nth Append → assert EventError + EndReasonError + channel closes."

**Status.** Resolved.

---

## D14 — Two timeouts: `RunTimeout` (default 10m) bounds the whole `Run`; `LLMCallTimeout` (default 180s) bounds each LLM call

**Decision.** Replace the single `TurnTimeout: 120s` (which was wrapped around the whole multi-turn loop and named misleadingly) with `RunTimeout` and `LLMCallTimeout`. Both default to non-zero values appropriate to thinking-model agents; both are caller-tunable; `0` means no cap. Both → `EndReasonTimeout`.

**Rationale.** The old `TurnTimeout` bound the whole `Run` — up to 20 LLM calls plus tool dispatches plus History I/O — at 120s. With Sonnet 4.5 thinking calls routinely 60–90s, two iterations exhaust the budget; `MaxTurns: 20` is unreachable in practice. The two guardrails contradicted each other. Splitting names them honestly: per-call protection vs. total wall-clock cap.

**API impact.** `SessionOptions.TurnTimeout` removed; `SessionOptions.RunTimeout` and `SessionOptions.LLMCallTimeout` added. ARCHITECTURE.md pseudocode wraps each LLM `Stream` call in a derived `LLMCallTimeout` ctx; the outer `runLoop` is wrapped in `RunTimeout`. Tool dispatch uses the outer `runCtx` (tools own their own timeouts).

**Status.** Resolved.

---

## D15 — Composite ADR-016 gate-closing PR: typed enums, constructor-union ThinkingConfig, string-typed EventKind, `Temperature *float32`, `MaxTokens 16384`

**Decision.** Land a single pre-W1 doc PR closing the ADR-016 gate. Five surface changes:

1. `Message.CacheHint bool` → `Message.Cache *CacheBreakpoint` with typed-string `CacheTTL` enum.
2. `Request.ThinkingEffort string` → `Request.Thinking *ThinkingConfig` with **constructor union** `ThinkingByEffort(ThinkingEffort)` / `ThinkingByBudget(int)` (unexported fields prevent setting both). `ThinkingEffort` typed.
3. `EventThinking` added to `EventKind`. `EventKind` (in both `llm` and `agent`) switched from `iota` to typed `string` for order-independent additions and clean log serialization.
4. `Request.Temperature float32` → `Request.Temperature *float32` with `llm.Float32` helper.
5. `MaxTokens` default raised from 8192 to 16384.

**Rationale.** Typed enums close foot-guns at compile time. Constructor union encodes provider precedence in caller choice rather than per-field provider rules at the call site. String-typed `EventKind` removes the breaking-change risk of mid-enum additions. `*float32` Temperature eliminates the deterministic-zero foot-gun (`temperature: 0` is THE setting for structured generation, RAG, code completion, golden tests). `MaxTokens: 8192` truncates "summarize / consolidate / write-a-document" outputs silently on modern models.

**API impact.** `v1/API.md` rewritten; `ANTHROPIC-MAPPING.md` resolution checklist closed; ADR-016 marked resolved (30.04.2026).

**Status.** Resolved.

---

## D16 — `Locker` is a pluggable interface in v1; in-memory implementation ships with v1; Redis implementation ships in v1.1 (ADR-024)

**Decision.** `agent.Locker` interface (`Lock(ctx, sessionID) (unlock, err)`) lives in **v1** (not v3 as ADR-009/012 deferred). v1 ships `agent.NewLocalLocker()` returning a `*LocalLocker` with per-id `sync.Mutex` map and a `Forget(id)` method — production-grade for single-replica deployments. `SessionOptions.Locker` is **required**. Lock scope is the **entire `Run`**. v1.1 ships `agent/locker/redis` with an abstract `Client` interface (`SetNX` + `Eval`) so aikido does not import any specific Redis client library; multi-replica callers can implement `agent.Locker` themselves before v1.1 lands.

**Rationale.** ADR-012's "per-Session struct mutex" only worked when callers cached one `*Session` instance per logical session ID across requests. That invariant was undocumented. Naive callers — webapps building a fresh `*Session` per HTTP request — got two distinct mutexes for the same session ID and silently lost concurrency control. The bug never appears in dev and is lethal in prod. Pluggable Locker behind a shared interface fixes the failure mode (the lock string converges on the same `Locker` even when `*Session` doesn't).

The Redis Locker deferral to v1.1 (rather than v1) preserves the ADR-008/014 "memory only in v1, callers BYO production backends" posture for one milestone, while still landing the interface — multi-replica callers can implement the lock backend themselves in the meantime.

**API impact.** `agent.Locker` interface, `agent.NewLocalLocker`, `agent.NewLocalSession` (auto-supplies in-memory `History` + `Locker`) added. `SessionOptions.Locker` required. Per-Session struct mutex removed. Lock-acquire timeout reuses `EndReasonError`. ADR-024 added; supersedes ADR-009 and the multi-replica deferral in ADR-012.

**Status.** Resolved.

---

## D17 — `agent.Drain(events) ([]llm.Message, error)` helper instead of `Event.Messages` field

**Decision.** Add `agent.Drain` that consumes the event channel and returns `[assistant, ...toolMessages]` for the turn. Used by `RunWithMessages` callers to obtain the produced messages without reimplementing event-to-message assembly. Streaming consumers fan out the channel themselves before passing one branch to `Drain`.

**Rationale.** `RunWithMessages` callers (trimming, branched conversations, agent-as-subroutine) need the assembled output to append to their own message store. The library already runs the assembly internally for `Run`'s History.Append; exposing it as a helper avoids forcing every caller to reimplement it (subtly differently, with inconsistent tool-result serialization). Choosing `Drain` over an `Event.Messages` field on `EventEnd` keeps `Event` simpler.

**API impact.** `agent.Drain(events <-chan Event) ([]llm.Message, error)` added. EXAMPLES.md gets a `RunWithMessages + Drain` recipe.

**Status.** Resolved.

---

## D18 — `History.Append` is variadic; flush once at end of turn; ADR-014's "partial-turn crash recovery" rationale dropped

**Decision.** `History.Append(ctx, sessionID, msgs ...llm.Message)`. The agent accumulates the user message, assistant message, and tool-result messages in a local slice and flushes them all in one variadic call after the model emits no more tool calls (or `MaxTurns` exhausts). On `EndReasonError` the flush does **not** happen — durable transcript stays in its pre-turn state.

**Rationale.** Per-step appending was K+2 round trips per turn (1 user + 1 assistant + K tool results). For Postgres-backed History across realistic networks (5-200ms each), this is 10ms-2s of pure DB latency on the agent loop's critical path per turn. The committed reason ("partial-turn crash leaves a recoverable transcript") doesn't survive scrutiny: no `Resume(fromTurnID)` API exists; partial-recorded turns are corrupt for replay (assistant-promised tool calls without their results, or vice versa); the cost is paid every turn forever in exchange for a feature with no consumer. End-of-turn flush gives every backend one round trip per turn instead of K+2; on crash, durable transcript misses the turn entirely (clean: nothing-or-everything).

**API impact.** `History.Append` signature change to variadic. ARCHITECTURE.md pseudocode rewritten. ADR-014 amendment names the new behavior. W5 tests assert exactly one Append call per successful turn, zero on `EndReasonError`.

**Status.** Resolved.

---

## D19 — Drop `llm.Catalog`, `llm.FindModel`, `llm.Model`, `llm.ErrUnknownModel` from v1 (ADR-025)

**Decision.** v1 ships **no** model catalog. `Catalog`, `FindModel`, `Model`, `ErrUnknownModel`, `llm/catalog.go`, and the seed-pricing table are all dropped. The `normalizeModelID` helper stays (used by the OpenRouter client itself, not by any catalog).

**Rationale.** Tracing call sites: nothing in the agent loop, the OpenRouter client, or the tools package consumes the catalog. It is purely caller-facing and rots fast (model lineups change every few weeks; pricing changes more often). The shape is OpenAI-pricing-shaped and cannot represent Anthropic's per-cache-tier rates without v2 breaking changes. Self-hosted, fine-tuned, and routing-variant models cannot appear in a static seed at all. v1's single provider (OpenRouter) returns `usage.cost` natively — `Usage.CostUSD` carries it.

**Tracking note.** Token consumption + cost tracking is a **future requirement**, owned by v1.x or v2. The natural home is per-provider: each direct provider package ships its internal pricing table. v2's `llm/anthropic` and `llm/openai` will compute cost per-call from their own catalogs; this requirement is captured in `ROADMAP.md` and `v2/SCOPE.md`.

**API impact.** ADR-025 added. `v1/API.md` `llm` section drops the catalog block. W1 deliverable list shrinks. INDEX.md Q23 marked moot.

**Status.** Resolved.

---

## D20 — `History` is required in `SessionOptions`; `agent.NewLocalSession` wraps with in-memory defaults for single-replica use

**Decision.** Drop the "nil = ephemeral in-memory" default. `History` is required. `Locker` is also required (per D16). For single-replica callers and tests, ship `agent.NewLocalSession(opts)` — equivalent to `NewSession` but auto-supplies `historymem.NewHistory()` and `agent.NewLocalLocker()` if either is nil in `opts`. `NewSession` itself stays strict.

**Rationale.** Symmetric with every other v1 plug-in (`Client`, `Tools`, `Storage`, `Locker`). Hidden defaults defeat the goal of explicit construction. `NewLocalSession` gives new users a one-line quickstart while keeping `NewSession` honest about what it requires.

**API impact.** `SessionOptions.History` required; nil returns an error from `NewSession`. `agent.NewLocalSession` added. README quickstart and `examples/agent-vfs` use `NewLocalSession`. `examples/multi-session/` (or webapp recipe in EXAMPLES.md) uses `NewSession` with explicit shared `Locker`.

**Status.** Resolved.

---

## D21 — Observability stance for v1 stays lean; no structured slog contract or `OnEvent` hook

**Decision.** `SessionOptions.Logger *slog.Logger` stays as-is. The Session may log internal events at its discretion; **no contract** on what it logs, when, or with what attributes — that is intentionally not specified for v1.0. No `SessionID` field on `agent.Event`. No `OnEvent` callback.

**Rationale.** Locking a structured-event vocabulary now (event names, attribute keys, levels) is premature; the caller list is small, their needs vary. Adding callbacks creates a parallel observability path that overlaps the channel and the logger, making the contract murkier. Both are purely additive in v1.x — defer until a real caller proves the shape.

**API impact.** No surface change beyond keeping `Logger` plumbed. v1.x candidate: structured slog contract + optional `OnEvent` hook.

**Status.** Resolved.

---

## D22 — Triage round: `MaxTokens` default 16384; collapse W2+W3; keep `vfs.ValidatePath` global; defer `RegisterVFSTools` extensibility

**Decisions (bundled):**

- **D22a — `MaxTokens` default raised to 16384.** Folded into the pre-W1 ADR-016 PR (D15). Modern Sonnet 4.5 supports 64K; Opus 4.5 supports 32K. 8192 truncates summarize/consolidate outputs silently. 16384 covers most generation tasks while still bounding runaway output.
- **D22b — Channel buffer cap stays hardcoded at 16.** Not a `SessionOptions.EventBufferSize`. v1 callers do not need it; the constant lives in `agent/run.go`. If a real consumer hits backpressure, the answer is "fan out, not bigger buffers."
- **D22c — Collapse W2 (non-streaming) and W3 (true streaming) into a single new W2.** The W2 fake-streaming intermediate code was throwaway. New W2 ships canned SSE transcripts on day one and validates against the real wire format from the start. Subsequent wave numbers shift down by one.
- **D22d — `vfs.ValidatePath` stays package-level.** Documented in godoc as enforcing aikido-wide structural invariants (no `..`, no `/abs`, no nulls, length cap, no empty). Caller-policy filters (`MaxFileBytes`, `AllowedExtensions`, `HideHiddenPaths`) live on `agent.VFSToolOptions`. The split is intentional and named.
- **D22e — `RegisterVFSTools` shape unchanged for v1.** No individual-tool wrapping / replacement / handles in v1. v1.x candidate: `BuildVFSTools(opts) ([]TaggedHandler, error)` returning per-tool handles, additively.

**API impact.** `MaxTokens` default updated. `v1/PLAN.md` collapses W2+W3. ARCHITECTURE.md adds the ValidatePath split paragraph. No other surface changes.

**Status.** Resolved.

---

## D23 — Documentation reconciliation across the repo

**Decision.** A single doc-only PR brings every file in `docs/` into a consistent, ready-to-implement state for the post-grilling design. Files updated: `API.md`, `ARCHITECTURE.md`, `DECISIONS.md` (3 new ADRs + 2 amendments), `PLAN.md`, `ANTHROPIC-MAPPING.md` (close checklist), `INDEX.md` (resolution status), `README.md`, `EXAMPLES.md`, `ROADMAP.md`, `PATTERNS.md`, `SECURITY.md`, `CONTRIBUTING.md`, `v2/SCOPE.md`. Divergence banners added to `WAVES-EARLY.md`, `WAVES-LATE.md`, `TEST-STRATEGY.md`, `REFERENCE-CODE.md`, `OPENROUTER-DETAILS.md` rather than full surgery — these are 50-100KB each of implementation-detail prose where wholesale rewriting would risk losing useful depth, and the authoritative shapes already live in the top-level docs.

**Rationale.** Round 1 of grilling left planning docs materially out of date; Round 2 piles more changes on top. Without reconciliation, every future contributor (or future self) hits a tangle of contradictions across files. One coherent post-grilling state lets W0 begin without anyone having to re-derive what's authoritative.

**API impact.** None — pure docs.

**Status.** Resolved.

---

## Wrap (Round 2)

Round 2 took the post-Round-1 design and applied second-grilling pressure. The 12 grills produced 12 more locked decisions (D12–D23). The biggest material changes vs. the Round-1 surface:

- `Locker` pulled forward from v3 to v1 (interface + in-memory) / v1.1 (Redis impl).
- Two timeouts replace the single mis-named `TurnTimeout`.
- Variadic `History.Append` flushed once per turn replaces per-step appending.
- Strict error policy on History and Locker (terminal for current `Run`).
- Catalog dropped entirely; cost/token tracking deferred to per-provider catalogs in v2.
- Three typed enums + a constructor union close the ADR-016 gate.
- `Temperature *float32` closes the deterministic-zero foot-gun.
- `agent.Drain` helper for `RunWithMessages` callers.
- `History` and `Locker` both required in `SessionOptions`; `agent.NewLocalSession` wraps with in-memory defaults.

Pre-W0 readiness criteria — all green:

1. ADR-016 gate is closed; pre-W1 doc PR landed (this round, in fact, *is* that PR).
2. ADRs 023, 024, 025 added; ADRs 014 and 016 amended.
3. `v1/API.md` reflects the post-grilling surface.
4. `v1/PLAN.md` waves W0–W6 are authoritative; W2+W3 collapsed.
5. ARCHITECTURE.md pseudocode and guardrails table match.
6. ROADMAP.md, EXAMPLES.md, README.md, SECURITY.md, CONTRIBUTING.md, PATTERNS.md, v2/SCOPE.md aligned.
7. Planning docs (WAVES-EARLY/LATE, TEST-STRATEGY, REFERENCE-CODE, OPENROUTER-DETAILS) carry divergence banners pointing to the authoritative top-level docs.

W0 (skeleton) can start.
