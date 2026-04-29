# Patterns

aikido implements proven patterns from MaPa's DeepThought knowledge base. Each row maps a pattern to its aikido package and notes any deltas. The patterns are the source of truth for "why is it built this way" — refer to them whenever a design choice surfaces in implementation.

DeepThought patterns live at `~/Nextcloud/DeepThought/Patterns/` (and `References/`). Read them when implementing or extending the corresponding package.

## Implemented in v1

| Pattern | aikido Implementation | Delta from pattern |
|---------|------------------------|--------------------|
| `Pluggable-LLM-Client-Interface-Pattern` | `llm/` — `Client`, `Request`, `Event`, `Message`, `ToolDef`, `ToolCall`, `Usage`, `CacheBreakpoint`, `ThinkingConfig` | Drop `Message.Name` (only ever held tool name; lives on `ToolCall` instead). Add `Message.Images` for multi-modal. `EventKind` is typed `string` for forward-compat. |
| `VFS-Storage-Interface-Pattern` | `vfs/` — `Storage` (4 methods), `FileMeta`, optional `ScopedStorage` and `Searchable` capabilities; `vfs/memory` impl | Reduced to four-method base contract (ADR-017): no `Snapshot`/`Restore`/`HashState`. Capability extensions are additive (`ScopedStorage` for tenant binding, `Searchable` for backend-native search). No `TxStorage` in v1 (ADR-019). |
| `Custom-Go-Agent-Loop-Over-SDK-Pattern` | `agent/` — `Session`, `NewSession`, `NewLocalSession`, `Run`, `RunWithMessages` | Session-based (multi-turn shares a session ID; ADR-012). History is a pluggable interface (ADR-014, variadic single-flush `Append`). Concurrency is a pluggable `Locker` interface (ADR-024). Hard guardrails baked in (`MaxTurns`, `RunTimeout`, `LLMCallTimeout`). |
| `Streaming-Tool-Call-Assembly-Pattern` | `llm/openrouter/sse.go` — `assembleToolCalls` | Lift verbatim. Buffered by index, emitted only at choice-done. |
| `SSE-Agent-Stream-Protocol-Pattern` | `agent/event.go` — events as `<-chan agent.Event` (Go-channel form, not HTTP SSE) | Same event vocabulary (text, thinking, tool_call, tool_result, usage, end). HTTP SSE wrapper is v3. |
| `Agent-Tool-Sandbox-Pattern` | `agent/tools_builtin.go` + `agent/RegisterVFSTools` + `vfs.ValidatePath` | No bash, no fetch, no filesystem. Hidden-path filter for `_*` defaults to on. Path validation lives in `vfs/path.go` (structural invariants); caller policy filters live in `agent.VFSToolOptions`. |
| `Anthropic-Prompt-Caching-Cost-Control` | `llm.Message.Cache *CacheBreakpoint` with `CacheTTL` enum → `cache_control: ephemeral` passthrough | OpenRouter forwards to Anthropic-routed models; non-Anthropic providers silently no-op. v2's `llm/anthropic` adds direct-provider fidelity. |
| `OpenRouter-Model-ID-Hyphen-Normalization-Gotcha` | `llm/openrouter/modelid.go` — `normalizeModelID` | Lift verbatim. `dots → hyphens` at request time. |
| `VFS-Postgres-Schema-Pattern` | User-supplied `Storage` impl in v1 (Ent-based) | Pattern documents the schema users should adopt; aikido does not ship a Postgres impl in v1 (ADR-008). |

## Referenced for tone and style

| Pattern | How it shapes aikido |
|---------|----------------------|
| `Working with MaPa` (References) | Minimal comments, focused PRs, options-pattern, single-binary deploys, picky on naming. |
| `Project Patterns` (Atlas) | Repository pattern, options pattern, event-driven mutations. |
| `OpenRouter-Audio-API-Realtime-Mismatch-Gotcha` | Informs v2 audio package design — separate STT/TTS providers preferred over combined audio LLM. |
| `Voice-Agent-Latency-Budget-Framework` (References) | Will inform v2 audio loop tuning (phrase buffering, TTS chunking schedules). |
| `ElevenLabs-TTS-WebSocket-Streaming-Pattern` (References) | Reference for v2 `audio/elevenlabs` provider. |

## Deferred to v2

These patterns are referenced but not implemented in v1:

| Pattern | v2 Location |
|---------|-------------|
| `OpenRouter-Audio-API-Realtime-Mismatch-Gotcha` | `audio/stt`, `audio/tts` design notes. |
| `ElevenLabs-TTS-WebSocket-Streaming-Pattern` | `audio/elevenlabs` provider. |
| `Voice-Agent-Latency-Budget-Framework` | `agent` extensions for audio pipeline. |

## Reference codebases

When in doubt about API shape or naming, look at these. They are MaPa's working code and encode his style.

| Codebase | What to learn from it |
|----------|------------------------|
| `~/github.com/mxcd/go-basicauth/storage_interface.go` | Storage + capability-interface idiom (`AtomicBackupCodeConsumer`). |
| `~/github.com/mxcd/go-basicauth/handler.go` | `Options` struct + `NewX(opts)` construction style. |
| `~/github.com/asolabs/hub/internal/client/openrouter/client.go` | Working OpenRouter request/response shapes; image decoding for v2. |
| `~/github.com/asolabs/hub/sandbox/agent-test/agent/loop.go` | Production 267-LOC agent loop. aikido's `agent` package generalizes this. |
| `~/github.com/asolabs/hub/internal/nexi/tools.go` | Existing tool registration style — explicit JSON Schema literals. |
| `~/wilde/gitlab.wilde-it.com/afb/defector/internal/inference/driver.go` | `Driver` interface precedent for provider abstraction. |
| `~/wilde/gitlab.wilde-it.com/valu-media/evaluator/api/internal/job/ai.go` | OpenRouter via OpenAI SDK + JSON Schema mode (anti-pattern reference: opaque error types, no streaming). |
| `~/github.com/asolabs/callcenter/src/llm/types.ts` | TypeScript `LlmProvider` interface — single-method shape mirrored in `llm.Client`. |
| `~/github.com/asolabs/callcenter/src/tools/registry.ts` | `ToolRegistry` shape — `register`, `schemas`, terminal flag. aikido drops the terminal flag in v1; reintroduce in v2 if needed. |

## Note-then-consolidate (recipe — not a v1 package)

Per ADR-015, the note-then-consolidate pattern is a documented recipe in v1, not a first-class library construct. Promotion to a `notes/` package is a v1.x consideration once 2+ production validations exist outside the original PRD generator.

The recipe in caller code, in ~30 lines:

1. Decide a path layout for atomic notes (e.g., `_notes/{turn-uuid}.md`). The `_` prefix triggers `agent.VFSToolOptions.HideHiddenPaths`'s default-on filter, so `list_files` returns notes only when explicitly asked.
2. Register two custom tools alongside `RegisterVFSTools`:
   - `add_note(text)` — handler writes `_notes/{uuid.New()}.md` to the bound `vfs.Storage`.
   - `consolidate_notes_into_doc(target_path, instructions)` — handler reads all `_notes/*.md` plus the existing `target_path` (if any), runs one `llm.Collect` call with the supplied instructions to produce a merged document, writes the result back, then deletes the consumed note files.
3. The system prompt instructs the model: "after each user message, call `add_note` once with one atomic fact; on the user's `done` signal, call `consolidate_notes_into_doc` with the appropriate target path."

What you gain: agents that don't have to rewrite the full document on every turn. What you avoid: prematurely freezing path layout, consolidation prompt, or note-per-turn assumption inside aikido while the pattern is still N=1.

When N≥2 production users validate the same shape, the recipe gets promoted to a `notes/` package additively. Until then, it lives here.
