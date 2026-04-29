# aikido

**AI KIt DOing things.** A Go library that gives applications a single, provider-agnostic interface for running AI inference — chat completion, tool calling, virtual file system, agent loops, and a first-class note-then-consolidate pattern. Backed by OpenRouter in v1, designed for direct providers (Anthropic, OpenAI, Mistral, ElevenLabs) in v2.

## Why aikido exists

Every Go service that ships AI features ends up writing the same plumbing twice:

- a bespoke OpenRouter client with hand-rolled SSE parsing,
- an ad-hoc tool registry and dispatch loop,
- a custom agent loop that mixes provider semantics, transaction handling, and user concerns,
- a knowledge-base-as-files abstraction that's never quite reusable.

aikido extracts the proven patterns into one package set so future projects reuse them instead of rebuilding.

## Who it is for

Go services that:

- run **agent loops** over a project workspace (read/write markdown, search, take notes),
- need **provider portability** (start on OpenRouter, switch to direct Anthropic later),
- want **storage flexibility** (memory in tests, Ent + Postgres in production),
- value **single-binary deploys** with no Node sidecar.

aikido is a library, not a framework. It does not own your HTTP layer, your DB, or your message log. It plugs into them.

## 30-second quickstart

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/mxcd/aikido/agent"
    "github.com/mxcd/aikido/llm/openrouter"
    "github.com/mxcd/aikido/tools"
    vfsmem "github.com/mxcd/aikido/vfs/memory"
)

func main() {
    client, _ := openrouter.NewClient(&openrouter.Options{
        APIKey: os.Getenv("OPENROUTER_API_KEY"),
    })

    storage := vfsmem.NewStorage()                  // unscoped knowledge base
    registry := tools.NewRegistry()
    _ = agent.RegisterVFSTools(registry, &agent.VFSToolOptions{Storage: storage})

    // NewLocalSession auto-supplies in-memory History + LocalLocker.
    // For multi-replica deployments, use NewSession and pass your own.
    session, _ := agent.NewLocalSession(&agent.SessionOptions{
        ID:           "demo-session",
        Client:       client,
        Tools:        registry,
        Model:        "anthropic/claude-sonnet-4-6",
        SystemPrompt: "You are a helpful research assistant.",
    })

    events, _ := session.Run(context.Background(), "Make a notes file with three facts about Go.")
    for ev := range events {
        if ev.Kind == agent.EventText {
            fmt.Print(ev.Text)
        }
    }
}
```

Multi-tenant variant — bind a scope-aware backend before registering VFS tools:

```go
backend  := hub.NewVFS()                      // implements vfs.ScopedStorage
storage  := backend.Scope("business-42")      // returns plain vfs.Storage
agent.RegisterVFSTools(registry, &agent.VFSToolOptions{Storage: storage})
```

The session itself does not see the scope. Callers map the opaque `Scope` string to whatever domain identifier they have (business UID, PRD session ID, tenant code, …).

Multi-replica variant — share one `Locker` across all `Session`s in the process; supply your own backend impl (Redis, etcd, advisory locks):

```go
locker := myapp.NewRedisLocker(redisClient)   // implements agent.Locker

session, _ := agent.NewSession(&agent.SessionOptions{
    ID:      sessionID,
    Client:  client,
    Tools:   registry,
    History: pghistory.NewHistory(db),         // implements agent.History
    Locker:  locker,
    Model:   "anthropic/claude-sonnet-4-6",
})
```

Two requests for the same `sessionID` build two distinct `*Session` values, but they hand the same string to the same `Locker` — concurrency control is preserved. v1.1 ships `agent/locker/redis` with an abstract `Client` interface so aikido does not import a Redis client library directly.

## Where to look next

| Doc | When to read it |
|-----|-----------------|
| [ROADMAP.md](ROADMAP.md) | What ships in v1, v2, v3. Explicit non-goals. |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Package map, data flow, agent loop algorithm. |
| [DECISIONS.md](DECISIONS.md) | ADRs explaining the load-bearing tradeoffs. |
| [PATTERNS.md](PATTERNS.md) | Cross-references to the DeepThought patterns aikido implements. |
| [EXAMPLES.md](EXAMPLES.md) | Three runnable examples: one-shot chat, agent over VFS, notes consolidation. |
| [SECURITY.md](SECURITY.md) | Sandbox principles, path validation, key handling. |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Dev setup, test strategy, PR conventions. |
| [v1/PLAN.md](v1/PLAN.md) | Detailed wave plan for v1 implementation. |
| [v1/API.md](v1/API.md) | Public API reference. |
| [v1/planning/INDEX.md](v1/planning/INDEX.md) | Implementation playbook: per-wave code skeletons, OpenRouter wire format, reference-code mining, test pyramid, 42-row risk register, and ~38 triaged open questions. |
| [v2/SCOPE.md](v2/SCOPE.md) | v2 outline: image, audio, queues, CLI, additional providers. |

## Status

**Pre-v0.1.** Library design locked, code waves not started. See [v1/PLAN.md](v1/PLAN.md) for the implementation schedule.
