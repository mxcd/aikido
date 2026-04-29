# v1 Planning — Implementation Playbook

> **Divergence note (30.04.2026).** The five detailed planning docs in this directory (REFERENCE-CODE, OPENROUTER-DETAILS, WAVES-EARLY, WAVES-LATE, TEST-STRATEGY) reflect the *pre-second-grilling* surface in places. They were produced before ADRs 023–025 landed and before the W2+W3 wave collapse. **Authoritative shapes live in `../API.md`, `../../ARCHITECTURE.md`, and `../../DECISIONS.md`; authoritative wave structure lives in `../PLAN.md`.** Where this directory's docs disagree with those, the top-level docs win.
>
> Specific known divergences in the planning docs: per-Session struct mutex (now Locker plug-in, ADR-024); `agent.NewAgent` / `Run(ctx, projectID, ...)` (now `Session` / `NewSession`, ADR-012); `vfs.TxStorage`, `Tx`, `Snapshot`, `Restore`, `HashState` (all dropped, ADR-017/019); `tools.SchemaFromType[T]()` (dropped, ADR-018); `llm.Catalog` / `FindModel` / `Model` (dropped, ADR-025); `Message.CacheHint`, `Request.ThinkingEffort`, `iota`-EventKind, `Temperature float32` (all replaced by post-ADR-016 shapes); single `TurnTimeout` (split into RunTimeout + LLMCallTimeout); per-step `History.Append` (now variadic single-flush at end of turn). Use the planning docs for *implementation depth* (file layouts, transcript shapes, test names, OpenRouter wire detail) but cross-check call shapes against `API.md`.

Detailed implementation guidance produced by a 4-agent planning pass. The locked design lives in [`docs/`](../../) (ARCHITECTURE, DECISIONS, PATTERNS, SECURITY); the wave summary lives in [`v1/PLAN.md`](../PLAN.md). The five documents under this directory are the per-area depth an engineer needs to actually write the code without re-deriving design.

## Documents

| Doc | Lines | Purpose |
|-----|------:|---------|
| [REFERENCE-CODE.md](REFERENCE-CODE.md) | 1174 | Maps the production code aikido v1 generalizes (`asolabs/hub` agent loop, OpenRouter client, tool registry; `mxcd/go-basicauth` storage idiom). Verdict per file: lift / adapt / inspiration. Snippets to mirror. |
| [OPENROUTER-DETAILS.md](OPENROUTER-DETAILS.md) | 981 | Single-source wire-format reference for `llm/openrouter`: endpoints, request/response shape, SSE format, tool-call fragment-by-index assembly with worked examples, error mapping, model-ID gotchas, cache-control passthrough, and full payloads for all five canned test transcripts. |
| [WAVES-EARLY.md](WAVES-EARLY.md) | 1496 | W0–W4 (skeleton through tools registry). File trees, public API, internal types, errors, ordered implementation steps, named test scenarios, dependencies, risks, exit criteria. Includes verbatim `justfile`, `.golangci.yml`, `.github/workflows/ci.yml`, and a 10-entry catalog seed with realistic 2026 pricing. |
| [WAVES-LATE.md](WAVES-LATE.md) | 2169 | W5–W8 + v0.1.0 tag protocol. W5 conformance suite (25 sub-tests, tx-gated). W6 reproduces the agent runLoop as Go with 7 helpers and explicit `EndReason` rules. W7 per-tool sub-tables for all five built-ins. W8 six-step `Consolidate` algorithm + 165-word default consolidation prompt. |
| [TEST-STRATEGY.md](TEST-STRATEGY.md) | 1415 | 6-layer test pyramid, fixture catalog (13 files under `llm/openrouter/testdata/` + `StubClient`), ~181 named test functions across W0–W8, full CI workflow, coverage targets, and a 42-row risk register cross-referenced to SECURITY.md threats and per-wave test names. |

Total: 7,235 lines across the five docs.

## Read order

**Implementing a wave:** Start at [PLAN.md](../PLAN.md) for the wave summary. Open the relevant section in [WAVES-EARLY.md](WAVES-EARLY.md) or [WAVES-LATE.md](WAVES-LATE.md). For W2/W3 also keep [OPENROUTER-DETAILS.md](OPENROUTER-DETAILS.md) open. For W6 also keep [REFERENCE-CODE.md](REFERENCE-CODE.md) open at the `loop.go` section. For every wave, [TEST-STRATEGY.md](TEST-STRATEGY.md) is your test-name and fixture index.

**Architectural review:** [ARCHITECTURE.md](../../ARCHITECTURE.md) → [DECISIONS.md](../../DECISIONS.md) → [PATTERNS.md](../../PATTERNS.md) → this index.

**Risk audit:** Skip to [TEST-STRATEGY.md](TEST-STRATEGY.md) §"Risk register" for the 42-row matrix.

## Open questions to resolve

The four planning agents collectively surfaced ~38 open questions across the locked design. They are categorized below by urgency and by the wave that hits the question first.

### 🔴 Blocking — decide before the relevant wave starts

**Before W2/W3 (`llm/openrouter`):**

| # | Source | Question | Recommendation |
|---|--------|----------|----------------|
| 1 | REF Q1 / OR Q1 | `ProviderPreferences` — production code uses OpenRouter's `provider` field for routing; aikido has no equivalent. | Add `openrouter.Options.ProviderOrder []string` (provider-specific, doesn't touch `llm.Request`). 5-line addition. |
| 2 | OR Q1 | `finish_reason: "content_filter"` — no typed error today. | Emit `EventError` wrapping `ErrInvalidRequest`. Document. No new typed error in v1. |
| 3 | OR Q3 | `delta.reasoning` field on thinking models — `EventTextDelta` carries only `content`. | Silently discard reasoning tokens; log usage. Document as known v1 limitation. |
| 4 | OR Q5 | `X-RateLimit-Reset` unit (millis vs seconds) — unconfirmed by docs. | Log headers in W2 smoke, verify, lock retry policy after observation. |
| 5 | OR Q7 | `stream_options.include_usage` deprecation — does usage still appear without it for all providers? | Live-test in W2 smoke. Send neither flag if confirmed reliable. |

**Before W4 (`tools`):**

| # | Source | Question | Recommendation |
|---|--------|----------|----------------|
| 6 | EARLY Q6 | W4 forward-deps on `vfs.Storage` (`tools.Env.Storage`). PLAN orders W4 before W5. | Ship `vfs.Storage` interface stub (signatures only) as part of W4's PR. W5 fills in the body. |
| 7 | EARLY Q8 | First third-party dep: `invopop/jsonschema` version pin. | Pin to current stable at W4 PR time. Document min-version in `go.mod`. |
| 8 | TEST Q6 | `SchemaFromType[chan int]()` — panic or error on unsupported types? | Panic at registration (programmer error, not runtime condition). |

**Before W5 (`vfs` + memory + conformance):**

| # | Source | Question | Recommendation |
|---|--------|----------|----------------|
| 9 | TEST Q11 | `HashContent` canonical byte format — separator, content-hash format. | Separator `\n`, content-hash is hex SHA-256 of bytes. Lock with a fixed-value test (`TestHashContent_Empty`). |
| 10 | REF Q8 | Path validation should also live inside `Storage.WriteFile`/`DeleteFile`, not just tool layer. | Add validation in `vfs/memory.Storage` write paths and a conformance test that bypasses the tool layer. |
| 11 | TEST Q3 | `foo/` (trailing slash) path — reject or normalize? | Reject. |
| 12 | TEST Q4 | `foo//bar` (double slash) — reject or normalize? | Reject. |
| 13 | TEST Q15 | `RunConformance` — `t.Parallel()`-safe? | Serial by default. Optional `RunConformanceParallel` variant for opt-in backends. |

**Before W6 (`agent`):**

| # | Source | Question | Recommendation |
|---|--------|----------|----------------|
| 14 | TEST Q1 / EARLY Q3 | Cancellation `EndReason` — current spec emits `EventError` only for `ctx.Cancel`. | Add `"cancelled"` as a fifth `EndReason` value. ADR addendum or PLAN clarification. |
| 15 | TEST Q9 | `MaxConcurrentTurns` — public option or implicit? | Confirm: implicit. No public field. Concurrency is always 1 per project per ADR-009. |
| 16 | TEST Q10 | `BeginTx` returns an error mid-turn — abort turn or fall back to non-tx? | Fall back to non-tx with logged warning. Best-effort semantics still honor the model's tool dispatch. |
| 17 | LATE | `VFSToolOptions.HideHiddenPaths bool` zero-value vs documented default `true`. Caller can't distinguish "explicitly off" from "unset". | Either accept the asymmetry (defaulting inverts it after construction) or change to `*bool`. ADR addendum. |

### 🟡 Should resolve before v0.1.0 tag

| # | Source | Question | Recommendation |
|---|--------|----------|----------------|
| 18 | REF Q2 / EARLY Q2 | Error-wrap prefix convention: `"openrouter: marshal: %w"` vs `"marshal: %w"`. | Prefix package name. More verbose, more debuggable. |
| 19 | REF Q5 | `MaxTurns` default — production uses 25, aikido spec says 20. | Stick with 20 (per ARCHITECTURE.md table). |
| 20 | REF Q6 | `Message.Content` pointer vs value — production uses `*string`, API.md uses `string`. | Keep `string` (per API.md). Wire form uses `*string` internally for JSON-null. |
| 21 | REF Q9 / TEST Q9 | `Tools` registry mutation semantics — read once at `NewAgent` or per-`Run`? | Read at `Run` time. Document mutation between runs is OK; mid-run mutation is undefined. |
| 22 | EARLY Q1 | `Temperature` zero-value — `float32` cannot send explicit `0`. | Either accept (document) or change to `*float32`. Likely accept; few callers need explicit zero. |
| 23 | EARLY Q3 | Catalog seed pricing will drift before ship. | Final price pass at W1 implementation time. |
| 24 | LATE | `EndReason` typed-string vs exported `const EndReasonStop = "stop"` for typo safety. | Add the consts. No API.md change needed. |
| 25 | TEST Q2 | `ToolResult.OK` derived from handler error only, or from result content too? | Error-only. Registry never inspects `Result.Content`. |
| 26 | TEST Q5 | `write_file` `content_type` default when omitted by the model. | Infer from extension; `text/markdown` for unknown. |
| 27 | TEST Q7 | `StubClient` exhaustion (script consumed, agent calls `Stream` again). | Return `ErrStubExhausted`. |
| 28 | TEST Q8 | `Notebook.Consolidate` with empty notebook + existing target. | No-op. Do not call the LLM. Do not bill the user for nothing. |
| 29 | TEST Q13 | `go.uber.org/goleak` for goroutine-leak verification in test packages. | Yes. One test-only dep is cheaper than an in-flight goroutine bug in v0.1. |
| 30 | TEST Q14 | `just smoke` running cost cap — abort if accumulated cost exceeds threshold. | Add `≤ 20 LOC` cap to each example printing running cost; document expected cost in protocol. |
| 31 | LATE | `Notebook.Add` storage source — base storage vs active tx. | Document explicitly: handlers use `env.Storage` (tx-aware); programmatic API uses `nb.storage`. |

### 🟢 Defer

| # | Source | Question | Disposition |
|---|--------|----------|-------------|
| 32 | REF Q4 | Built-in `get_current_date` tool. | Never. Library not framework — caller injects clock-y tools. |
| 33 | REF Q7 | `Request.Extras map[string]any` escape hatch for provider-specific fields. | v2 design problem. v1 keeps provider-specifics on provider's `Options`. |
| 34 | LATE | `Options.ConsolidationPrompt string` for fully replacing default consolidation prompt. | v1.x additive if user demand surfaces. |
| 35 | OR Q2 | OpenAI `refusal` field on assistant messages. | Not exposed in v1; refusals also surface as plain text content. |
| 36 | OR Q4 | `completion_tokens_details.reasoning_tokens` not in `llm.Usage`. | Document gap. Adding the field would break locked surface; defer to v2. |
| 37 | OR Q8 | `cost` `float64` precision — fine for display, not for accounting. | Document the limitation in godoc. |
| 38 | OR Q6 | Mid-stream connection drop without an `error` envelope. | Confirm `bufio.Scanner` returns clean error in W3 testing; treat as `EventError + EventEnd("error")`. |

## API additions implied by the recommendations

If MaPa accepts the recommendations above, the following additive changes land in v1 (none breaks the locked surface):

```go
// Open Question 1 → openrouter.Options
type Options struct {
    // ... existing fields
    ProviderOrder []string  // nil = no preference
}

// Open Question 14 → agent.Event
const (
    EndReasonStop      = "stop"
    EndReasonMaxTurns  = "max_turns"
    EndReasonError     = "error"
    EndReasonTimeout   = "timeout"
    EndReasonCancelled = "cancelled"  // NEW
)

// Open Question 27 → internal/testutil
var ErrStubExhausted = errors.New("stub client: script exhausted")
```

Plus one possible breaking-but-additive question — `VFSToolOptions.HideHiddenPaths *bool` (Q17) — that should be decided now, before any caller writes code against the value-type form.

## What this team did NOT do

- **Write any Go code.** Only planning markdown.
- **Change `API.md`.** All recommendations are surfaced as open questions, not silent edits.
- **Add new ADRs.** Tensions are flagged with `→ ADR addendum?` notes; the human decides.
- **Verify against a live OpenRouter account.** OPENROUTER-DETAILS.md cites docs and notes the items requiring a W2 smoke pass (rate-limit headers, deprecated-flag behavior).

When the open questions above are triaged, those decisions feed back into `DECISIONS.md` (as ADR addenda or new ADRs) or `API.md` (as additive fields). The wave docs already encode the recommendations; they are not blocked on triage.

---

## Resolution status (post-second-grilling, 30.04.2026)

| # | Original question | Status |
|---|-------------------|--------|
| 1 | `ProviderPreferences` (`openrouter.Options.ProviderOrder`) | **Resolved** — added to `openrouter.Options` per recommendation. |
| 2 | `finish_reason: "content_filter"` | Open — ship as recommended (`EventError` wrapping `ErrInvalidRequest`). |
| 3 | `delta.reasoning` field on thinking models | **Resolved** — `EventThinking` now exists (ADR-016 close-out); reasoning is emitted, not discarded. |
| 4 | `X-RateLimit-Reset` unit | Open — verify in W2 smoke. |
| 5 | `stream_options.include_usage` | Open — verify in W2 smoke. |
| 6 | W4 forward-deps on `vfs.Storage` | Moot — wave structure changed; new W3 (`tools`) precedes W4 (`vfs`). Tools package no longer depends on `vfs.Storage` (ADR-021 slimmed `Env`; closure-capture replaces injection). |
| 7 | `invopop/jsonschema` version pin | Moot — `SchemaFromType` removed (ADR-018); the dependency is dropped from v1. |
| 8 | `SchemaFromType[chan int]()` panic vs error | Moot — `SchemaFromType` removed (ADR-018). |
| 9 | `HashContent` canonical byte format | Moot — `HashState` removed from `Storage` (ADR-017). |
| 10 | Path validation in `vfs/memory` write paths | Open — keep recommendation: validate on entry, conformance test bypassing tool layer. |
| 11 | Trailing-slash path normalization | Open — keep recommendation: reject. |
| 12 | Double-slash path normalization | Open — keep recommendation: reject. |
| 13 | `RunConformance` `t.Parallel()` safety | Open — keep recommendation: serial by default. |
| 14 | `EndReason` for cancellation | **Resolved** — `EndReasonCancelled` already in `API.md`. |
| 15 | `MaxConcurrentTurns` public option | **Resolved** — Locker plug-in (ADR-024) replaces it. Concurrency is "one Run per session ID per `Locker`," configured by the choice of `Locker` impl. |
| 16 | `BeginTx` mid-turn error | Moot — `Tx` removed from v1 (ADR-019). |
| 17 | `VFSToolOptions.HideHiddenPaths` zero-value vs default | Open — accept asymmetry for v1 (lean stance per Grill #11/#12). Reconsider as `*bool` in v1.x if real callers need to express "explicitly off". |
| 18 | Error-wrap prefix convention | Open — keep recommendation: prefix package name. |
| 19 | `MaxTurns` default 20 vs 25 | **Resolved** — 20, locked. |
| 20 | `Message.Content` pointer vs value | Open — keep recommendation: `string`. |
| 21 | `Tools` registry mutation semantics | Open — keep recommendation: read at `Run` time. |
| 22 | `Temperature` zero-value | **Resolved** — changed to `*float32` with `llm.Float32` helper (ADR-016 close-out). |
| 23 | Catalog seed pricing drift | Moot — catalog dropped from v1 (ADR-025). |
| 24 | `EndReason` typed-string vs exported consts | **Resolved** — both: `EndReasonStop` etc. consts already in `API.md`; `EventKind` now typed `string`. |
| 25 | `ToolResult.OK` derived from handler error | Open — keep recommendation: error-only. |
| 26 | `write_file` content_type default | Open — keep recommendation: infer from extension. |
| 27 | `StubClient` exhaustion | Open — keep recommendation: `ErrStubExhausted`. |
| 28 | `Notebook.Consolidate` empty notebook | Moot — `notes` package deferred (ADR-015). |
| 29 | `goleak` test-only dep | Open — keep recommendation: yes. |
| 30 | `just smoke` cost cap | Open — keep recommendation: print running cost. |
| 31 | `Notebook.Add` storage source | Moot — `notes` package deferred (ADR-015). |
| 32 | Built-in `get_current_date` tool | Defer (never). |
| 33 | `Request.Extras` escape hatch | Defer (v2). |
| 34 | `Options.ConsolidationPrompt` | Moot — `notes` package deferred (ADR-015). |
| 35 | OpenAI `refusal` field | Defer (not exposed in v1). |
| 36 | `reasoning_tokens` in `Usage` | Defer (v2). |
| 37 | `cost` `float64` precision | Defer (document limitation). |
| 38 | Mid-stream connection drop | Open — verify in W2 testing. |

**Note on wave structure (Grill #12c):** the original W2 ("non-streaming path") and W3 ("real streaming") waves were collapsed into a single new W2 ("real streaming + tool-call assembly + retry"). Subsequent wave numbers shift down by one in the post-grilling PLAN.md.
