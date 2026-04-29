# Examples

Two runnable examples covering the v1 surface. Each lives under `examples/` in the repo and compiles in CI. Code blocks below show the essential shape; see the corresponding `examples/<name>/main.go` for the full file once W2/W5/W6 land.

The third example originally planned (`examples/notes-consolidate/`) was deferred to v1.x with the rest of the `notes` package (ADR-015). The note-then-consolidate pattern is documented as a hand-rollable recipe in `PATTERNS.md`.

## 1. One-shot chat completion

**File:** `examples/chat-oneshot/main.go`

**What it shows:** the smallest possible aikido use case — build a client, build a request, drain a stream into a final result with `llm.Collect`.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/mxcd/aikido/llm"
    "github.com/mxcd/aikido/llm/openrouter"
)

func main() {
    client, err := openrouter.NewClient(&openrouter.Options{
        APIKey: os.Getenv("OPENROUTER_API_KEY"),
    })
    if err != nil {
        log.Fatal(err)
    }

    text, _, usage, err := llm.Collect(context.Background(), client, llm.Request{
        Model: "anthropic/claude-sonnet-4-6",
        Messages: []llm.Message{
            {Role: llm.RoleUser, Content: "Give me three facts about Go in one sentence each."},
        },
        MaxTokens:   512,
        Temperature: llm.Float32(0.3),
    })
    if err != nil {
        log.Fatal(err)
    }

    fmt.Println(text)
    fmt.Printf("\nTokens: in=%d out=%d cost=$%.4f\n",
        usage.PromptTokens, usage.CompletionTokens, usage.CostUSD)
}
```

**Run:**

```sh
export OPENROUTER_API_KEY=sk-or-...
go run ./examples/chat-oneshot
```

## 2. Agent over a memory VFS

**File:** `examples/agent-vfs/main.go`

**What it shows:** stand up a session with the bundled in-memory VFS, register the built-in VFS tools, run one turn, and print the streamed events. The agent reads/writes files in the VFS; nothing touches the real filesystem. `NewLocalSession` auto-supplies in-memory `History` and `Locker` for the single-replica case.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/mxcd/aikido/agent"
    "github.com/mxcd/aikido/llm/openrouter"
    "github.com/mxcd/aikido/tools"
    vfsmem "github.com/mxcd/aikido/vfs/memory"
)

func main() {
    client, err := openrouter.NewClient(&openrouter.Options{
        APIKey: os.Getenv("OPENROUTER_API_KEY"),
    })
    if err != nil {
        log.Fatal(err)
    }

    storage := vfsmem.NewStorage()
    registry := tools.NewRegistry()
    if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{
        Storage:         storage,
        HideHiddenPaths: true,
    }); err != nil {
        log.Fatal(err)
    }

    session, err := agent.NewLocalSession(&agent.SessionOptions{
        ID:           "demo-session",
        Client:       client,
        Tools:        registry,
        Model:        "anthropic/claude-sonnet-4-6",
        SystemPrompt: "You are a research assistant. Use the file tools to organize findings into markdown files.",
    })
    if err != nil {
        log.Fatal(err)
    }

    ctx := context.Background()
    events, err := session.Run(ctx, "Create research-notes.md with three short bullet points about goroutines, then read it back to confirm.")
    if err != nil {
        log.Fatal(err)
    }

    for ev := range events {
        switch ev.Kind {
        case agent.EventText:
            fmt.Print(ev.Text)
        case agent.EventToolCall:
            fmt.Printf("\n[tool] %s(%s)\n", ev.ToolCall.Name, ev.ToolCall.Arguments)
        case agent.EventToolResult:
            if ev.ToolResult.OK {
                fmt.Printf("[result] ok\n")
            } else {
                fmt.Printf("[result] error: %s\n", ev.ToolResult.Error)
            }
        case agent.EventEnd:
            fmt.Printf("\n[end: %s]\n", ev.EndReason)
        }
    }

    files, _ := storage.ListFiles(ctx)
    fmt.Printf("\nVFS now contains %d files:\n", len(files))
    for _, f := range files {
        fmt.Printf("  %s (%d bytes)\n", f.Path, f.Size)
    }
}
```

**Run:**

```sh
go run ./examples/agent-vfs
```

## Recipe — `RunWithMessages` + `Drain` for caller-managed history

When the caller maintains their own message log (trimming, branched conversations, agent-as-subroutine), they use `Session.RunWithMessages` and `agent.Drain`. The library prepends the system prompt from `SessionOptions.SystemPrompt`; the caller hands in their pruned `[]llm.Message` (excluding the system prompt) and gets back the produced messages for storing.

```go
// caller's own message store (trimmed however they want)
history := myStore.Load(ctx, sessionID)

events, _ := session.RunWithMessages(ctx, history)

newMessages, err := agent.Drain(events)
if err != nil {
    log.Fatal(err)
}

myStore.Append(ctx, sessionID, newMessages...)
```

For callers who also want streaming-to-UI, fan out the channel with a goroutine before passing one branch to `Drain`.

## Recipe — webapp shape with shared `Locker`

A typical webapp builds a fresh `*Session` per HTTP request. That works correctly when all per-request `*Session` values share one `Locker` (and one `History`):

```go
// app startup — once
locker := agent.NewLocalLocker()
history := historymem.NewHistory()   // or your persistent History impl

// per request
session, _ := agent.NewSession(&agent.SessionOptions{
    ID:      r.Header.Get("X-Session-Id"),
    Client:  client,
    Tools:   registry,
    History: history,
    Locker:  locker,
    Model:   "anthropic/claude-sonnet-4-6",
})
events, _ := session.Run(r.Context(), userMessage)
```

Two concurrent requests for the same session ID build two distinct `*Session` values but acquire the same lock string from the shared `Locker`. The agent serializes them automatically. For multi-replica services, swap `LocalLocker` for a Redis-backed `Locker` (caller-implemented today; v1.1 ships `agent/locker/redis`).

## What is intentionally not in `examples/`

- No HTTP server example. aikido is a library; HTTP wrapping is the caller's concern.
- No image-gen example. Image generation is v2.
- No Ent-backed VFS example. Wiring Ent to `vfs.Storage` is project-specific; documented in [v2/SCOPE.md](v2/SCOPE.md) when the reference impl lands.
- No notes-consolidation example. Pattern recipe lives in [PATTERNS.md](PATTERNS.md); first-class `notes` package deferred to v1.x (ADR-015).
