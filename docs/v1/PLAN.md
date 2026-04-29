# v1 Implementation Plan

Wave-based delivery. Each wave is one PR (or one well-scoped slice of one). No wave depends on a future wave's API.

This plan reflects the **post-second-grilling** design (see [`planning/GRILL-RESULTS.md`](planning/GRILL-RESULTS.md) D1–D23 and ADRs 012–025 in [`../DECISIONS.md`](../DECISIONS.md)). v1 is now W0–W6 (W2+W3 collapsed into one wave; W8 deleted; notes deferred to v1.x per ADR-015).

The detailed planning docs under `planning/` reflect the *pre-grilling* surface in places — check the wave summaries below for any divergence; **the wave summaries here, plus `API.md` and `ARCHITECTURE.md`, are authoritative.**

> **Pre-W1 gate (CLOSED 30.04.2026 / ADR-016):** The composite paper-mapping doc PR has folded all surface changes into `API.md`. Five surface changes landed: `Cache *CacheBreakpoint`, `Thinking *ThinkingConfig` (constructor union), `EventThinking`, string-typed `EventKind`, `Temperature *float32` + `llm.Float32` helper. Plus the bundled grilling outcomes: `MaxTokens` default 16384, `RunTimeout` + `LLMCallTimeout` (replacing `TurnTimeout`), `History` required + variadic `Append`, `Locker` interface required, `agent.NewLocalLocker`, `agent.NewLocalSession`, `agent.Drain`, drop `Catalog`. With the gate closed, W1 begins from a single coherent locked surface.

> **Implementation depth:** Per-file code skeletons, named test scenarios, OpenRouter wire-format specifics, reference-code mining, and the risk register live under [`planning/`](planning/INDEX.md). Read this PLAN.md for the wave summary; open the planning docs when implementing a wave. Where the planning docs reflect pre-grilling shapes (per-Session mutex, `agent.NewAgent`, `Tx` capability, `SchemaFromType`, `Catalog`, etc.), the wave summaries here override.

---

## W0 — Skeleton

**Goal.** Empty repo with green CI and the docs already in place.

**Deliverables**

- `go.mod` declaring module `github.com/mxcd/aikido`, Go 1.23.
- `LICENSE` (MIT).
- `README.md` at repo root pointing to `docs/`.
- `justfile` with `check`, `test`, `lint`, `vet`, `build`, `tidy` targets.
- `.github/workflows/ci.yml` running `just check` on push and PR.
- `.gitignore` (Go-standard plus `.env`, `bin/`, `dist/`).
- `.golangci.yml` with the standard linter set.

**Tests.** `just check` passes on an empty module.

**Exit criteria.** CI green on first commit. Repo can be cloned and `just check` runs out-of-the-box.

**Risks.** None. Pure scaffolding.

---

## W1 — `llm` types

**Goal.** All provider-agnostic types defined; no implementation yet. Surface reflects the post-ADR-016 shapes.

**Deliverables**

- `llm/client.go` — `Client` interface, `Request` (with `*ThinkingConfig`, `*float32` Temperature, `*CacheBreakpoint` indirectly via Message), `Event`, `EventKind` (typed `string`).
- `llm/message.go` — `Message`, `Role`, `RoleSystem/User/Assistant/Tool`, `ImagePart`, `ToolCall`, `Usage`, `CacheTTL` enum, `CacheBreakpoint`.
- `llm/thinking.go` — `ThinkingEffort` enum, `ThinkingConfig` (unexported fields), `ThinkingByEffort`, `ThinkingByBudget` constructors.
- `llm/tooldef.go` — `ToolDef`.
- `llm/helpers.go` — `Collect(ctx, c, req) (text, calls, usage, err)`, `Float32(v float32) *float32`.
- `llm/errors.go` — typed error wrappers (`ErrAuth`, `ErrRateLimited`, `ErrServerError`, `ErrInvalidRequest`).
- `llm/doc.go` — package-level godoc.

**Tests**

- `Collect` happy path: feed a stub channel, get text/calls/usage back. `EventThinking` events do **not** appear in the returned text.
- `Collect` propagates `EventError` as the returned error.
- `ThinkingByEffort` / `ThinkingByBudget` round-trip — internal effort/budget readable by provider clients (via unexported accessors in the same package).
- `Float32(0)` returns a non-nil pointer to zero — verifies the deterministic-zero foot-gun is closed.

**Exit criteria.** `go doc github.com/mxcd/aikido/llm` reads as a complete API reference. No imports outside the standard library and `github.com/google/uuid`.

**Risks.** Type bikeshedding. Lock the names from [v1/API.md](API.md); deviations need an ADR update.

> **Note on dropped deliverables.** No `llm/catalog.go`, `llm.Catalog`, `llm.FindModel`, `llm.Model`, `llm.ErrUnknownModel`, no seed catalog (ADR-025).

---

## W2 — `llm/openrouter` (real streaming + tool-call assembly + retry)

**Goal.** Call OpenRouter end-to-end with true SSE streaming, tool-call assembly per the DeepThought pattern, and 429/5xx retry at stream-start only. (Formerly W2 + W3; collapsed into a single wave per Grill #12c — the W2 "single-event consumption" intermediate state was throwaway.)

**Deliverables**

- `llm/openrouter/client.go` — `Options` (with `ProviderOrder []string` per INDEX.md Q1), `NewClient`, internal HTTP client wiring.
- `llm/openrouter/api_types.go` — JSON-tagged unexported request and response types matching OpenAI-compatible chat completions shape.
- `llm/openrouter/modelid.go` — `normalizeModelID` (dots → hyphens) per the DeepThought gotcha.
- `llm/openrouter/sse.go` — SSE parser (line-based, `data: ` prefix, `[DONE]` termination). Inlined; not extracted (ADR-022). `assembleToolCalls(fragments)` buffering by `index`, emitting only at choice-done.
- `llm/openrouter/retry.go` — wrap stream-start in `internal/retry.Do` with default policy.
- `internal/retry/` — internal-only exponential backoff helper (ADR-022).
- `llm/openrouter/Stream` — true SSE streaming; emits `EventTextDelta` per chunk, `EventThinking` when the provider routes through Anthropic with thinking enabled, `EventToolCall` per assembled call, `EventUsage` from final chunk, `EventEnd` last.
- `llm/openrouter/testdata/` — canned transcripts:
  - `simple_text.sse` — plain completion.
  - `single_tool.sse` — one tool call.
  - `multi_tool_interleaved.sse` — two calls fragmented across events.
  - `429_then_success.sse` — retry path.
  - `mid_stream_error.sse` — provider drops connection after some tokens.
  - `thinking.sse` — Anthropic-routed thinking-enabled response with `thinking_delta` events. (NEW per ADR-016 close-out.)
- `examples/chat-oneshot/main.go`.

**Tests**

- Each transcript replays through `httptest.Server`; assert event order, complete tool-call JSON, usage emission before end, `EventThinking` text on the thinking transcript.
- `normalizeModelID`: `"anthropic/claude-sonnet-4.6"` → `"anthropic/claude-sonnet-4-6"`.
- Cancellation: caller cancels `ctx`; producer goroutine exits and channel closes.
- 429 retry: first response is 429 with `retry-after`, second is 200; final result is success.
- Mid-stream error: assert `EventError` followed by `EventEnd` with `EndReason="error"`.
- Real-network smoke (manual, gated on `OPENROUTER_API_KEY` env var): `examples/chat-oneshot` against real OpenRouter prints text and token counts.

**Exit criteria.** All canned transcripts pass. `examples/chat-oneshot` works against real OpenRouter. `multi_tool_interleaved.sse` and `thinking.sse` tests green.

**Risks.** Provider-side SSE format drift (new fields, reordered events). Mitigation: parser is permissive on unknown fields; tests catch breakage early via real smoke tests.

---

## W3 — `tools` registry

**Goal.** Tool registration, dispatch, and explicit schema definition (ADR-018: explicit only; reflective `SchemaFromType` removed from v1).

**Deliverables**

- `tools/registry.go` — `Registry`, `NewRegistry`, `Register`, `Defs`, `Dispatch`, `Has`.
- `tools/handler.go` — `Handler` type, slim `Env` struct (`SessionID string`, `TurnID uuid.UUID` only — ADR-021), `Result` struct.
- `tools/schema.go` — explicit helpers: `Object`, `String`, `Integer`, `Number`, `Boolean`, `Enum`, `Array`.
- `tools/doc.go` — package godoc including the `invopop/jsonschema` recipe for users who want struct-driven schemas.
- `tools/errors.go` — `ErrUnknownTool`, `ErrDuplicateTool`.

**Tests**

- Register two tools, dispatch each, assert handler called with parsed args and the slim `Env`.
- Duplicate registration returns `ErrDuplicateTool`.
- Dispatch unknown tool returns `ErrUnknownTool`.
- `Object(map[string]any{"x": String("desc")}, "x")` produces canonical output.

**Exit criteria.** Sample tool registers and dispatches end-to-end with the slim `Env`.

**Risks.** None. No third-party deps in this wave (`invopop/jsonschema` removed per ADR-018).

---

## W4 — `vfs` interface + memory impl + conformance suite

**Goal.** Minimal four-method `Storage` (ADR-017) plus optional `ScopedStorage` (ADR-013) and `Searchable` (ADR-020) capabilities. Memory backend satisfying `Storage` + `Searchable`. Reusable conformance suite.

**Deliverables**

- `vfs/storage.go` — `Storage` (4 methods: `ListFiles`, `ReadFile`, `WriteFile`, `DeleteFile`), `FileMeta`.
- `vfs/scoped.go` — `ScopedStorage` capability (single method `Scope(scope string) Storage`).
- `vfs/searchable.go` — `Searchable` capability (`Search(ctx, query) ([]string, error)`, `SearchSyntax() string`).
- `vfs/path.go` — `ValidatePath` (structural invariants only; caller-policy filters live on `agent.VFSToolOptions`).
- `vfs/errors.go` — `ErrPathInvalid`, `ErrFileNotFound`, `ErrFileTooLarge`.
- `vfs/conformance.go` — exported `RunConformance(t *testing.T, factory func() Storage)`. Searchable sub-tests are gated on the factory's storage satisfying `vfs.Searchable`.
- `vfs/memory/storage.go` — in-memory `Storage` + `Searchable` impl using `sync.RWMutex`. Search is case-insensitive substring with optional glob filter; `SearchSyntax()` documents it.
- `vfs/memory/storage_test.go` — runs `RunConformance` against `vfs/memory.NewStorage`.

**Tests (in conformance suite)**

- Path validation rejects `..`, `/abs`, empty, null, oversized, trailing slash, double slash.
- Write/read round-trip preserves bytes and content type.
- List returns all files in deterministic order.
- Delete returns `ErrFileNotFound` for missing path.
- Searchable (gated): empty corpus returns no matches; single-substring query returns matching paths; glob filter restricts the corpus; `SearchSyntax()` returns a non-empty string.

**Exit criteria.** Memory impl passes the full conformance suite (including Searchable sub-tests). The suite documents the four-method contract every storage backend must meet.

**Risks.** None significant. Cut from earlier scope: `Snapshot` / `Restore` / `HashState` (ADR-017), `TxStorage` / `Tx` (ADR-019), `SnapshotID` type. Conformance suite is materially smaller than the pre-grilling design.

---

## W5 — `agent` core: `Session`, `History`, `Locker`, streaming loop

**Goal.** Session-based streaming agent loop tying `llm`, `tools`, pluggable `History`, and pluggable `Locker` together. RunTimeout / LLMCallTimeout split. Variadic single-flush `History.Append`. Strict History/Locker error policy.

**Deliverables**

- `agent/session.go` — `SessionOptions` (with `History`, `Locker`, `RunTimeout`, `LLMCallTimeout`, `MaxTokens` 16384 default, `Temperature *float32`), `Session`, `NewSession` (validates required fields).
- `agent/local.go` — `NewLocalSession` convenience constructor that auto-supplies `historymem.NewHistory()` and `agent.NewLocalLocker()` if not provided.
- `agent/run.go` — `Session.Run`, `Session.RunWithMessages`, internal `runLoop` (Locker-wrapped, two-timeout, single end-of-turn variadic flush, strict-error).
- `agent/event.go` — `Event`, `EventKind` (typed `string`), `ToolResult`, `EndReason*` consts (`EndReasonStop`, `EndReasonMaxTurns`, `EndReasonError`, `EndReasonTimeout`, `EndReasonCancelled`).
- `agent/drain.go` — `Drain(events <-chan Event) ([]llm.Message, error)` helper for `RunWithMessages` callers.
- `agent/history.go` — `History` interface (variadic `Append`).
- `agent/history/memory/history.go` — in-memory `History` impl using `sync.RWMutex`.
- `agent/history/conformance.go` — exported `RunConformance(t *testing.T, factory func() History)` parallel to `vfs.RunConformance`.
- `agent/locker.go` — `Locker` interface, `LocalLocker` (per-id `sync.Mutex` map), `NewLocalLocker`, `(*LocalLocker).Forget`.
- `llm/llmtest/stub_client.go` — public `StubClient` (ADR-022 — promoted from internal). `TurnScript` shape, `ErrStubExhausted`.

**Tests**

- Happy path: stub returns text + one tool call + end-on-next-turn. Assert event sequence, exactly one `History.Append` call at end of turn (variadic), final history-store state.
- Tool error: stub returns tool call, registry handler returns error; session emits `EventToolResult{OK: false}` and continues; stub's next turn ends.
- Mid-stream error: stub emits `EventError` mid-stream; session emits `EventError` + `EventEnd(EndReasonError)`; **no `History.Append` for this turn**; channel closes.
- Max-turns: stub never stops calling tools; session emits `EventEnd(EndReasonMaxTurns)` after 20 iterations; History flushed.
- RunTimeout: stub blocks; outer `RunTimeout` ctx deadline trips; session emits `EventEnd(EndReasonTimeout)`.
- LLMCallTimeout: stub blocks one call; per-call ctx trips; session emits `EventError + EventEnd(EndReasonError)` (or `EndReasonTimeout` — choose and document; recommend `EndReasonTimeout` for symmetry).
- Cancellation: caller cancels ctx mid-turn; session emits `EventEnd(EndReasonCancelled)`.
- Strict History error: stub History returns error on `Append`; assert `EventError` + `EventEnd(EndReasonError)` and channel closes (ADR-023).
- Strict Locker error: stub Locker returns error on `Lock`; assert `EventError` + `EventEnd(EndReasonError)` and channel closes.
- Concurrency (Locker): two `Run` calls on **two different `*Session` values with the same ID** sharing one `LocalLocker` serialize; with different IDs run in parallel. Verifies the multi-replica-shape correctness fix.
- History conformance suite passes for the bundled memory backend.
- `Drain` happy path: feed an event channel that emits text + tool calls + tool results + EventEnd; assert returned message slice is `[assistant{text, calls}, tool{result1}, tool{result2}, ...]` in order.
- `NewSession` validation: missing `History`, `Locker`, `Client`, `Model`, or `ID` returns an error.
- `NewLocalSession`: nil `History` and nil `Locker` get auto-supplied; user-provided ones are respected.

**Exit criteria.** `examples/agent-vfs/main.go` runs end-to-end with a real OpenRouter client, a real `LocalLocker`, and an in-memory `History` (via `NewLocalSession`).

**Risks.** Locker mutex semantics under concurrent `Forget` and `Lock` for the same key — write a focused test. New surface area (Locker + History) is small but doubles the W5 plug-in count compared to the pre-grilling shape.

---

## W6 — VFS-aware built-in tools

**Goal.** Drop-in tool set so users can stand up a working knowledge-base agent in one call. Tool handlers capture their `Storage` via closure (ADR-021); `search` is registered conditionally based on capability (ADR-020).

**Deliverables**

- `agent/tools_builtin.go` — handlers for `read_file`, `write_file`, `list_files`, `delete_file`, `search`.
- `agent/tools_register.go` — `RegisterVFSTools(reg *tools.Registry, opts *VFSToolOptions) error`. Registers `read_file`/`write_file`/`list_files`/`delete_file` unconditionally; registers `search` only if `opts.Storage` satisfies `vfs.Searchable`. Each handler captures `opts.Storage` (and `opts.HideHiddenPaths` etc.) via closure.
- `agent/VFSToolOptions` — `Storage vfs.Storage` (required), `HideHiddenPaths bool`, `AllowedExtensions []string`, `MaxFileBytes int64`.

**Tool semantics**

| Tool | Args | Returns |
|------|------|---------|
| `read_file` | `{path: string}` | `{content: string, content_type: string, size: int}` |
| `write_file` | `{path: string, content: string, content_type?: string}` | `{path: string, size: int}` |
| `list_files` | `{}` | `{files: [{path, size, updated_at}, ...]}` |
| `delete_file` | `{path: string}` | `{path: string}` |
| `search` | `{query: string}` | `{paths: [string]}` |

The `search` tool's description embeds `vfs.Searchable.SearchSyntax()` so the model knows what query language the backend accepts.

**Tests**

- Each tool: happy path + invalid path (rejected) + path-not-found (graceful).
- Write/read round-trip via a `Session` (script the stub LLM via `llm/llmtest` to call `write_file` then `read_file`).
- HideHiddenPaths: write `_internal/x.md` and `notes.md`; list returns only `notes.md`.
- AllowedExtensions: write `foo.bin` rejected when extensions list is `[".md"]`.
- MaxFileBytes: write content over the cap rejected with clear error.
- `search` registration: when `opts.Storage` is `vfs.Searchable`, the tool is registered; when not, it is absent from the registry. Verify with `Registry.Has("search")`.
- `search` semantics (memory backend): case-insensitive substring; results returned as `{paths: [...]}`.

**Exit criteria.** `examples/agent-vfs/main.go` runs end-to-end with the real tool set: session creates a file, lists, reads, searches, and reports back to the user.

**Risks.** Backends shipping a confusing `SearchSyntax()` description leak that confusion to the model. Mitigation: document the contract — `SearchSyntax()` should be one short paragraph in plain English with one example query.

---

## After W6 — Tagging v0.1.0

Once W0–W6 are merged and `just check` is green:

1. Bump module path tag in CI to v0.1.0.
2. Manual smoke run of `examples/chat-oneshot` and `examples/agent-vfs` against real OpenRouter.
3. Tag `v0.1.0` and push.
4. Update `README.md` "Status" section to reference the tag.

aikido is now a usable library. v1.1 work begins (`agent/locker/redis`); v2 work begins from a stable v1 surface that callers can pin against.

## Out of scope for v1

Repeated for clarity (see [DECISIONS.md](../DECISIONS.md) for full rationale):

- No CLI binary (ADR-010).
- No image, audio, or queue packages.
- No `notes` package — pattern documented as a recipe in `PATTERNS.md` (ADR-015).
- No `vfs/local`, `vfs/postgres`, or any backend other than `vfs/memory` (ADR-008).
- No `vfs.TxStorage` / `vfs.Tx` — agent loop is not transactional in v1 (ADR-019).
- No `vfs.Storage.Snapshot` / `Restore` / `HashState` — caller-side concerns (ADR-017).
- No `tools.SchemaFromType[T]()` — explicit schema only (ADR-018).
- No `retry` as a public package — moved to `internal/retry/` (ADR-022).
- No `internal/sseparse/` extraction — SSE parser inline in `llm/openrouter/` (ADR-022).
- No `llm.Catalog` / `llm.FindModel` / `llm.Model` — no v1 catalog (ADR-025); per-provider catalogs are v2.
- No `agent/locker/redis` — ships in v1.1 (ADR-024). Multi-replica callers can implement `agent.Locker` themselves before v1.1 lands.
- No structured slog event vocabulary — `Logger` plumbed but contract intentionally lean for v1.0; observability hooks land in v1.x once a real caller proves the need.
- No SSE-over-HTTP wrapper.
- No direct Anthropic / OpenAI providers — joins v2 (ADR-002).
- The `agent.RunWithMessages` escape hatch remains for callers that own message history themselves; default `Session.Run` reads/appends via the `History` interface (ADR-014). `agent.Drain` is the recommended way for `RunWithMessages` callers to obtain the turn's produced messages.
