# v2 Scope (Outline)

v2 is the toolkit MaPa wants for the headline use cases: image generation invoked by other AI agents (CLI), real-time voice agents, and durable job execution. It is a strict superset of v1 — no v1 breaking changes.

This document is **outline only**. Detailed API design happens during the v2 planning pass once v1 is shipped and at least one external user is on it.

---

## Goals

1. Image generation as the first CLI use case.
2. Streaming STT and TTS so the v1 agent loop can drive voice agents.
3. Direct provider clients for cache-control fidelity OpenRouter cannot guarantee.
4. Job queues with configurable retry and exponential backoff for long-running agent runs and image jobs.
5. The CLI binary itself: `aikido chat`, `aikido agent`, `aikido image`, `aikido tts`, `aikido stt`.

## Non-goals

- Multi-instance lock coordination via a bundled Redis Locker — ships in **v1.1** as `agent/locker/redis`, not v2 (ADR-024). The `agent.Locker` interface itself ships in v1.
- SSE-over-HTTP wrapper — v3.
- Vector store, embeddings — out of scope permanently.
- A managed service — out of scope permanently.

---

## Packages

### `image`

Image generation behind a small interface that mirrors `llm.Client` in shape.

**Open design questions for v2 planning:**

- Do we expose a single-shot `Generate(ctx, req) (Image, error)` or also a `Stream`-style progress channel? Some providers stream progress events; some return only on completion.
- Reference images: byte content, URL, or both?
- Output: byte content, presigned URL, or callback?
- Failure modes: how do we surface NSFW filters, content policy violations, partial generations?

**Initial provider:** OpenRouter image endpoints. Direct providers (`image/openai/dall-e`, `image/anthropic`) follow the same `llm` v1 → v2 pattern.

### `audio/stt`

Streaming speech-to-text.

**Open design questions:**

- Frame format: PCM16 base64 vs raw bytes vs WAV?
- Sample-rate negotiation: provider-driven or caller-driven?
- VAD and endpointing: in the library or in the provider?
- Partial vs final transcripts: separate event kinds?

**Initial provider:** ElevenLabs Scribe via WebSocket (per the DeepThought `ElevenLabs-TTS-WebSocket-Streaming-Pattern` and `Voice-Agent-Latency-Budget-Framework` references).

### `audio/tts`

Streaming text-to-speech.

**Open design questions:**

- Incremental write-text vs send-once?
- Chunk schedules (latency vs prosody) — caller-tunable?
- Output codec negotiation (MP3 vs μ-law for telephony)?
- Backpressure: caller-managed or provider-managed?

**Initial provider:** ElevenLabs TTS WebSocket streaming.

### `audio/elevenlabs`

The first STT and TTS provider. Implements both `audio/stt.Provider` and `audio/tts.Provider`. WebSocket-based; chunking schedule reuses the asolabs/callcenter values that worked in production.

### `llm/anthropic`

Direct Anthropic client. Reasons: provider-native prompt caching with full `cache_control` semantics; OpenAI's structured-output features that OpenRouter does not forward. Implements `llm.Client`. Same streaming and tool-call assembly contract.

### `llm/openai`

Direct OpenAI client. Same shape. Useful when callers need OpenAI-specific behaviors (structured outputs, reasoning models with provider-native thinking-effort).

### `queue`

Job queue interface. Supports asynchronous execution of agent runs, image generation, and TTS pipelines.

**Open design questions:**

- Queue interface shape — is `Submit(ctx, job)` plus `Worker.Run(ctx)` enough, or do we need a higher-level `Pool` abstraction?
- Backends: bundled `queue/memory`, `queue/redis`, `queue/postgres`. Reuse the storage-callback pattern (interface implementation) for consistency.
- Retry: per-job `RetryPolicy` or global `Queue.DefaultRetry`? Probably both with per-job override.
- Result delivery: callback, `<-chan JobResult`, or stored in the queue for later poll? Probably all three depending on backend.
- Idempotency: caller-supplied job key? Or library-generated UUID?

**Backends to ship in v2:**

- `queue/memory` — in-process (test-grade).
- `queue/redis` — Redis-backed.
- `queue/postgres` — Postgres-backed (uses `LISTEN/NOTIFY` for wakeup).

### `cmd/aikido`

The CLI binary. Cobra-based. All v1 functionality plus v2 modalities.

**Subcommands:**

- `aikido chat "<prompt>"` — one-shot chat completion.
- `aikido agent --project <dir> "<prompt>"` — run agent loop with VFS-backed local dir (requires a v2 `vfs/local` impl, deferred or a `cmd/aikido` private impl).
- `aikido image --reference <file> "<prompt>"` — image generation. Headline use case.
- `aikido tts "<text>" --voice <voice> --out <file>` — text to speech.
- `aikido stt --in <file>` — speech to text.

The CLI uses `mxcd/go-config` for env-driven defaults; flags override.

---

## v2 dependencies on v1 surface

- `image.Client` and `audio.*.Provider` mirror `llm.Client`'s single-streaming-method shape so users can switch providers in one constructor.
- `queue.Job` carries a session spec for agent jobs — meaning the v1 agent surface is reused unchanged.
- `cmd/aikido agent` is a thin wrapper over `agent.NewSession` (or `agent.NewLocalSession`) plus a memory or local VFS — pure v1 reuse.
- v2's `llm/anthropic` and `llm/openai` packages ship internal pricing tables for cost computation per provider call — closing the token / cost tracking requirement deferred from v1's catalog drop (ADR-025).

If v2 design pressure forces breaking changes to v1 types, the change is split: bump v1 to a new minor with additive fields, do not break v1 callers, document migration.

---

## Sequencing for v2

Tentative wave numbering (`Wxx` to distinguish from v1):

| Wave | Deliverable |
|------|-------------|
| W20 | `cmd/aikido` skeleton with `chat` and `agent` subcommands (just wraps v1). |
| W21 | `image` interface + `image/openrouter` provider + `aikido image` subcommand. |
| W22 | `audio/tts` interface + `audio/elevenlabs/tts` + `aikido tts` subcommand. |
| W23 | `audio/stt` interface + `audio/elevenlabs/stt` + `aikido stt` subcommand. |
| W24 | Direct providers: `llm/anthropic`, `llm/openai`. |
| W25 | `queue` interface + `queue/memory` + retry policy types. |
| W26 | `queue/redis` and `queue/postgres` backends. |
| W27 | Worked agent example using `queue` for long-running runs. |

Total estimate: ~6 weeks of focused work.

---

## What v2 does NOT change about v1

- The `llm.Client` interface is **locked**. New providers extend, never modify.
- `vfs.Storage` is **locked**. New capability interfaces (`Searchable` already in v1; `Embeddable` if needed) are additive.
- `agent.History` and `agent.Locker` are **locked**. New backends are additive.
- `agent.SessionOptions` may grow new optional fields; no existing field removed or repurposed.
- `EventKind` is typed `string` in both `llm` and `agent`; new event kinds are additive.
- The note-then-consolidate pattern continues to live as a recipe in `PATTERNS.md` until 2+ production validations justify a first-class package (per ADR-015).

Any tension between a v2 design need and v1 stability is resolved in v1's favor: ship v2 functionality alongside v1 contracts, do not change v1.
