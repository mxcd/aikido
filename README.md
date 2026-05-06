# aikido

**AI KIt DOing things.** A Go library that gives applications a single, provider-agnostic interface for running AI inference — chat completion, tool calling, virtual file system, agent loops.

```bash
go get github.com/mxcd/aikido
```

Backed by OpenRouter in v1; designed so v2 can plug in direct providers (Anthropic, OpenAI, …) behind the same `llm.Client` interface.

## What you get in v1

- **`llm`** — provider-agnostic `Client`, streaming `Event` channel, `Request` shape with cache breakpoints, thinking config, and explicit `*float32` temperature.
- **`llm/openrouter`** — real SSE streaming, tool-call assembly across fragments, 429/5xx retry at stream-start, `ProviderOrder` routing.
- **`llm/llmtest`** — `StubClient` for scripting multi-turn conversations in your tests.
- **`tools`** — registry, dispatch, explicit JSON-Schema helpers.
- **`vfs`** — minimal four-method `Storage` interface plus optional `ScopedStorage` and `Searchable` capabilities. Bundled backends:
  - **`vfs/memory`** — read/write in-process.
  - **`vfs/embedfs`** — wraps any `fs.FS` (`embed.FS`, `fs.Sub`, `fstest.MapFS`); read-only.
- **`agent`** — `Session` with pluggable `History` and `Locker`, two-tier timeouts (`RunTimeout` + `LLMCallTimeout`), strict error policy, single end-of-turn variadic `History.Append`. Built-in VFS tools: `read_file`, `write_file`, `list_files`, `delete_file`, `search`. Toggle `ReadOnly: true` to expose only read tools.
- **`cmd/aikido`** — CLI built on `urfave/cli/v3` with `chat` and `agent` subcommands.

See [docs/v1/API.md](docs/v1/API.md) for the locked surface.

## Quickstart

### One-shot completion

```go
client, _ := openrouter.NewClient(&openrouter.Options{APIKey: os.Getenv("OPENROUTER_API_KEY")})

text, _, _, usage, _ := llm.Collect(ctx, client, llm.Request{
    Model:    "anthropic/claude-haiku-4.5",
    Messages: []llm.Message{{Role: llm.RoleUser, Content: "Give me one fun fact about Go."}},
})
fmt.Println(text)
fmt.Printf("cost=$%.6f\n", usage.CostUSD)
```

### Image generation

Image-capable OpenRouter models (e.g. `google/gemini-2.5-flash-image-preview`,
`openai/gpt-image-1`) surface generated images on the same `llm.Client`
streaming surface. Inline `data:` URIs are decoded to bytes; remote URLs
pass through verbatim. `Collect` returns them as the third positional value.

```go
_, _, images, _, _ := llm.Collect(ctx, client, llm.Request{
    Model:    "google/gemini-2.5-flash-image-preview",
    Messages: []llm.Message{{Role: llm.RoleUser, Content: "A pixel-art fox in tall grass."}},
})
for i, img := range images {
    if len(img.Data) > 0 {
        _ = os.WriteFile(fmt.Sprintf("out-%d.png", i), img.Data, 0o644)
    } else if img.URL != "" {
        fmt.Println("image at:", img.URL)
    }
}
```

When streaming, the same data flows as `llm.EventImage` events. In agent
runs, `Drain` populates `llm.Message.Images` on the assembled assistant
message, mirroring how `ToolCalls` are handled.

### Agent over a writable VFS

```go
storage := vfsmem.NewStorage()
registry := tools.NewRegistry()
_ = agent.RegisterVFSTools(registry, &agent.VFSToolOptions{Storage: storage})

session, _ := agent.NewLocalSession(&agent.SessionOptions{
    ID:           "demo",
    Client:       client,
    Tools:        registry,
    Model:        "anthropic/claude-sonnet-4.6",
    SystemPrompt: "You are a research assistant.",
})

events, _ := session.Run(ctx, "Make a notes file with three facts about Go.")
for ev := range events {
    if ev.Kind == agent.EventText {
        fmt.Print(ev.Text)
    }
}
```

`NewLocalSession` auto-supplies an in-memory `History` and a `LocalLocker`. For multi-replica deployments use `NewSession` with your own `Locker` (e.g. Redis) and persistent `History`.

### Chatbot over an embedded knowledge base

```go
//go:embed knowledge/*.md
var corpus embed.FS

knowledge, _ := fs.Sub(corpus, "knowledge")
storage  := embedfs.NewStorage(knowledge)
registry := tools.NewRegistry()
_ = agent.RegisterVFSTools(registry, &agent.VFSToolOptions{
    Storage:  storage,
    ReadOnly: true, // skip write_file + delete_file
})

session, _ := agent.NewLocalSession(&agent.SessionOptions{
    ID: "chatbot", Client: client, Tools: registry,
    Model: "anthropic/claude-haiku-4.5",
    SystemPrompt: "Answer from the corpus only. Cite the source file.",
})
```

The model can `search`, `list_files`, and `read_file` against the embedded markdown — but cannot mutate it. Defense in depth: `vfs/embedfs` would also reject mutations even if the tools were registered.

Four runnable examples under [`examples/`](examples/): `chat-oneshot`, `image-generation`, `agent-vfs`, `chatbot`.

## CLI

```bash
go install github.com/mxcd/aikido/cmd/aikido@latest
export OPENROUTER_API_KEY=sk-or-...   # or place it in .env in the cwd

aikido chat "Give me one fun fact about Go."
aikido agent "Create plan.md with one bullet, then list files."
```

`aikido --help` for the full flag list. Both subcommands accept `--model`, `--system`, `--max-tokens`, `--temperature`.

## Tested

- `go test ./...` — every package, every wave, including `-race`.
- `httptest.Server`-driven SSE replay for OpenRouter, six canned transcripts (simple text, single tool, interleaved multi-tool, 429-then-success retry, mid-stream error, thinking).
- Reusable conformance suites under `vfs.RunConformance` and `agent/history.RunConformance` so custom backends inherit coverage.

## Documentation

| Doc | Read when |
|-----|-----------|
| [docs/README.md](docs/README.md) | Library elevator pitch |
| [docs/ROADMAP.md](docs/ROADMAP.md) | What ships in v1, v2, v3 |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Package map, data flow, agent loop algorithm |
| [docs/DECISIONS.md](docs/DECISIONS.md) | Load-bearing ADRs |
| [docs/PATTERNS.md](docs/PATTERNS.md) | Cross-references to the patterns aikido implements |
| [docs/EXAMPLES.md](docs/EXAMPLES.md) | Runnable example walk-throughs |
| [docs/SECURITY.md](docs/SECURITY.md) | Sandbox principles, path validation, key handling |
| [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) | Dev setup, test strategy, PR conventions |
| [docs/v1/API.md](docs/v1/API.md) | Public API reference (locked) |
| [docs/v1/PLAN.md](docs/v1/PLAN.md) | Wave-by-wave implementation plan |
| [docs/v2/SCOPE.md](docs/v2/SCOPE.md) | v2 outline — image, audio, queues, additional providers |

## Status

**v0.1 implemented.** All v1 waves (W0–W6) plus the CLI and embedfs adapter are in `main` with full test coverage. Tagging awaits one more pass of integration smoke tests against the live provider.

## License

MIT — see [LICENSE](LICENSE).
