# aikido

**AI KIt DOing things.** A Go library that gives applications a single, provider-agnostic interface for running AI inference — chat completion, tool calling, virtual file system, agent loops.

```bash
go get github.com/mxcd/aikido
```

Backed by OpenRouter in v1; designed so v2 can plug in direct providers (Anthropic, OpenAI, …) behind the same `llm.Client` interface.

## What you get in v1

- **`llm`** — provider-agnostic `Client`, streaming `Event` channel, `Request` shape with cache breakpoints, thinking config, and explicit `*float32` temperature.
- **`llm/openrouter`** - real SSE streaming, tool-call assembly across fragments, 429/5xx retry at stream-start (3 attempts, `Options.MaxAttempts` to change, 1 = no retry), provider routing (`Options.Provider` / per call `WithProvider`: order, only, ignore, sort by price/throughput/latency, quantizations, max price), `Models` / `ModelEndpoints` (per-provider pricing, uptime, p50-p99 latency and throughput), and `GenerateImage` for the models OpenRouter serves only on `POST /api/v1/images`.
- **`llm/codex`** - the Codex backend of a ChatGPT subscription (bills the subscription, not an API key): device-code login, a `TokenSource` that refreshes and hands every rotated login back for persisting, streaming text, image input and structured output. No tools, no audio input.
- **`llm.Transcribe`** - speech-to-text over any client that accepts audio input (`llm.Message.Audio`, sent by `llm/openrouter` as `input_audio`), with `llm.AudioFormat` for browser MIME types and an `llm.Vocabulary` of expected terms that biases the prompt and corrects the transcript.
- **`llm.SpeechGenerator`** - text-to-speech, implemented by `llm/openrouter` on `POST /api/v1/audio/speech` (ElevenLabs, OpenAI, Gemini and other TTS models).
- **`voice`** - one agent turn with speech in and/or out: transcription, per-sentence speech synthesis while the model still writes, typed events, cost over all stages, and an NDJSON `net/http` handler.
- **`llm.Request.ResponseFormat`** - structured output (`llm.JSONSchema`), mapped by `llm/openrouter` and `llm/codex`.
- **`llm/llmtest`** — `StubClient` for scripting multi-turn conversations in your tests.
- **`tools`** — registry, dispatch, explicit JSON-Schema helpers.
- **`vfs`** — minimal four-method `Storage` interface plus optional `ScopedStorage` and `Searchable` capabilities. Bundled backends:
  - **`vfs/memory`** — read/write in-process.
  - **`vfs/embedfs`** — wraps any `fs.FS` (`embed.FS`, `fs.Sub`, `fstest.MapFS`); read-only.
  - **`vfs/osfs`** — a directory on the local disk, confined by `os.Root`; atomic writes.
- **`agent`** — `Session` with pluggable `History` and `Locker`, two-tier timeouts (`RunTimeout` + `LLMCallTimeout`), strict error policy, single end-of-turn variadic `History.Append`. Built-in VFS tools: `read_file` (line ranges), `write_file`, `edit_file` (exact string replace), `list_files`, `delete_file`, `grep` (RE2), `glob`, plus `search` when the storage is `Searchable`. Toggle `ReadOnly: true` to expose only read tools, `DisableExtended: true` to drop `grep`/`glob`/`edit_file`.
- **`cmd/aikido`** — CLI built on `urfave/cli/v3` with `chat`, `agent`, `image`, and `skill` subcommands.

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

`CostUSD` is the provider's own price for the call, not an estimate: every chat
request asks OpenRouter for usage accounting, so cost comes back on the
non-streaming response and on the final SSE chunk alike. A turn that ends in a
provider error or a content filter was billed too, and its usage is delivered
with the error rather than dropped (ADR-030).

Every call's `Usage` also carries `Provider` (who served it), `Latency` (call
to first token), `Duration` and `TokensPerSecond`, measured client-side
(ADR-034). Agent runs emit one `EventUsage` per model call; voice turns list
them in `Usage.LLMCalls` and on the NDJSON done line as `usage.llm_calls`.

```go
ctx = openrouter.WithProvider(ctx, &openrouter.ProviderPrefs{
    Only: []string{"google-vertex/europe"}, // or Sort: "throughput"
})
eps, _ := client.ModelEndpoints(ctx, "anthropic/claude-haiku-4.5") // eps.Endpoints[i].LatencyLast30m.P50
```

### Image generation

Image-capable OpenRouter models reach aikido through two paths. Models that
answer on chat completions (`google/gemini-3.1-flash-image-preview`,
`openai/gpt-5.4-image-2`) use `Complete` with `Modalities: []string{"image", "text"}` -
`Complete`, not `Collect`, because a base64 payload routinely exceeds the SSE
per-line cap. Inline `data:` URIs are decoded to bytes; remote URLs pass
through verbatim.

```go
resp, err := client.Complete(ctx, llm.Request{
    Model:      "google/gemini-3.1-flash-image-preview",
    Messages:   []llm.Message{{Role: llm.RoleUser, Content: "A pixel-art fox in tall grass."}},
    Modalities: []string{"image", "text"},
})
if err != nil {
    log.Fatal(err)
}
for i, img := range resp.Images {
    if len(img.Data) > 0 {
        _ = os.WriteFile(fmt.Sprintf("out-%d.png", i), img.Data, 0o644)
    } else if img.URL != "" {
        fmt.Println("image at:", img.URL)
    }
}
```

Models served only by OpenRouter's dedicated images endpoint
(`openai/gpt-image-2.5-flare` and friends, which never answer on chat
completions and are missing from a plain `GET /models` listing) go through the
optional `llm.ImageGenerator` capability instead:

```go
// client here is the concrete *openrouter.Client, which implements the
// capability, so call it directly.
resp, err := client.GenerateImage(ctx, llm.ImageRequest{
    Model:       "openai/gpt-image-2.5-flare",
    Prompt:      "A pixel-art fox in tall grass.",
    AspectRatio: "16:9",
    ImageSize:   "2K",
    Quality:     "high",
})
```

A frame the ratio list does not offer (OpenAI rejects `4:5`, and renders `3:4`
as 1024x1536) is requested as explicit pixels instead: `Size: "1024x1280"`,
alone, without `AspectRatio`/`ImageSize`. OpenAI wants both sides divisible by
16 and the longest edge at most 3840.

Code holding a plain `llm.Client` type-asserts first, since not every provider
has such an endpoint:

```go
func generate(ctx context.Context, c llm.Client, req llm.ImageRequest) (llm.ImageResponse, error) {
    gen, ok := c.(llm.ImageGenerator)
    if !ok {
        return llm.ImageResponse{}, errors.New("client has no images endpoint")
    }
    return gen.GenerateImage(ctx, req)
}
```

When streaming the chat path, the same data flows as `llm.EventImage` events.
In agent runs, `Drain` populates `llm.Message.Images` on the assembled
assistant message, mirroring how `ToolCalls` are handled.

### Speech-to-text

`llm.Message.Audio` carries audio clips; `llm/openrouter` sends them as
`input_audio` content parts on both `Stream` and `Complete`. `llm.Transcribe`
wraps that into one call: default model `google/gemini-3.1-flash-lite` (about
1 s per clip, verbatim German, English and Italian), temperature 0, and a
prompt that makes the model mark silence or noise instead of inventing a
sentence. That case returns `""`.

```go
format, err := llm.AudioFormat(r.Header.Get("Content-Type")) // "audio/webm;codecs=opus" -> "webm"
if err != nil {
    return err
}
text, usage, err := llm.Transcribe(ctx, client, llm.TranscribeRequest{
    Audio:  clip,
    Format: format,
    Hint:   "The speaker most likely speaks German. Write amounts as digits.",
})
```

Keep example sentences and amounts out of `Hint`: for a silent clip the model
repeats them as the transcript. `llm/codex` rejects audio with
`llm.ErrInvalidRequest`.

`TranscribeRequest.Vocabulary` fixes the words a speech model gets wrong:
brand names, article numbers, trade words. `Terms` go into the prompt as
"spell exactly like this" (capped at 200 terms / 3000 bytes), then the
transcript is corrected deterministically: `Replacements` (heard variant to
canonical term, case-insensitive, whole word, longest first), exact term
matches with spaces and case ignored, and with `Fuzzy` a conservative
edit-distance match that never touches words under 4 letters.
`TranscribeDetailed` returns the raw and the corrected text.

```go
vocab := &llm.Vocabulary{
    Terms:        []string{"PFEIFFER & MAY", "LIV60G", "Waschtisch", "Duschtasse"},
    Replacements: map[string]string{"pfeifer und mai": "PFEIFFER & MAY"},
    Fuzzy:        true,
}
t, err := llm.TranscribeDetailed(ctx, client, llm.TranscribeRequest{Audio: clip, Format: format, Vocabulary: vocab})
// t.Raw  "Ist die Duschtase Liv 60 G von Pfeifer und Mai lieferbar?"
// t.Text "Ist die Duschtasse LIV60G von PFEIFFER & MAY lieferbar?"
```

A reply that is only a short bracketed phrase counts as no speech, so a
German hint answering `[keine Sprache]` returns `""` as well.

### Text-to-speech

`llm/openrouter` implements the optional `llm.SpeechGenerator` capability on
`POST /api/v1/audio/speech`. The response is the raw audio in one piece
(`mp3` as `audio/mpeg`, or the default `pcm`, 16-bit little-endian mono);
OpenRouter does not stream chunks of it, so speak sentence by sentence for
early playback (the `voice` package does).

```go
resp, err := client.Speak(ctx, llm.SpeechRequest{
    Model:  "elevenlabs/eleven-v4-turbo",
    Input:  "Die Dusche kommt an die Fensterwand.",
    Voice:  "sarah", // george, sarah, adam, alice, bella, brian, daniel, laura, ...
    Format: "mp3",
})
// resp.Audio, resp.ContentType ("audio/mpeg"), resp.GenerationID
```

OpenRouter reports no usage on speech responses, so `SpeechResponse` has no
`Usage`; the price is per input character (eleven-v4-turbo: 0.00002 USD,
about 0.002 USD for a two-sentence answer) and shows up on
`GET /api/v1/generation?id=<GenerationID>` some seconds later.

### Voice turns

`voice.Pipeline` wraps one agent turn with speech: text or audio in, text or
text plus speech out, all four combinations through one `Run`.

```go
pipeline, _ := voice.New(&voice.Options{
    Transcriber:      client,           // any llm.Client for llm.Transcribe
    Vocabulary:       vocab,
    Speech:           client,           // any llm.SpeechGenerator
    Voice:            "sarah",          // model defaults to elevenlabs/eleven-v4-turbo, mp3
    SpeechUSDPerChar: 0.00002,
    SpokenStyle:      voice.DefaultSpokenStyle, // short plain answers for speech turns
})
events, err := pipeline.Run(ctx, session, voice.Input{
    Audio: clip, ContentType: "audio/webm;codecs=opus", Language: "de", Output: voice.OutputSpeech,
})
for ev := range events {
    switch ev.Kind {
    case voice.EventTranscript: // ev.Text (corrected), ev.Raw
    case voice.EventText:       // reply delta
    case voice.EventSpeech:     // ev.Speech.Seq, .Audio, .ContentType, in Seq order
    case voice.EventError:      // ev.Err; with ev.Speech set only that chunk failed
    case voice.EventDone:       // ev.EndReason, ev.Usage.CostUSD over STT + LLM + TTS
    }
}
```

Speech is synthesized per sentence as soon as one is complete, in parallel
(`SpeechConcurrency`, default 2), and emitted in order, so playback starts
while the model is still writing. `voice.Speakable` strips markdown, code and
URLs before synthesis. `voice.NewHandler` serves the pipeline over plain
`net/http` as NDJSON (one JSON event per line, audio base64), accepting JSON,
multipart or a raw audio body, and runs the turn under
`context.WithoutCancel` so a closed tab does not lose it. Under Gin:
`r.POST("/voice", gin.WrapH(handler))`.

Every `Run` ends with exactly one `EventDone` carrying the usage so far, also
when `ctx` is cancelled or past its deadline (`EndReason` `cancelled` or
`timeout`), so a timed-out turn still books its cost; a consumer that stopped
reading does not block it forever. `HandlerOptions.ToolResult` forwards chosen
tool result content as `content` on `tool_result` lines, and the `Session`
factory can decode extra JSON request fields from `r.Body` again.

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
    ReadOnly: true, // skip write_file, edit_file, delete_file
})

session, _ := agent.NewLocalSession(&agent.SessionOptions{
    ID: "chatbot", Client: client, Tools: registry,
    Model: "anthropic/claude-haiku-4.5",
    SystemPrompt: "Answer from the corpus only. Cite the source file.",
})
```

The model can `search`, `grep`, `glob`, `list_files`, and `read_file` against the embedded markdown — but cannot mutate it. Defense in depth: `vfs/embedfs` would also reject mutations even if the tools were registered.

### ChatGPT subscription (`llm/codex`)

```go
dc, _ := codex.StartDeviceLogin(ctx, nil)       // show dc.VerificationURL + dc.UserCode
login, _ := codex.PollDeviceLogin(ctx, nil, dc) // blocks until confirmed (15 min max)

tokens := codex.NewTokenSource(login, func(ctx context.Context, t codex.Tokens) error {
    return store.Save(ctx, t) // the refresh token rotates and is single-use: persist every refresh
}, nil)
client, _ := codex.NewClient(&codex.Options{Tokens: tokens, Originator: "my-app"})
res, _ := client.Complete(ctx, llm.Request{Model: "gpt-5.5", Messages: msgs, ResponseFormat: schema})
```

Keep one `TokenSource` per login and process, and do not share a login with the Codex CLI (`codex.ParseAuthJSON` reads its `auth.json`): a refresh token used twice revokes the login, surfacing as `codex.ErrReauthRequired`.

Five runnable examples under [`examples/`](examples/): `chat-oneshot`, `image-generation`, `agent-vfs`, `chatbot`, `voicebot` (push-to-talk web page over `voice.Handler`).

## CLI

```bash
go install github.com/mxcd/aikido/cmd/aikido@latest
export OPENROUTER_API_KEY=sk-or-...   # or place it in .env in the cwd

aikido chat "Give me one fun fact about Go."
aikido agent "Create plan.md with one bullet, then list files."
aikido image --aspect 16:9 --size 2K "A pixel-art fox in tall grass."
aikido image -m google/gemini-3.1-flash-image-preview "A quick draft."
aikido image --ref logo.png --quality high "The same logo on a matte black mug."
```

`aikido --help` for the full flag list. `chat` and `agent` accept `--model`, `--system`, `--max-tokens`, `--temperature`.

`image` defaults to `openai/gpt-image-2.5-flare`, which OpenRouter serves only
through `POST /api/v1/images`; `OPENROUTER_IMAGE_MODEL` or `-m` overrides it.
Which ids take that endpoint is the `--images-api-models` list
(`OPENROUTER_IMAGES_API_MODELS`, comma-separated): a value replaces the
built-in list, and something non-matching like `none` disables the routing.
The Gemini image models are deliberately off the list and keep the
chat-completions path. `--ref` attaches reference images on both paths (repeat
it for several); `--quality` is images-endpoint only.

On images-endpoint models `--aspect` is sent as an exact pixel frame (`size`),
not as `aspect_ratio`, so every `W:H` up to 3:1 comes out exact: `4:5` is
1024x1280 and `9:16` is 1024x1824 at 1K; `--size 2K`/`4K` raise the short edge
to 2048/4096 within the 3840 px edge and 8.3 MP caps. A frame the endpoint
cannot render (wider than 3:1, or `--size 0.5K`) switches to
`google/gemini-3.1-flash-image-preview` with a notice on stderr, unless the
model was chosen explicitly (`-m` or `OPENROUTER_IMAGE_MODEL`); then it errors
and names that model.

`aikido --version` reports the module version the binary was built from:
`go install github.com/mxcd/aikido/cmd/aikido@v0.6.1` prints `v0.6.1`, a build
from a checkout prints its VCS pseudo-version or `(devel)` plus the revision.

### Claude Code skill

`aikido` ships a Claude Code skill that teaches an agent to drive this CLI —
chiefly for high-quality image generation. Install it once:

```bash
aikido skill install              # user-global → ~/.claude/skills/aikido/SKILL.md
aikido skill install --scope project   # repo-local → ./.claude/skills/aikido/SKILL.md
aikido skill install --print      # print the skill to stdout instead of writing
```

The skill is bundled in the binary (`//go:embed`), so any installed `aikido`
can deploy it. After installing, Claude triggers it automatically on
image-generation requests, or you can invoke it explicitly with `/aikido`.

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
