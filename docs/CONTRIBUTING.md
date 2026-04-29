# Contributing

aikido is small, opinionated, and meant to stay that way. Read [DECISIONS.md](DECISIONS.md) before proposing structural changes — load-bearing decisions are deliberately frozen.

## Dev setup

**Prerequisites:**

- Go 1.23 or newer
- [`just`](https://github.com/casey/just) for task running
- [`golangci-lint`](https://golangci-lint.run/) for linting

**Bootstrap:**

```sh
git clone https://github.com/mxcd/aikido
cd aikido
just check        # vet + lint + test
```

## Repository layout

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full package map. In short:

- `llm/`, `llm/openrouter/`, `llm/llmtest/` — provider abstraction and helpers.
- `tools/`, `vfs/`, `vfs/memory/` — tool registry and storage.
- `agent/`, `agent/history/memory/` — agent loop and bundled in-memory backends. (v1.1 adds `agent/locker/redis`.)
- `internal/retry/` — internal exponential backoff helper.
- `examples/` — runnable examples; every file must compile in CI.
- `docs/` — what you are reading.

## Test strategy

aikido tests without network access by default. The patterns:

| Surface | Approach |
|---------|----------|
| `vfs/memory` and any future `Storage` implementation | The reusable `vfs/conformance_test.go` suite. Run it against your impl with `vfs.RunConformance(t, factory)`. |
| `agent/history/memory` and any future `History` implementation | The reusable `agent/history/conformance_test.go` suite. Same pattern as the VFS suite. |
| `agent.Locker` implementations | Tests verifying that two `Run` calls on different `*Session` values with the same ID serialize when sharing a `Locker`, and run in parallel for different IDs. |
| `llm/openrouter` and any future provider | Canned SSE transcripts under `<provider>/testdata/`. Tests run against `httptest.NewServer` replaying the transcripts. |
| `agent` | `llm/llmtest.StubClient` — script the model's behavior turn-by-turn. Assert event sequence and final VFS / History state. |
| `examples/` | `go build ./examples/...` is part of `just check` — every example must compile. |

**Real smoke tests** (manual, with `OPENROUTER_API_KEY` set):

```sh
export OPENROUTER_API_KEY=sk-or-...
go run ./examples/chat-oneshot
go run ./examples/agent-vfs
```

These are not part of CI. Run them before tagging a release.

## PR conventions

aikido ships in **waves** — see [v1/PLAN.md](v1/PLAN.md). Each wave is one PR. Each PR ticks the wave's exit criteria.

### Per-PR checklist

- [ ] One wave (or one well-scoped slice of one wave).
- [ ] `just check` is green.
- [ ] New public types and funcs have godoc comments.
- [ ] No new third-party dependency without a justification in the PR description.
- [ ] No backwards-incompatible changes to v1 public surface (additive only after the first tagged release).
- [ ] `examples/` builds, including any new example.
- [ ] PR description references the wave (e.g., "W3: streaming + tool-call assembly") and the exit criteria.

### Code style

- **Minimal comments.** Function and type names carry meaning. Add a comment only when the WHY is non-obvious — a hidden constraint, a workaround for a provider quirk, an invariant that future readers must preserve.
- **Naming is picky.** `llm` not `client`. `vfs` not `storage`. `notes` not `consolidation`. Use the names from [ARCHITECTURE.md](ARCHITECTURE.md). Disagree in a PR if you have a better one — but disagree explicitly.
- **No Go-idiomatic abuses.** No global state. No init() side effects. No package-level mutable variables. Constructors take `*Options` and return `(*X, error)`.
- **Errors carry context.** Wrap with `fmt.Errorf("...: %w", err)` at every layer boundary. Provider errors wrap a typed error so callers can distinguish rate limits from auth failures.
- **No premature abstraction.** Three similar lines is better than a half-baked helper. Extract only when the third use site arrives.
- **Date formatting.** When dates appear in docs, use `dd.MM.yyyy`.

### Naming reference

Picked carefully; copy the convention rather than inventing your own.

- Packages are short, lowercase, no underscores: `llm`, `vfs`, `tools`, `agent`.
- Types are PascalCase, no `I` prefix on interfaces.
- Constructors are `NewX(opts *Options)`.
- Compile-time interface conformance lines belong at the top of the implementing file: `var _ llm.Client = (*Client)(nil)`.
- Test files end in `_test.go`. Conformance suites are `<package>_conformance.go` exporting a single `Run<Name>(t *testing.T, factory)` function.
- Receiver names are short and consistent within a file: `c *Client`, `r *Registry`, `s *Session`, `l *LocalLocker`.

## Tagging tech debt

aikido follows MaPa's "tech debt → TODO.md" convention. If you spot a defect outside the current PR's scope, append it to `TODO.md` with a one-line description and the rationale. Do not fix it in the current PR.

Example TODO entries:

```
- [ ] llm/openrouter: handle provider 502s during stream-start (currently bubbles as generic error). Why: hides retryable vs fatal.
- [ ] agent: emit Logger structured events at turn-start / turn-end / tool dispatch with stable attribute names. Why: locks the observability contract for v1.x dashboards. Currently the Logger is plumbed but emission is implementation-defined.
```

## Where the design discussions live

Every load-bearing decision is in [DECISIONS.md](DECISIONS.md). Patterns and references are in [PATTERNS.md](PATTERNS.md). v2 scope is in [v2/SCOPE.md](v2/SCOPE.md). PRs that change the design (new public type, new ADR-worthy choice) update DECISIONS.md as part of the same PR.

## What not to PR

- New providers in v1. Wait for the v1 release; new providers (`llm/anthropic`, `llm/openai`) are explicitly v2.
- A CLI binary. v2.
- Image, audio, or queue packages. v2.
- A `vfs/postgres` or `agent/history/postgres` reference impl. v1 keeps the bundled backends to memory only — see [DECISIONS.md ADR-008](DECISIONS.md), [ADR-014](DECISIONS.md).
- `agent/locker/redis`. Ships in v1.1 — see [DECISIONS.md ADR-024](DECISIONS.md). For v1, multi-replica callers implement `agent.Locker` themselves.
- An `llm.Catalog` revival. Dropped in v1; per-provider catalogs are v2 — see [ADR-025](DECISIONS.md).
- Reflective `tools.SchemaFromType[T]()`. Dropped in v1 — see [ADR-018](DECISIONS.md).
- A `notes/` package. Deferred to v1.x — see [ADR-015](DECISIONS.md). Pattern recipe is in [PATTERNS.md](PATTERNS.md).
- A `vfs.TxStorage` capability. Dropped in v1 — see [ADR-019](DECISIONS.md). Candidate for v1.x re-introduction if a second use case proves the abstraction is real.

If you think one of these should land sooner, open an issue first to discuss the scope shift before writing code.
