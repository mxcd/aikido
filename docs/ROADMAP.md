# Roadmap

Three-tier trajectory. v1 ships the load-bearing core. v1.1 closes the multi-replica gap. v2 turns aikido into the toolkit MaPa wants for image-gen-via-CLI and voice agents.

## v1 — Core (target: ~3 weeks of focused work)

The provider-agnostic foundation. Every feature in v1.1 / v2 sits on top of this surface, so it has to be right.

### Packages

| Package | Purpose |
|---------|---------|
| `llm` | Provider-agnostic types: `Client`, `Request`, `Message`, `ToolDef`, `ToolCall`, `Event`, `Usage`, `CacheBreakpoint`, `ThinkingConfig`. |
| `llm/openrouter` | First provider. Streaming-first SSE, tool-call assembly, 429/5xx retry at stream-start. |
| `llm/llmtest` | Public test helpers — `StubClient` for callers' own integration tests. |
| `tools` | Tool registry, dispatch, explicit JSON Schema helpers. |
| `vfs` | `Storage` interface, optional `ScopedStorage` and `Searchable` capabilities, path validation. Memory backend bundled. |
| `agent` | Session-based streaming agent loop. Pluggable `History` and `Locker` (in-memory backends bundled). Built-in VFS tools. `NewLocalSession` convenience constructor. |
| `agent/history/memory` | Bundled in-memory `History` backend. |
| `internal/retry` | Internal exponential backoff helper. |

### Capabilities at v1 release

- Stream chat completion against any OpenRouter-routed model with full multi-modal (image) input support; explicit thinking budget / effort knob; per-message cache breakpoints with TTL.
- Define tool schemas explicitly via `tools.Object/String/Enum/...`; register and dispatch them through a typed registry. Struct-driven schemas via `invopop/jsonschema` directly when callers prefer that style.
- Plug in any storage backend that satisfies `vfs.Storage`. Use the bundled in-memory backend in tests, supply your own (Ent in production) for persistence.
- Plug in any conversation-history backend that satisfies `agent.History`. Use the bundled in-memory backend or supply your own (Postgres-, Mongo-, anything-keyed-by-string).
- Plug in any cross-replica lock backend that satisfies `agent.Locker`. Bundled `agent.LocalLocker` is production-grade for single-replica deployments; multi-replica callers implement against Redis or whatever they have.
- Spin up an agent with a single `agent.NewLocalSession(...)` call for single-replica use, or `agent.NewSession(...)` with explicit `History` + `Locker` for production. Register the built-in VFS tools to get a working knowledge-base agent in under 30 lines of glue code.
- Two timeouts (`RunTimeout` and `LLMCallTimeout`) bound the agent loop independently of `MaxTurns`.
- Strict error policy: any `History` or `Locker` failure is terminal for the current `Run`; the durable transcript stays consistent.

### Explicit v1 non-goals

- No CLI binary (joins v2).
- No image generation (joins v2).
- No STT/TTS (joins v2).
- No bundled Postgres or filesystem VFS backends — bring your own.
- No bundled Postgres or Redis history backends — bring your own.
- No bundled Redis Locker — joins **v1.1** (ADR-024). Multi-replica callers implement `agent.Locker` themselves before then.
- No model catalog (`Catalog`, `FindModel`, `Model`) — caller-side concern (ADR-025). OpenRouter returns `usage.cost` natively.
- No `notes` package — pattern documented as a recipe in `PATTERNS.md` (ADR-015).
- No reflective `tools.SchemaFromType[T]()` — explicit schema only (ADR-018).
- No `vfs.Tx` / `Snapshot` — agent loop is non-transactional (ADR-017, ADR-019).
- No structured slog logging contract — `Logger` plumbed but emission lean for v1.0.
- No job queue. The library performs LLM call retries internally; long-running orchestration is the caller's concern in v1.
- No MCP server, no orchestration framework, no UI, no built-in vector store.

## v1.1 — Multi-replica Locker

One additive package; no v1 breaking changes.

| Package | Purpose |
|---------|---------|
| `agent/locker/redis` | Redis-backed `agent.Locker` implementation with abstract `Client` interface (two methods: `SetNX`, `Eval`) so aikido does not import any specific Redis client library. Token-matched Lua-script unlock; periodic refresh; configurable TTL / acquire timeout. |

Caller wires their existing `go-redis` / `redigo` / `rueidis` connection through a small adapter satisfying the `Client` interface, and passes the resulting `Locker` into `agent.SessionOptions.Locker`. Multi-replica deployments get correct cross-process concurrency control with no aikido-side Redis dep.

## v2 — Modalities, queues, CLI

The toolkit MaPa's CLI use case (image gen invoked by other AI agents) needs. Built strictly on the v1 surface; no v1 breaking changes allowed.

### New packages

| Package | Purpose |
|---------|---------|
| `image` | Image generation. System prompt plus reference images. OpenRouter image endpoints first; direct providers follow. |
| `audio/stt` | Streaming speech-to-text. |
| `audio/tts` | Streaming text-to-speech. |
| `audio/elevenlabs` | First STT and TTS provider. |
| `llm/anthropic` | Direct Anthropic client for cache-control fidelity that OpenRouter cannot guarantee. Ships its internal pricing table for cost computation per token (see also: token / cost tracking). |
| `llm/openai` | Direct OpenAI client. Ships its internal pricing table for cost computation per token. |
| `queue` | Job queue interface. Memory, Redis, and Postgres backends. Per-job retry and exponential backoff configuration. |
| `cmd/aikido` | The CLI. Subcommands: `chat`, `agent`, `image`, `tts`, `stt`. |

### Capabilities at v2 release

- `aikido image --reference photo.jpg "make this a watercolor"` from any AI agent shell-out.
- Real-time voice agents over the v1 agent loop with STT in front and TTS behind.
- Direct provider clients with provider-native prompt caching.
- Per-provider token / cost accounting via internal pricing tables (the requirement noted during the catalog drop in ADR-025).
- Distributed job queue for long-running agent runs and image jobs.

### v2 non-goals

- No SSE-over-HTTP wrapper for serving agent runs to web clients (v3).
- No vector store, no embedding APIs.

## v3+ — Scale and operability

Driven by production needs once at least one external user is on v2.

- **Direct-Anthropic prompt caching fallback** — when OpenRouter cache forwarding is unreliable for a given model family.
- **SSE-over-HTTP wrapper** — surface the agent's `<-chan Event` as a `text/event-stream` HTTP response with a single helper.
- **Distributed queue with backpressure** — worker pool, rate limiting per provider, fair scheduling across tenants.
- **Operational telemetry** — OpenTelemetry spans across the agent loop, latency histograms per provider call, structured slog event vocabulary for cost accounting hooks.

## Versioning policy

aikido follows semver. **Breaking API changes happen only across major versions.** v1.x is additive. The `llm.Client` interface is locked at v1.0; new providers extend it, never modify it. The `vfs.Storage`, `agent.History`, and `agent.Locker` interfaces are the same: future capability interfaces are additive, never breaking.

When a v2 package needs a v1 type to grow, the type grows additively (new optional fields). No silent semantic changes inside v1.

## What aikido will never be

- A LangChain-style orchestration framework. aikido has one agent loop and pluggable storage; orchestration is the caller's job.
- An MCP server or client. MCP is a transport; aikido is a library.
- A vector database. Embedding and similarity search are downstream of aikido and live in the caller's storage layer.
- A managed service. The library is BYO-API-keys.
