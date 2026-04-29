# Test Strategy and Risk Register

> ⚠️ **Divergence banner (30.04.2026).** This file reflects the *pre-second-grilling* surface in places. Authoritative test scenarios for the post-grilling shapes (Locker concurrency, strict History/Locker error policy, variadic Append flush-once, two-timeout split, `Drain` helper) are summarized in `../PLAN.md`'s W5 deliverables. Use this file for general test-strategy depth (test pyramid, fixture catalog, risk register), but expect named tests in §"per-wave names" to be off by one wave-number from W2 onward, and to reference dropped surfaces (`Tx`, `Snapshot`, `SchemaFromType`, `Notebook.Consolidate`, `Catalog`, `CacheHint`, `ThinkingEffort`, `iota` `EventKind`, value-type `Temperature`, per-Session struct mutex). The 42-row risk register cross-references remain useful as a checklist; some rows became moot.

The implementer-facing companion to [PLAN.md](../PLAN.md), [SECURITY.md](../../SECURITY.md), and [ARCHITECTURE.md](../../ARCHITECTURE.md). This document tells you which test to write next, what fixture to put under `testdata/`, what `just check` runs, and where the design has known soft spots that demand explicit assertions.

Companion documents own complementary concerns:

- [`OPENROUTER-DETAILS.md`](OPENROUTER-DETAILS.md) — exact wire-format payloads (this doc references fixtures by purpose only).
- [`AGENT-LOOP-DETAILS.md`](AGENT-LOOP-DETAILS.md) — pseudocode of `runLoop`; this doc references it by behavior.
- [`API.md`](../API.md) — public surface; every test name in this doc maps to a function defined there.

If you are about to write a test, search this file for the wave letter (`W3`, `W6`, ...) and the package name. If your test name is not here, either (a) the strategy missed it — open a PR to extend this file; or (b) you may be testing the wrong thing — re-read the wave plan.

---

## Test pyramid for aikido v1

aikido is a network-touching library running untrusted model output. The pyramid skews away from end-to-end and toward layered, scripted, deterministic tests. CI never hits the network.

| Layer | What it tests | Per-package presence | Example |
|-------|---------------|----------------------|---------|
| **Unit** | Pure logic with no I/O. Exercises one function or one method. | Every package. | `tools/registry_test.go`: `TestRegistry_Register_DuplicateRejected` registers a tool twice and asserts `errors.Is(err, ErrDuplicateTool)`. |
| **Conformance** | One reusable suite that any `Storage` implementation must pass. Imported by `vfs/memory_test.go` and by user backends. | `vfs/`, plus any future capability suite. | `vfs.RunConformance(t, func() vfs.Storage { return memvfs.NewStorage() })`. |
| **Transcript replay** | Provider wire format. Parser correctness against canned SSE / non-stream JSON. | `llm/openrouter/`. Any future provider gets its own `testdata/`. | `httptest.NewServer` writes the bytes of `simple_text.sse` to the response stream; the client returns events; the test asserts the event sequence. |
| **Stub-driven integration** | `agent`, `notes`, and the built-in tools end-to-end without provider or network. The model is scripted; the storage is in-memory; the registry is real. | `agent/`, `notes/`, `agent/tools_builtin*`. | `internal/testutil.NewStubClient(turns ...[]llm.Event)` returns one scripted turn per `Stream` call. |
| **Build-verify** | Every example compiles. Catches public-API regressions early. | `examples/` driven by `go build ./examples/...` in `just check`. | If `agent.Options` field is renamed and `examples/agent-vfs/main.go` references the old name, CI fails before the test pass. |
| **Smoke (manual)** | Sanity check before tagging. Real network. Real OpenRouter. Real cost. | Not in CI. Triggered explicitly by `just smoke`. | `OPENROUTER_API_KEY=sk-or-... go run ./examples/chat-oneshot` returns text and prints non-zero token usage. |

**Layer cost ranking (cheap → expensive):** Unit < Conformance < Transcript < Stub-integration < Build-verify (compile time) < Smoke (real money).

**When to add a test at each layer:**

- **Unit** — every public function should have one happy path test and one error test. Internal helpers earn unit tests when behavior surfaces a non-trivial branch (path validation, model-id normalization, schema rendering).
- **Conformance** — only when a new capability interface is introduced. Do **not** add to the conformance suite for backend-specific behavior; that lives in the backend's own test file.
- **Transcript** — when adding support for a new provider event shape (new field, new `[DONE]` shape, new error envelope). Recording a real SSE response and stripping it down is the canonical workflow; do not synthesize transcripts from the spec alone.
- **Stub-integration** — for any change to the agent loop, registry dispatch, or tool wiring. New tools get one stub-integration round-trip test.
- **Build-verify** — automatic; never write one by hand. Just keep `examples/` compiling.
- **Smoke** — release-only. Never depend on smoke tests for correctness signal during development.

**What NOT to test at each layer:**

- **Unit** — don't test `time.Now()`-driven behavior without injecting a clock; don't test goroutine scheduling.
- **Conformance** — don't test memory-backend specifics (e.g., that `sync.RWMutex` is held). The suite tests the contract, not the impl.
- **Transcript** — don't test the agent loop. Transcripts are wire-format only. The agent has stub-integration tests for everything end-to-end.
- **Stub-integration** — don't test SSE parsing or HTTP retries. Use the real `llm/openrouter` only when explicitly testing provider integration; otherwise the stub.
- **Smoke** — don't try to assert specific text from the model. Assert event-shape only (event kinds, that `Usage.PromptTokens > 0`).

---

## Required fixtures

Every test fixture in the project, by directory. File names are normative — name your fixtures these exact names.

### `llm/openrouter/testdata/`

| File | Purpose | First-line shape (full content in `OPENROUTER-DETAILS.md`) |
|------|---------|------------------------------------------------------------|
| `simple_text.sse` | Plain completion: 3 text-delta events plus a final `[DONE]`. The minimum-viable transcript that exercises `EventTextDelta` accumulation. | `data: {"id":"gen-...","choices":[{"delta":{"content":"Hello"},"index":0}]}` |
| `single_tool.sse` | Exactly one tool call. Arguments arrive in two fragments at indices `0`. `finish_reason: "tool_calls"` on the final choice frame. Verifies `assembleToolCalls` emits one event with the full reassembled JSON. | `data: {"id":"gen-...","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}` |
| `multi_tool_interleaved.sse` | Two tool calls, fragments interleaved by index (frame N has index 0 args, frame N+1 has index 1 args, frame N+2 has more index 0 args). Verifies the index-keyed buffer keeps them apart. | (same prefix; two `tool_calls` entries with `index:0` and `index:1`). |
| `mid_stream_error.sse` | Two text-delta frames, then a frame with `error: {"message":"...","type":"server_error"}`. No `[DONE]`. Verifies the client emits `EventError` then `EventEnd("error")` and closes the channel. | `data: {"choices":[{"delta":{"content":"par"}}]}` ... `data: {"error":{"message":"upstream timeout","type":"server_error"}}` |
| `usage_only_in_final.sse` | Two text-delta frames, then a final frame containing only `usage: {prompt_tokens:..., completion_tokens:..., total_cost:...}` plus `finish_reason:"stop"`, then `[DONE]`. Verifies `EventUsage` is emitted before `EventEnd`. | (closes with `data: {"choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":42,"completion_tokens":17}}` then `data: [DONE]`) |
| `cache_control_passthrough.sse` | Response with `usage.cache_creation_input_tokens` and `usage.cache_read_input_tokens` populated. Verifies `Usage.CacheReadTokens` and `Usage.CacheWriteTokens` map correctly. | (terminal frame includes both fields). |
| `429_then_success.json` | **Not SSE.** A pair of HTTP-layer fixtures: response 1 is `HTTP/1.1 429 Too Many Requests` with `Retry-After: 1` and a JSON error body; response 2 is the body of `simple_text.sse`. The test fixture loader picks file by attempt count. | (HTTP fixture format: status code, headers, body, separated by blank lines.) |
| `5xx_then_success.json` | Same shape as 429 but with `HTTP/1.1 503 Service Unavailable`. Verifies 5xx retry uses exponential backoff (no `Retry-After`). | (HTTP fixture format.) |
| `auth_failure.json` | `HTTP/1.1 401 Unauthorized` JSON. Single response, never retried. Verifies `ErrAuth` is wrapped. | `HTTP/1.1 401 Unauthorized\n\n{"error":{"message":"invalid API key","code":"401"}}` |
| `bad_request.json` | `HTTP/1.1 400 Bad Request`. Verifies `ErrInvalidRequest` is returned (not retried). | `HTTP/1.1 400 Bad Request\n\n{"error":{"message":"model not found","code":"400"}}` |
| `nonstream_simple.json` | One non-streaming chat-completion JSON body. Used by `Stream` when the W2 path is exercised under test. | `{"id":"gen-...","choices":[{"message":{"content":"Hi","role":"assistant"},"finish_reason":"stop"}],"usage":{...}}` |
| `nonstream_with_tool.json` | Non-streaming response with one tool call in `choices[0].message.tool_calls`. | (full message object with `tool_calls` array.) |
| `model_id_normalization.json` | Captures one request body to verify `normalizeModelID` ran before send. Used as a request-side fixture (the test extracts the body sent to `httptest.Server` and asserts model is `"anthropic/claude-sonnet-4-6"` not `4.6`). | (request body capture, not response.) |

**Total `llm/openrouter/testdata/` fixtures:** 13.

### `internal/testutil/`

No file fixtures. The `StubClient` is a Go type, not data.

**`StubClient` design:**

```go
package testutil

import (
    "context"
    "errors"
    "sync"
    "github.com/mxcd/aikido/llm"
)

// StubClient is a scriptable llm.Client. Each Stream call dequeues one turn
// from the script. Tests push a sequence of turns; the agent consumes them.
type StubClient struct {
    mu     sync.Mutex
    turns  [][]llm.Event   // FIFO; one slice = one Stream call
    calls  int             // number of Stream calls observed
    seen   []llm.Request   // requests received, in order
}

func NewStubClient(turns ...[]llm.Event) *StubClient

// PushTurn enqueues another scripted turn at the tail.
func (s *StubClient) PushTurn(events []llm.Event)

// Calls returns the number of Stream calls observed so far.
func (s *StubClient) Calls() int

// Requests returns a copy of all Requests received, in call order.
func (s *StubClient) Requests() []llm.Request

// Stream pops the next scripted turn and emits it on the returned channel.
// If the script is exhausted, returns ErrStubExhausted.
func (s *StubClient) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error)

var ErrStubExhausted = errors.New("stub: no more scripted turns")

// Helpers for building common turn shapes:
func TextOnly(text string) []llm.Event              // EventTextDelta + EventEnd
func TextThenToolCall(text, name, args string) []llm.Event
func ErrorTurn(err error) []llm.Event               // EventError + EventEnd("error")
func TimeoutTurn() []llm.Event                      // never emits — use to test ctx cancel
```

A turn may contain any number of events; it always ends with the channel closing on the producer side. The agent should be unable to tell the stub from a real `*openrouter.Client`.

### Per-wave fixture map

Which packages need which fixtures, when.

| Wave | Fixtures needed | Owner package |
|------|-----------------|---------------|
| W0 | `.golangci.yml`, `justfile`, `.github/workflows/ci.yml` | repo root |
| W1 | none — pure types | `llm/` |
| W2 | `nonstream_simple.json`, `nonstream_with_tool.json`, `auth_failure.json`, `bad_request.json`, `model_id_normalization.json` | `llm/openrouter/testdata/` |
| W3 | `simple_text.sse`, `single_tool.sse`, `multi_tool_interleaved.sse`, `mid_stream_error.sse`, `usage_only_in_final.sse`, `cache_control_passthrough.sse`, `429_then_success.json`, `5xx_then_success.json` | `llm/openrouter/testdata/` |
| W4 | none — registry tests use inline tool defs | `tools/` |
| W5 | none — conformance suite is in-process | `vfs/`, `vfs/memory/` |
| W6 | `internal/testutil.StubClient` lands here | `agent/`, `internal/testutil/` |
| W7 | none — uses memory storage and stub client | `agent/` (built-ins) |
| W8 | none — uses memory storage and stub client; consolidation prompt may live as a const in `notes/prompts.go` | `notes/` |

**Total fixtures across project:** 13 OpenRouter testdata files. Plus the `StubClient` Go type. Plus three CI/tooling configs (`ci.yml`, `golangci.yml`, `justfile`). No other on-disk fixtures.

---

## Test scenarios per wave

This section is the **test naming contract**. When you implement a wave, your `_test.go` files should contain functions with these exact names. If you find you need a test that is not listed, either it indicates a missing scenario (extend this doc in the same PR) or you are over-testing (justify in the PR).

Convention: `TestSubject_Behavior` for package-internal subjects. `TestPackage_Subject_Behavior` only when needed for disambiguation across files. Subtests use `t.Run("descriptor", ...)` with the descriptor matching the table-row name.

### W0 — Skeleton

No Go tests in W0. The smoke check is "`just check` exits 0 on a fresh clone" and is verified by CI.

### W1 — `llm` types

Package: `github.com/mxcd/aikido/llm`. File: `llm/llm_test.go` (single file fine for W1; split when it crosses ~400 lines).

| Test | Tests that... |
|------|---------------|
| `TestCollect_TextOnly` | feeding a stub channel with three `EventTextDelta` events and one `EventEnd` returns the concatenated text and no error. |
| `TestCollect_ToolCalls` | a turn with one `EventTextDelta` + one `EventToolCall` + `EventEnd` returns `(text, []ToolCall{call}, nil, nil)`. |
| `TestCollect_PropagatesError` | an `EventError` mid-stream is returned as the function's error. |
| `TestCollect_Usage` | an `EventUsage` event populates the returned `*Usage` pointer with non-nil token counts. |
| `TestCollect_ContextCancelled` | cancelling `ctx` while the stub has unsent events returns `context.Canceled`. |
| `TestCollect_StreamError` | the underlying `Client.Stream` returning a non-nil error returns `(zero, nil, nil, err)`. |
| `TestCatalog_HasSeedEntries` | the seed catalog contains at least one Anthropic, one OpenAI, and one Mistral entry. |
| `TestFindModel_KnownID` | `FindModel("anthropic/claude-sonnet-4-6")` returns a populated `Model` with no error. |
| `TestFindModel_DotsToHyphens` | `FindModel("anthropic/claude-sonnet-4.6")` returns the same model as the dash form (normalization happens). |
| `TestFindModel_UnknownID` | `FindModel("nonexistent/model")` returns `ErrUnknownModel`. |
| `TestErrors_AreSentinels` | `errors.Is(fmt.Errorf("wrap: %w", llm.ErrAuth), llm.ErrAuth)` is true; same for the other four typed errors. |
| `TestEventKind_String` | (only if `String()` is added) every `EventKind` constant returns a descriptive string. Skip if not implemented. |

**12 tests in W1.**

### W2 — `llm/openrouter` non-streaming via `Stream`

Package: `github.com/mxcd/aikido/llm/openrouter`. Files: `client_test.go`, `modelid_test.go`.

| Test | Tests that... |
|------|---------------|
| `TestNewClient_RequiresAPIKey` | `NewClient(&Options{})` returns a non-nil error mentioning `APIKey`. |
| `TestNewClient_DefaultsApplied` | `NewClient(&Options{APIKey: "x"})` populates `BaseURL` to `https://openrouter.ai/api/v1` and `HTTPClient` to non-nil. |
| `TestNewClient_OptionsOverride` | callers can override `BaseURL` and `HTTPClient` via the constructor. |
| `TestStream_NonStream_TextOnly` | feeding `nonstream_simple.json` returns a channel that emits one `EventTextDelta` (full content), one `EventUsage`, one `EventEnd("stop")`, then closes. |
| `TestStream_NonStream_ToolCall` | feeding `nonstream_with_tool.json` returns one `EventTextDelta`, one `EventToolCall` with full JSON, one `EventEnd("stop")`. |
| `TestStream_NonStream_AuthFailure` | `auth_failure.json` returns an error via `EventError` wrapping `ErrAuth`. |
| `TestStream_NonStream_BadRequest` | `bad_request.json` returns an error wrapping `ErrInvalidRequest` and is **not** retried. |
| `TestStream_RequestBodyShape` | the test server captures the request body; assertions: `Authorization: Bearer <key>` header set, body contains `model`, `messages`, `tools` only when non-nil, `stream: false` for W2 (or `true` for W3 — adjust per wave). |
| `TestNormalizeModelID_DotsToHyphens` | table-driven: `anthropic/claude-sonnet-4.6` → `anthropic/claude-sonnet-4-6`; `openai/gpt-4.1` → `openai/gpt-4-1`; already-hyphenated unchanged; non-model strings (no `/`) unchanged or untouched per spec. |
| `TestNormalizeModelID_RequestBodyApplies` | `Stream` with model `"anthropic/claude-sonnet-4.6"` sends body with `"model":"anthropic/claude-sonnet-4-6"` (read from `model_id_normalization.json`). |
| `TestStream_HTTPRefererPropagated` | `Options.HTTPReferer` and `Options.XTitle` set headers `HTTP-Referer` and `X-Title`. |

**11 tests in W2.**

### W3 — `llm/openrouter` streaming + tool-call assembly + retry

Package: `github.com/mxcd/aikido/llm/openrouter`. Plus `github.com/mxcd/aikido/internal/sseparse`.

| Test | Tests that... |
|------|---------------|
| `TestSSEParse_LineByLine` | parser fed `data: {...}\n\ndata: [DONE]\n\n` emits two frames in order. |
| `TestSSEParse_IgnoresComments` | lines starting with `:` are ignored. |
| `TestSSEParse_HandlesCRLF` | `\r\n` line endings are accepted (some HTTP intermediaries rewrite). |
| `TestSSEParse_PartialReads` | parser called with chunks split mid-line still produces correct frames once the trailing `\n\n` arrives. |
| `TestSSEParse_DoneTerminatesStream` | `[DONE]` frame causes parser to signal end of stream. |
| `TestStream_SimpleText` | `simple_text.sse` produces three `EventTextDelta` (in order), one `EventUsage`, one `EventEnd("stop")`, then channel closes. |
| `TestStream_SingleToolCall` | `single_tool.sse` reassembles fragmented arguments and emits **exactly one** `EventToolCall` with valid JSON. |
| `TestStream_MultiToolInterleaved` | `multi_tool_interleaved.sse` produces two `EventToolCall` events, each with valid JSON for the right tool, in `index` order. |
| `TestStream_MidStreamError` | `mid_stream_error.sse` produces some `EventTextDelta`, then `EventError`, then `EventEnd("error")`; channel closes; no further events. |
| `TestStream_UsageEmittedBeforeEnd` | `usage_only_in_final.sse` always emits `EventUsage` before `EventEnd`; the order is asserted explicitly (not by index). |
| `TestStream_CacheControlTokens` | `cache_control_passthrough.sse` produces `Usage.CacheReadTokens > 0` and `Usage.CacheWriteTokens > 0`. |
| `TestStream_ContextCancelled_ChannelCloses` | with a long-running stub, cancelling `ctx` causes the producer goroutine to return within 100ms and the channel to close; no goroutine leak (use `goleak.VerifyNone(t)`). |
| `TestStream_RetryOn429` | `429_then_success.json`: client receives 429, sleeps for `Retry-After`, retries, succeeds; final stream emits the success frames. |
| `TestStream_RetryOn5xx` | `5xx_then_success.json`: client receives 503 with no `Retry-After`, applies exponential backoff per default policy, retries, succeeds. |
| `TestStream_RetryExhausted` | server always returns 503; client retries `MaxAttempts` times, then returns wrapped `ErrServerError`. |
| `TestStream_NoRetryOn400` | server returns 400 once; client returns `ErrInvalidRequest` immediately without retry. |
| `TestStream_NoRetryOn401` | server returns 401 once; client returns `ErrAuth` immediately without retry. |
| `TestStream_NoRetryAfterStreamStarted` | server starts streaming, then errors mid-stream; client emits `EventError` and does not retry (would replay tokens). |
| `TestAssembleToolCalls_FragmentOrder` | unit test: feed fragments `[idx=0,args="{\"a\""], [idx=1, args="{\"b\""], [idx=0, args="\":1}"], [idx=1, args="\":2}"]; assert two complete calls with valid JSON. |
| `TestAssembleToolCalls_ChoiceDoneFlushes` | partial fragment stays in buffer until the `finish_reason: "tool_calls"` frame arrives; only then emitted. |
| `TestAssembleToolCalls_DropsIncompleteOnError` | mid-stream error before the choice-done frame produces no `EventToolCall` and the partial buffer is dropped. |

**21 tests in W3** (plus 5 sseparse subset).

### W4 — `tools` registry

Package: `github.com/mxcd/aikido/tools`. Files: `registry_test.go`, `schema_test.go`, `schema_reflect_test.go`.

| Test | Tests that... |
|------|---------------|
| `TestRegistry_NewIsEmpty` | a fresh registry has zero `Defs()` and `Has("anything") == false`. |
| `TestRegistry_RegisterAndDispatch` | register one tool, dispatch with valid args, handler is called with parsed args and the env, returns `Result`. |
| `TestRegistry_RegisterDuplicate` | registering twice with the same name returns `ErrDuplicateTool`. |
| `TestRegistry_DispatchUnknown` | dispatching a `ToolCall` whose name is not registered returns `ErrUnknownTool`. |
| `TestRegistry_Defs_PreservesOrder` | `Defs()` returns tools in registration order. |
| `TestRegistry_Has` | `Has("registered")` is true; `Has("nope")` is false. |
| `TestRegistry_DispatchInvalidJSON` | dispatching with malformed JSON args returns an error wrapping `json.SyntaxError` (handler is **not** called). |
| `TestRegistry_DispatchPropagatesHandlerError` | handler returns an error; `Dispatch` returns that exact error (`errors.Is` holds). |
| `TestRegistry_DispatchPropagatesContext` | context with a value is passed through to the handler. |
| `TestRegistry_DispatchInjectsEnv` | the `Env` passed to the handler equals the `Env` passed to `Dispatch` (project, turn ID, storage all match). |
| `TestSchema_Object_Required` | `Object(map[string]any{"x":String("d")}, "x")` produces `{"type":"object","properties":{"x":{"type":"string","description":"d"}},"required":["x"]}`. |
| `TestSchema_Object_NoRequired` | `Object(map[string]any{"x":String("d")})` omits the `"required"` key. |
| `TestSchema_String` | `String("description")` returns `{"type":"string","description":"description"}`. |
| `TestSchema_Integer_Number_Boolean` | each helper produces the corresponding JSON Schema type. |
| `TestSchema_Enum` | `Enum("d", "a", "b")` produces `{"type":"string","description":"d","enum":["a","b"]}`. |
| `TestSchema_Array` | `Array(String("d"), "items of x")` produces `{"type":"array","items":{...},"description":"items of x"}`. |
| `TestSchema_NestedObject` | `Object` nested inside another `Object` round-trips correctly. |
| `TestSchemaFromType_FlatStruct` | a struct with `json:"foo"` tags produces a schema with `properties.foo`. |
| `TestSchemaFromType_RequiredViaTag` | `json:"foo,omitempty"` is non-required; `json:"foo"` is required. |
| `TestSchemaFromType_EmptyStruct` | `SchemaFromType[struct{}]()` returns `{"type":"object","properties":{}}` (no panic). |
| `TestSchemaFromType_UnsupportedTypePanics` | (or returns error — pick one in impl): `SchemaFromType[chan int]()` produces a clear error. |

**21 tests in W4.**

### W5 — `vfs` interface + memory impl + conformance suite

Package: `github.com/mxcd/aikido/vfs` (suite + path validation), `github.com/mxcd/aikido/vfs/memory` (impl + run conformance).

The conformance suite is the bulk of W5. It is in `vfs/conformance.go` (not `_test.go`) and is invoked from `vfs/memory/memory_test.go` via `vfs.RunConformance(t, factory)`. Subtest names below are the names that show up in `go test -v` output.

#### `vfs/path_test.go` — path validation unit tests

| Test | Tests that... |
|------|---------------|
| `TestValidatePath_Empty` | empty string returns `ErrPathInvalid`. |
| `TestValidatePath_Absolute` | `/foo` returns `ErrPathInvalid`. |
| `TestValidatePath_DotDotPrefix` | `../foo` returns `ErrPathInvalid`. |
| `TestValidatePath_DotDotMiddle` | `foo/../bar` returns `ErrPathInvalid`. |
| `TestValidatePath_DotDotEnd` | `foo/..` returns `ErrPathInvalid`. |
| `TestValidatePath_NullByte` | `foo\x00bar` returns `ErrPathInvalid`. |
| `TestValidatePath_TooLong` | a 513-byte path returns `ErrPathInvalid`; a 512-byte path is accepted. |
| `TestValidatePath_BackslashTraversal` | `foo\..\bar` is rejected (Windows-style traversal). |
| `TestValidatePath_DoubleSlash` | `foo//bar` is rejected (or normalized — choose one explicitly in impl). |
| `TestValidatePath_LeadingSlash` | `/foo` rejected. Trailing slash `foo/` (directory-style) rejected per spec — vfs is files-only. |
| `TestValidatePath_DotPrefix_Allowed` | `.hidden` is accepted (hiding is at the tool layer, not validation). |
| `TestValidatePath_UnderscorePrefix_Allowed` | `_notes/x.md` is accepted. |
| `TestValidatePath_Happy` | `foo.md`, `dir/sub/file.txt`, `a-b_c.json`, `日本語.md` all valid. |

#### `vfs/hash_test.go` — content hash unit tests

| Test | Tests that... |
|------|---------------|
| `TestHashContent_Deterministic` | calling `HashContent` twice with the same input returns the same hash. |
| `TestHashContent_OrderInvariant` | reordering the `[]FileMeta` produces the same hash (suite sorts internally by path). |
| `TestHashContent_Empty` | empty input returns a stable known hash (lock the bytes in a constant; future implementations cannot drift). |
| `TestHashContent_ContentSensitive` | flipping one byte in one file changes the hash. |
| `TestHashContent_PathSensitive` | renaming one file path changes the hash. |

#### `vfs/conformance.go` — suite (run by every backend)

The suite is one exported function. It uses subtests so failures point to the exact contract being violated.

| Subtest name | Tests that... |
|--------------|---------------|
| `CreateProject_Unique` | two `CreateProject` calls return distinct UUIDs. |
| `ProjectExists_True` | `ProjectExists` returns true for a freshly-created project. |
| `ProjectExists_False` | `ProjectExists(ctx, uuid.New())` returns false. |
| `WriteFile_RejectsInvalidPath` | `WriteFile(ctx, p, "../etc/passwd", ...)` returns `ErrPathInvalid`. |
| `WriteFile_RejectsUnknownProject` | `WriteFile` against a non-existent projectID returns `ErrProjectNotFound`. |
| `WriteRead_RoundTrip` | `WriteFile` then `ReadFile` returns the same bytes; metadata `Size`, `ContentType`, `Path` populated. |
| `WriteRead_PreservesContentType` | content-type passed in survives the round trip. |
| `WriteFile_OverwriteUpdatesMeta` | second write to same path replaces content; `UpdatedAt` advances. |
| `ListFiles_Empty` | new project lists zero files. |
| `ListFiles_AfterWrites` | listing returns one entry per written file. |
| `ListFiles_DeterministicOrder` | listing returns the same order across calls (suite sorts by path; backends must too). |
| `ReadFile_NotFound` | reading a path that does not exist returns `ErrFileNotFound`. |
| `DeleteFile_RemovesEntry` | delete + list shows the entry gone; subsequent read returns `ErrFileNotFound`. |
| `DeleteFile_NotFound` | delete against missing path returns `ErrFileNotFound`. |
| `DeleteFile_RejectsInvalidPath` | delete with `..` path returns `ErrPathInvalid`. |
| `Snapshot_CapturesPreState` | snapshot taken before a write, then write, then restore — file is gone. |
| `Snapshot_RestoreCreatedFile` | snapshot before file existed; write file; restore — file is deleted (was created post-snapshot). |
| `Snapshot_RestoreModifiedFile` | snapshot when content was `"a"`; modify to `"b"`; restore — content is `"a"`. |
| `Snapshot_RestoreDeletedFile` | snapshot when file existed; delete file; restore — file reappears with original content. |
| `Snapshot_OnlyChangedPathsTracked` | passing `changedPaths=["a.md"]` creates a snapshot that only reverts `a.md`; other modifications survive restore. |
| `HashState_Stable` | two `HashState` calls with no intervening writes return the same value. |
| `HashState_ChangesOnWrite` | `HashState` differs after a write. |
| `HashState_ChangesOnDelete` | `HashState` differs after a delete. |
| `HashState_OrderIndependent` | writing files A, B, C and writing C, B, A in another project produces identical hashes (paths are the same set). |
| `Tx_BeginCommit_AppliesWrites` (TxStorage only) | `BeginTx` + `WriteFile` via tx + `Commit`; outer storage sees the write. |
| `Tx_BeginRollback_DiscardsWrites` (TxStorage only) | `BeginTx` + `WriteFile` via tx + `Rollback`; outer storage does not see the write. |
| `Tx_Isolated` (TxStorage only) | writes inside an open tx are not visible to a parallel reader using the outer storage until `Commit`. |
| `Tx_RollbackThenCommitErr` (TxStorage only) | `Commit` after `Rollback` returns an error (tx already terminal). |
| `Tx_DoubleCommitErr` (TxStorage only) | second `Commit` returns an error. |
| `Tx_ContextCancelDuringTx` (TxStorage only) | cancelling ctx after `BeginTx` causes the next storage op via the tx to return `context.Canceled`. |

The conformance suite **skips** `Tx_*` subtests if the supplied factory does not return a `TxStorage`.

#### `vfs/memory/memory_test.go` — backend-specific

| Test | Tests that... |
|------|---------------|
| `TestMemory_RunConformance` | invokes `vfs.RunConformance(t, func() vfs.Storage { return memvfs.NewStorage() })`. |
| `TestMemory_SatisfiesTxStorage` | compile-time assertion (`var _ vfs.TxStorage = (*Storage)(nil)`) plus runtime confirmation that the suite ran the `Tx_*` subtests. |
| `TestMemory_ConcurrentReadsAreSafe` | 100 goroutines reading the same file in parallel produce no race (run with `-race`). |
| `TestMemory_ConcurrentWritesSerialize` | 100 goroutines writing the same path produce no race; final content is one of the writes (no torn write). |

**~40 tests in W5 total** (13 path + 5 hash + ~25 conformance subtests + ~4 memory-specific). The conformance subtest count is normative.

### W6 — `agent` core

Package: `github.com/mxcd/aikido/agent`. Plus `internal/testutil`. Files: `agent_test.go`, `run_test.go`, `safety_test.go`.

| Test | Tests that... |
|------|---------------|
| `TestNewAgent_RequiresClient` | `NewAgent(&Options{Storage: ...})` (no Client) returns an error. |
| `TestNewAgent_RequiresStorage` | `NewAgent(&Options{Client: ...})` (no Storage) returns an error. |
| `TestNewAgent_AppliesDefaults` | a sparse Options gets `MaxTurns=20`, `TurnTimeout=120s`, `MaxTokens=8192` populated. |
| `TestRun_HappyPath_NoTools` | stub returns text + `EventEnd`; agent emits `EventText` + `EventEnd("stop")`; channel closes. |
| `TestRun_HappyPath_OneToolThenStop` | stub turn 1 returns `tool_call(write_file)`; turn 2 returns text + end. Agent dispatches the tool, emits `EventToolCall` + `EventToolResult{OK:true}` + `EventText` + `EventEnd("stop")`. |
| `TestRun_HappyPath_MultipleToolsOneTurn` | stub turn 1 returns two tool calls; both dispatch; both `EventToolResult` emitted; turn 2 ends. |
| `TestRun_ToolError_DoesNotAbort` | tool dispatch returns error; agent emits `EventToolResult{OK:false, Error:"..."}` and proceeds; loop ends only when stub stops calling tools. |
| `TestRun_ToolError_ResultEnteredIntoHistory` | the next stub turn's `Request.Messages` contains the tool-error result as a tool-role message, so the model can correct course. |
| `TestRun_UnknownTool_DoesNotAbort` | stub calls a tool that is not in registry; agent emits `EventToolResult{OK:false}` mentioning `ErrUnknownTool`; loop continues. |
| `TestRun_MidStreamError` | stub emits `EventError` mid-turn; agent emits `EventError` + `EventEnd("error")`; channel closes. |
| `TestRun_MaxTurnsExhausted` | stub script returns a tool call every turn; after `MaxTurns`, agent emits `EventEnd("max_turns")`. Assert exact turn count = `MaxTurns`. |
| `TestRun_TurnTimeout` | stub blocks indefinitely; agent's `TurnTimeout` of 100ms fires; agent emits `EventEnd("timeout")`; channel closes within 200ms. |
| `TestRun_CallerCancelsContext` | caller cancels ctx mid-stream; agent emits `EventEnd` with reason indicating cancellation (or `EventError` per current decision — assert which); channel closes within 100ms. |
| `TestRun_UsagePropagated` | stub's `EventUsage` is emitted to the caller as agent `EventUsage`. |
| `TestRun_SystemPromptIncluded` | first message in stub's first `Request` is `Role=System` with `Content=Options.SystemPrompt`. |
| `TestRun_UserMessageIncluded` | second message is `Role=User` with `Content=userText`. |
| `TestRun_AssistantHistoryAccumulated` | turn 2's `Request.Messages` contains the system + user + assistant (turn 1) + tool result messages, in order. |
| `TestRun_TxStorage_CommitOnNoErr` | with `vfs/memory` (TxStorage); turn 1 writes file via tool; tool succeeds; agent commits; outer storage sees the file. Use a counter to assert `BeginTx` + `Commit` happened, `Rollback` did not. |
| `TestRun_TxStorage_RollbackOnToolErr` | turn 1 writes file successfully via tool A; turn 1 then fails tool B; agent rolls back; outer storage does **not** see file from tool A. |
| `TestRun_NonTxStorage_NoBeginTx` | use a `Storage` that does not satisfy `TxStorage`; the agent never calls a transactional method (verified via spy storage). |
| `TestRun_TxStorage_SnapshotBeforeCommit` | on commit, `Snapshot(ctx, projectID, turnID, changedPaths)` is called with the union of all `Result.ChangedPaths` from the turn. |
| `TestRunWithMessages_PrependsSystemPrompt` | history `[user, assistant, user]` is sent to the model as `[system, user, assistant, user]`. |
| `TestRunWithMessages_RejectsSystemInHistory` | history that already contains a `RoleSystem` message returns an error from `RunWithMessages` (or the system message is prepended ahead — pick one in impl and assert). |
| `TestSafety_SameProject_Serialize` | two `Run` calls with identical `projectID` and a stub that pauses 100ms each; total wall time ≥ 200ms (serialized). Assert via timing within tolerance. |
| `TestSafety_DifferentProjects_Parallel` | two `Run` calls with different `projectID`; total wall time ≤ 150ms (overlap allowed). |
| `TestSafety_MutexReleased_OnError` | a `Run` that ends in `EventError` releases the project mutex; a follow-up `Run` on the same project does not deadlock. |
| `TestSafety_MutexReleased_OnTimeout` | same as above, but for `EventEnd("timeout")`. |
| `TestSafety_MutexReleased_OnPanic` | if a tool handler panics, the agent recovers, emits an event, and releases the mutex; follow-up `Run` does not deadlock. |
| `TestSafety_LoadOrStore_OneMutexPerProject` | hammer `Run` from 50 goroutines on the same projectID; assert exactly one mutex is created (use `sync.Map.Range` introspection in test, or reflect on a counter). |

**29 tests in W6** (plus the StubClient lands here as a Go type, not a test).

### W7 — VFS-aware built-in tools

Package: `github.com/mxcd/aikido/agent` (built-in tool handlers). File: `tools_builtin_test.go`.

Per-tool: 1 happy path + 1 invalid-path + 1 not-found + tool-specific edge cases. Tested directly against the handler with a `tools.Env` that uses `vfs/memory`, then end-to-end via stub-driven agent.

#### Direct handler tests

| Test | Tests that... |
|------|---------------|
| `TestReadFile_Happy` | write via storage; `read_file({path:"a.md"})` returns content + content_type + size. |
| `TestReadFile_PathInvalid` | `read_file({path:"../etc/passwd"})` returns `Result{OK:false}` with error mentioning `ErrPathInvalid`. |
| `TestReadFile_NotFound` | `read_file({path:"missing.md"})` returns `Result` mentioning `ErrFileNotFound` (model-friendly text). |
| `TestReadFile_TooLarge` | a 2 MiB file returned with `MaxFileBytes=1 MiB` returns truncation error. |
| `TestWriteFile_Happy` | `write_file({path:"a.md", content:"hi", content_type:"text/markdown"})` writes; result `ChangedPaths=["a.md"]`. |
| `TestWriteFile_PathInvalid` | `write_file({path:"/abs/foo"})` returns `ErrPathInvalid`. |
| `TestWriteFile_DefaultContentType` | omitting content_type defaults to `text/markdown` (or `application/octet-stream` — pick one and lock). |
| `TestWriteFile_RespectsAllowedExtensions` | `AllowedExtensions=[".md"]`; `write_file({path:"x.bin"})` returns clear error. |
| `TestWriteFile_RespectsMaxFileBytes` | content of 2 MiB with `MaxFileBytes=1 MiB` rejected; sub-limit content accepted. |
| `TestListFiles_Happy` | three writes; `list_files({})` returns three entries. |
| `TestListFiles_HidesUnderscoreByDefault` | write `_notes/x.md` and `notes.md`; `list_files({})` with `HideHiddenPaths=true` returns only `notes.md`. |
| `TestListFiles_HidesDotByDefault` | write `.config` and `config.md`; result excludes `.config`. |
| `TestListFiles_ShowsHidden_WhenDisabled` | with `HideHiddenPaths=false`, `_notes/x.md` is listed. |
| `TestDeleteFile_Happy` | write; `delete_file({path:"a.md"})`; subsequent list is empty. |
| `TestDeleteFile_NotFound` | delete a missing path returns clear error in result content. |
| `TestDeleteFile_PathInvalid` | delete with `..` returns `ErrPathInvalid`. |
| `TestDeleteFile_RespectsAllowedExtensions` | with `AllowedExtensions=[".md"]`, deleting `foo.bin` rejected (write list governs delete too — confirm in impl). |
| `TestSearchFiles_Substring` | three files; `search_files({query:"foo"})` returns the file containing `foo` with line number. |
| `TestSearchFiles_CaseInsensitive` | `search_files({query:"FOO"})` matches `foo` in content. |
| `TestSearchFiles_NoMatches` | `search_files({query:"zzz"})` returns empty matches list (no error). |
| `TestSearchFiles_GlobFilter` | files `a.md`, `b.txt` both contain `foo`; `search_files({query:"foo", glob:"*.md"})` returns only `a.md`. |
| `TestSearchFiles_HidesHiddenByDefault` | `_notes/x.md` containing `foo` not returned when `HideHiddenPaths=true`. |
| `TestSearchFiles_GlobMalformed` | `search_files({query:"x", glob:"["})` returns clear error. |

#### Integration via stub agent

| Test | Tests that... |
|------|---------------|
| `TestAgent_VFSTools_WriteThenRead` | stub turn 1 calls `write_file`; turn 2 calls `read_file`; turn 3 ends. Final state: file present, both tool results emitted as events, `EventEnd("stop")`. |
| `TestAgent_VFSTools_RegisterTwice` | calling `RegisterVFSTools` on the same registry twice returns `ErrDuplicateTool` from the second call. |

**25 tests in W7.**

### W8 — `notes` package

Package: `github.com/mxcd/aikido/notes`. Files: `notebook_test.go`, `tools_test.go`.

| Test | Tests that... |
|------|---------------|
| `TestNewNotebook_RequiresStorage` | `NewNotebook(&Options{})` returns an error. |
| `TestNewNotebook_AppliesDefaults` | `Options.Path` defaults to `"_notes/"`; `Options.DocsPath` defaults to `"docs/"`. |
| `TestNotebook_Add_WritesToCorrectPath` | `Add(ctx, projectID, turnID, "body")` writes to `_notes/<turnID>.md`. |
| `TestNotebook_Add_AppendsWithinSameTurn` | two `Add` calls with same turnID produce one file with both bodies, separated by a single newline (or specified separator). |
| `TestNotebook_List_ChronologicalOrder` | three notes in different turns; `List` returns them in `UpdatedAt` ascending order. |
| `TestNotebook_List_Preview` | each `NoteRef.Preview` is the first 200 chars of the note body, max. |
| `TestNotebook_List_Empty` | freshly-created project returns empty slice (no error). |
| `TestNotebook_Read_Concatenates` | three notes; `Read` returns body1 + sep + body2 + sep + body3 in order. |
| `TestNotebook_Read_Empty` | empty project returns `("", nil)`. |
| `TestNotebook_Consolidate_Happy` | three notes + existing target with content `"old"`; stub LLM returns `"merged"`; after `Consolidate`: target file content is `"merged"`, notes deleted, `ConsumedNotes` populated. |
| `TestNotebook_Consolidate_NoExistingTarget` | target file does not exist; consolidate creates it with the stub's output. |
| `TestNotebook_Consolidate_NoNotes` | empty notebook + existing target; consolidate is a no-op (or returns `ConsolidationResult` with empty `ConsumedNotes` — pick one). |
| `TestNotebook_Consolidate_LLMError` | stub LLM returns error; consolidate returns the error; **no notes are deleted**, target file unchanged. |
| `TestNotebook_Consolidate_PartialFailure` | stub LLM returns text; storage write of the merged doc fails; consolidate returns error and notes are **not** deleted (transactional intent). |
| `TestNotebook_Consolidate_InstructionsAppended` | `Consolidate(..., target, "use bullet points")`; the `Request.Messages[0].Content` (system) ends with `"use bullet points"`. |
| `TestNotebook_Consolidate_InvalidTargetPath` | `target="../etc/passwd"` returns `ErrPathInvalid`. |
| `TestNotebook_Consolidate_TargetIsNotePath` | `target="_notes/foo.md"` is rejected (cannot consolidate into a note file). |
| `TestRegisterTools_AllThreeRegistered` | after `RegisterTools(reg, nb)`: `reg.Has("add_note") && reg.Has("list_notes") && reg.Has("consolidate_notes_into_doc")`. |
| `TestRegisterTools_DispatchAddNote` | dispatch `add_note({body:"x"})` calls `Notebook.Add`; result `ChangedPaths=["_notes/<turnID>.md"]`. |
| `TestRegisterTools_DispatchListNotes` | dispatch `list_notes({})` returns the same shape as `Notebook.List`. |
| `TestRegisterTools_DispatchConsolidate` | dispatch `consolidate_notes_into_doc({target_path:"profile.md"})` calls `Notebook.Consolidate` and returns the `ConsolidationResult`. |
| `TestNotes_EndToEnd_StubAgent` | stub script: turn 1 calls `add_note("a")`; turn 2 calls `add_note("b")`; turn 3 calls `add_note("c")`; turn 4 calls `consolidate_notes_into_doc({target_path:"profile.md"})`; turn 5 ends. Final state: `profile.md` exists with stub-LLM-output content; `_notes/` is empty. |

**22 tests in W8.**

---

### Test count summary across waves

| Wave | Test count |
|------|------------|
| W0 | 0 (CI passes on empty module) |
| W1 | 12 |
| W2 | 11 |
| W3 | 21 |
| W4 | 21 |
| W5 | ~40 (13 path + 5 hash + ~25 conformance subtests + ~4 memory) |
| W6 | 29 |
| W7 | 25 |
| W8 | 22 |
| **Total** | **~181** |

This exceeds the "50–80" rough target in the brief. The number is defensible because (a) W5 conformance contributes ~25 subtests that ship as one suite — they are 25 named test cases but cost one author-write of the suite plus one delegating test in `vfs/memory_test.go`; (b) tool tests in W7 are per-tool-per-edge-case and each is a few lines. If the count needs trimming pre-merge, W4 schema tests are the best candidates to consolidate (table-drive into one test).

---

## CI workflow

One workflow file. One job. Three steps. No matrix on Go versions in v1 (Go 1.23 is the minimum and the only supported version per `go.mod`). Adding 1.24+ to the matrix is a v1.x patch, not a breaking change.

### `.github/workflows/ci.yml`

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  check:
    name: lint + test + build
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: "1.23"
          cache: true

      - name: Verify go.mod is tidy
        run: |
          go mod tidy
          git diff --exit-code go.mod go.sum

      - name: Lint
        uses: golangci/golangci-lint-action@v6
        with:
          version: v1.61
          args: --timeout=5m

      - name: Test (with race detector and coverage)
        run: |
          go test -race -coverprofile=coverage.out -covermode=atomic ./...

      - name: Build examples
        run: go build ./examples/...

      - name: Coverage summary (informational)
        run: go tool cover -func=coverage.out | tail -1
```

**Why one job, three steps:**

- The repo is small enough that splitting into parallel `lint`, `test`, `build` jobs costs more in spin-up time than it saves in wall time.
- `go test ./...` already covers everything except `./examples/...`, which is built explicitly.
- The race detector is on for every CI run. Concurrency bugs in the per-project mutex are exactly the class of bug that surfaces here.
- Coverage is uploaded as a job summary (`tail -1` of `go tool cover -func`) but **no coverage gate** in v1. Targets are aspirational; gating raises the bar pre-implementation, not post-.

**What CI does not do:**

- Network calls to OpenRouter. Smoke tests are local-only.
- Notification of failures to Slack / Discord. Add when there is a team; for now, MaPa watches the GitHub UI.
- Release / publish. Tagging is manual until v0.2 at earliest.

### `.golangci.yml`

```yaml
run:
  timeout: 5m
  go: "1.23"
  tests: true

linters:
  disable-all: true
  enable:
    - errcheck       # unchecked errors break the "errors carry context" rule
    - govet          # builtin vet
    - ineffassign    # unused assigns mask bugs
    - staticcheck    # SA-class checks; the heavy hitter
    - unused         # dead code is debt
    - gofmt          # canonical formatting
    - goimports      # canonical import ordering
    - gosec          # security: S1, S2 only — see linters-settings

linters-settings:
  gosec:
    includes:
      - G101  # hardcoded credentials
      - G102  # bind to all interfaces
      - G104  # audit errors not checked (overlap with errcheck; keep)
      - G201  # SQL string formatting (not used in v1 but prevents future drift)
      - G304  # file path provided as taint input
      - G305  # zip slip
      - G401  # weak crypto (we use SHA-256, but lock the rule)
    excludes:
      - G306  # poor file permissions on writes — vfs/memory does not write to disk
  errcheck:
    check-type-assertions: true
    check-blank: false
  govet:
    enable:
      - shadow

issues:
  max-issues-per-linter: 0
  max-same-issues: 0
  exclude-rules:
    # Test files may use _ for ignored returns from testutil constructors.
    - path: _test\.go
      linters: [errcheck]
      text: "Error return value of `.*Close` is not checked"
    # Conformance suite uses subtest names that do not match TestX.
    - path: vfs/conformance\.go
      linters: [unused]
```

**Linter rationale:**

- `staticcheck` catches the common Go traps; it stays on always.
- `gosec` is narrowed to the S1/S2 (high-confidence) rules. The full S3+ set has too many false positives for a library this small. `G304` is critical because aikido handles model-supplied paths.
- `goimports` enforces import grouping (stdlib, third-party, local).
- `revive`, `gocritic`, `gocyclo` are intentionally **off**. They produce stylistic noise the reference code (`go-basicauth`) does not enforce; consistency with that repo's conventions matters more than maximal lint coverage.

### `justfile`

```just
default:
    @just --list

# Packages under test — exclude examples (no test surface, would skew coverage).
test_packages := `go list ./... | grep -v '/examples/'`

# Default check: vet + lint + test. Used in CI and locally before push.
check: tidy fmt vet lint test build

test:
    go test {{test_packages}} -v

test-race:
    go test {{test_packages}} -race -v

test-coverage:
    go test {{test_packages}} -cover -coverprofile=coverage.out
    go tool cover -func=coverage.out

test-coverage-html:
    go test {{test_packages}} -coverprofile=coverage.out
    go tool cover -html=coverage.out -o coverage.html
    @echo "Coverage report: coverage.html"

build:
    go build ./...
    go build ./examples/...

fmt:
    go fmt ./...
    goimports -w .

vet:
    go vet ./...

lint:
    golangci-lint run

tidy:
    go mod tidy
    git diff --exit-code go.mod go.sum

# Manual smoke tests. Requires OPENROUTER_API_KEY in env.
smoke:
    @if [ -z "$OPENROUTER_API_KEY" ]; then echo "OPENROUTER_API_KEY not set"; exit 1; fi
    @echo "=== chat-oneshot ==="
    go run ./examples/chat-oneshot
    @echo "=== agent-vfs ==="
    go run ./examples/agent-vfs
    @echo "=== notes-consolidate ==="
    go run ./examples/notes-consolidate

# Useful one-offs.
clean:
    rm -f coverage.out coverage.html

doc:
    @echo "Open http://localhost:6060/pkg/github.com/mxcd/aikido/"
    godoc -http=:6060
```

**Justfile contract:**

- `just check` runs in CI and locally; it must succeed before any PR is merged.
- `just smoke` is local-only and requires a real API key.
- `just test-race` is a pre-push aid for changes touching the agent loop.
- `just doc` is for design review; not part of CI.

---

## Coverage targets

Numbers are aspirational, not gated in CI. They are defensible because each is justified by what the package actually contains.

| Package | Target line coverage | Why |
|---------|----------------------|-----|
| `llm` | 90% | Mostly types, `Collect` helper, model catalog. The error wrappers are sentinels (low surface). 90% is achievable without contortion. |
| `llm/openrouter` | 80% | The SSE parser, retry logic, and tool-call assembly are the hot path. Defensive branches (HTTP edge cases like `EOF` mid-stream) are hard to exercise without integration; 80% is honest. |
| `internal/sseparse` | 95% | Pure parser; every line shape has a test. |
| `tools` | 90% | Registry + schema helpers are simple. The reflection helper has fewer paths to test. |
| `vfs` | 90% | Path validation has many cases; suite + memory backend covers them. |
| `vfs/memory` | 95% | Conformance suite drives almost all of it; the few gaps are race-detector-only edge cases. |
| `agent` | 80% | The loop has concurrency, timeouts, and tx detection. Complete coverage requires nondeterministic-shape tests; 80% is the honest target. |
| `notes` | 85% | Five public methods, each with a stub-driven test. |
| `retry` | 95% | Pure helper; jitter and base-delay are testable with a clock. |
| `internal/testutil` | TBD with implementer | This is test code itself; coverage of test helpers is meta and not gated. |

**No CI gate.** Drop below target → mention in PR description. Two consecutive PRs below target → file an issue. v1 ships when waves W0–W8 are merged and `just check` is green; coverage is not on the release checklist.

---

## Risk register

One row per identified risk. Likelihood and Impact are L (low), M (medium), H (high). The "Test that proves mitigation works" column is the **single test name** that, if it passes, demonstrates the risk is bounded. If a risk has no testable mitigation, it should be escalated in the Open Questions section instead.

Categories are interleaved per wave for chronological reading. Filter mentally by category prefix in `Description` if needed.

| ID | Wave | Description | Likelihood | Impact | Mitigation | Test that proves mitigation works |
|----|------|-------------|------------|--------|------------|-----------------------------------|
| R-001 | W3 | **Provider — SSE drift:** OpenRouter reorders `delta` fields or adds new envelope keys. Parser written today silently misses fields tomorrow. | M | M | Parser is permissive on unknown fields; transcripts updated when wire format changes; smoke tests catch drift before tagging. | `TestStream_SimpleText` (locks the minimum viable shape). Smoke tests cover regression beyond fixtures. |
| R-002 | W3 | **Provider — finish_reason inconsistency:** some models emit `finish_reason: "tool_calls"`, others `"function_call"` (legacy). Tool-call assembly may not flush. | M | M | Assembly flushes on choice-done, regardless of `finish_reason` value; finish reason is mapped to a sentinel in agent layer. | `TestAssembleToolCalls_ChoiceDoneFlushes` |
| R-003 | W3 | **Provider — tool-call args invalid JSON:** model returns `{"a": 1` (unterminated). Assembly emits an event with broken JSON; downstream `Dispatch` panics or errors opaquely. | M | M | Assembly validates JSON via `json.Valid` before emitting; if invalid, emit `EventError` instead of `EventToolCall`. | `TestAssembleToolCalls_DropsIncompleteOnError` (extend to cover the JSON-validity case). |
| R-004 | W3 | **Provider — usage in middle of stream:** OpenRouter sometimes emits `usage` in a non-terminal frame. Test fixtures may capture only one shape. | L | L | Code emits `EventUsage` whenever encountered; downstream tolerates multiple `EventUsage` events. | `TestStream_UsageEmittedBeforeEnd` plus an additional case in `usage_only_in_final.sse` documenting the contract. |
| R-005 | W3 | **Provider — model ID dots vs hyphens:** caller passes `anthropic/claude-sonnet-4.6`; OpenRouter returns 400 `model not found` because the ID is dot-separated. Subtle bug; user blames library. | H | M | `normalizeModelID` runs unconditionally on outgoing model IDs. Lock with explicit unit + integration tests. | `TestNormalizeModelID_RequestBodyApplies` |
| R-006 | W3 | **Provider — silent cache_control drop:** OpenRouter forwards Anthropic `cache_control` blocks but other providers strip them. User sees no caching savings, no error. | M | L | Document explicitly; v3 adds direct-Anthropic fallback. v1 does not pretend to verify cache hits. | (no test — documented behavior). Risk mitigated only by `cache_control_passthrough.sse` proving the parser at least surfaces returned cache tokens. |
| R-007 | W3 | **Provider — 429 retry-after parsing:** `Retry-After` header may be a delta (`1`) or HTTP-date (`Wed, ...`). Parsing the date wrong skips backoff. | L | M | Support delta-seconds only in v1; HTTP-date logged as warning and falls back to default backoff. Document as known limitation. | `TestStream_RetryOn429` (covers delta case only — TBD case for date). |
| R-008 | W3 | **Provider — mid-stream replay:** retrying a stream after partial token emission would re-bill the user and re-emit text. The agent must not retry mid-stream. | L | H | Retry only at stream-start (status code received before first SSE frame). Mid-stream errors propagate immediately. | `TestStream_NoRetryAfterStreamStarted` |
| R-009 | W6 | **Concurrency — mutex first-time-for-projectID race:** two goroutines both call `Run` with a never-seen projectID; both `LoadOrStore` simultaneously. With naive `sync.Map.Load + Store`, one mutex is created twice; serialization breaks. | M | H | Use `sync.Map.LoadOrStore` exclusively; the API guarantees one stored value per key. | `TestSafety_LoadOrStore_OneMutexPerProject` |
| R-010 | W6 | **Concurrency — mutex leak on panic:** a tool handler panics; the deferred unlock never runs because the goroutine exits abnormally. Future `Run` on same project deadlocks. | M | H | Wrap tool dispatch in `recover`; emit `EventError`; release mutex via `defer` regardless. | `TestSafety_MutexReleased_OnPanic` |
| R-011 | W6 | **Concurrency — channel close race:** the producer goroutine closes the event channel; a slow consumer is mid-`select` on the channel. Closing while writes are pending panics with "send on closed channel." | M | H | Producer always closes the channel from a single goroutine, after all sends; never close from caller side. Test with `goleak`. | `TestStream_ContextCancelled_ChannelCloses` (extend to use `goleak.VerifyNone(t)` deferred check). |
| R-012 | W6 | **Concurrency — context cancellation lost in stream forwarding:** the agent reads from the LLM stream channel and writes to its own; if it does not select on `ctx.Done()` in both directions, cancellation is delayed by one event boundary. | M | M | Always `select { case ev := <-llmChan: case <-ctx.Done(): }` and `select { case agentChan <- ev: case <-ctx.Done(): }`. | `TestRun_CallerCancelsContext` |
| R-013 | W6 | **Concurrency — TurnTimeout double-cancel:** `TurnTimeout` and caller-supplied ctx interact (e.g., caller already cancelled, then turn times out). The agent should report exactly one `EventEnd`. | M | L | Single source of truth for `EventEnd` reason: timeout takes precedence over cancellation in v1. Document explicitly. | `TestRun_TurnTimeout` (assert exactly one `EventEnd` with reason="timeout"). |
| R-014 | W5,W6 | **Storage — TxStorage capability detection bypass:** a backend that has a partial `BeginTx` (e.g., returns `(nil, errors.New("unsupported"))`) but satisfies the interface. Agent calls `BeginTx`, gets nil tx, dereferences. | L | M | Agent checks return value of `BeginTx`; on error, falls back to non-tx path with a logged warning. | `TestRun_TxStorage_BeginTxErrorFallsBack` (add to W6). |
| R-015 | W5 | **Storage — snapshot capture race:** two `Run` calls on different projects both snapshot in parallel; if the storage shares state across projects (bad backend), snapshots interleave. | L | M | Conformance suite asserts isolation per projectID. Backend bugs are user-visible. | `vfs.RunConformance` `Snapshot_*` subtests run with a `t.Parallel()` variant (TBD with implementer). |
| R-016 | W5 | **Storage — conformance suite gap on tx isolation:** if the suite does not parallelize a write inside a tx with a read outside, MVCC bugs in user backends ship undetected. | M | M | `Tx_Isolated` subtest runs the read in a separate goroutine, asserts read does not see uncommitted write. | `Tx_Isolated` |
| R-017 | W5 | **Storage — hash determinism breaks across Go versions:** `HashContent` uses `map` iteration; without sorting, hash differs run to run. | L | H | Sort `[]FileMeta` by path before hashing. Lock with explicit invariance test. | `TestHashContent_OrderInvariant` |
| R-018 | W5 | **Storage — path validation bypass via Unicode:** `ValidatePath` rejects ASCII `..` but accepts `..` (homoglyphs). | L | M | Validation rejects literal `..` in any UTF-8 form (path is stored as UTF-8); `\u`-escaped paths are not accepted as input because Go strings are bytes-of-UTF-8. Document. | `TestValidatePath_BackslashTraversal` (extend to include unicode case). |
| R-019 | W5,W7 | **Security — path traversal:** model writes `path: "../../etc/passwd"`; if the user's storage backend writes to disk and the validation layer is bypassed, host filesystem is reachable. | M | H | Validation runs in the tool layer **and** in `vfs.WriteFile`/`DeleteFile` as defense in depth. User backends are documented to call `ValidatePath` themselves. | `TestWriteFile_PathInvalid` + conformance `WriteFile_RejectsInvalidPath`. |
| R-020 | W5,W7 | **Security — null-byte path injection:** `path: "foo.md\x00.txt"`; some filesystems treat the null as terminator and write to `foo.md`. | L | H | `ValidatePath` rejects null bytes anywhere in the string. | `TestValidatePath_NullByte` |
| R-021 | W7 | **Security — file size DoS:** model calls `write_file({path:"big.md", content:<1 GB string>})`; agent buffers in memory and OOMs. | M | H | `MaxFileBytes` (default 1 MiB) enforced in the tool handler before storage call; rejected with clear error. | `TestWriteFile_RespectsMaxFileBytes` |
| R-022 | W2 | **Security — API key leakage in error messages:** provider returns `{"error":{"message":"invalid key sk-or-...full-key..."}}`; library wraps and logs the message verbatim. | L | H | Error wrappers redact `Bearer <token>` headers and `sk-` patterns when serializing. (TBD: confirm impl detail with implementer; alternatively, document that callers must redact logs.) | `TestStream_NonStream_AuthFailure` (assert error message does not contain the literal API key string in test setup). |
| R-023 | W7 | **Security — model-injected tool args bypass schema:** model returns `{"path":"../etc"}`; if the registry trusts the args, the tool is called. | M | H | All tools call `ValidatePath` on path-shaped arguments. Custom-tool authors are warned in CONTRIBUTING. | `TestReadFile_PathInvalid` |
| R-024 | W7 | **Security — write_file content_type bypass:** model writes `content_type: "text/html"` containing JS; user later renders the file in a browser without escaping. | L | M | Library is content-agnostic. Document explicitly: callers escape on output. Hidden-path filter does not address this. | (no test — documented). |
| R-025 | W6 | **Security — tool name collision via late registration:** user registers `write_file` after `RegisterVFSTools`; second registration replaces (silent) or errors (current decision: errors). | L | M | `Registry.Register` returns `ErrDuplicateTool` on collision; `RegisterVFSTools` returns the first error. | `TestRegistry_RegisterDuplicate` + `TestAgent_VFSTools_RegisterTwice`. |
| R-026 | W1 | **API shape — type bikeshedding before v0.1.0:** `Event` uses `Tool *ToolCall` not `ToolCall ToolCall`; if changed mid-implementation, every `agent` test breaks. | M | L | Lock types in W1 against `API.md`. Subsequent waves do not modify W1 types. | `TestEvent_FieldShape` (lock with reflection — assert `Event` has fields `Kind`, `Text`, `Tool`, `Usage`, `Err`). |
| R-027 | post-v0.1 | **API shape — non-additive change:** v0.1.x introduces a method on `Storage` interface; user backends break. | L | H | After v0.1.0 tag: never modify any interface in `llm`, `vfs`, `tools`, `agent`, `notes`. New behavior via additional capability interfaces only. ADR added to DECISIONS.md if a change is genuinely required. | (process control — no test). Build-verify in CI catches accidental breakage in `examples/`. |
| R-028 | post-v0.1 | **API shape — adding required field to Options struct:** `agent.Options.Locker` becomes required; users not setting it get nil-pointer. | L | M | New Options fields are always optional (defaulted in constructor). Locker fits this; nil = in-process mutex. | (constructor-default tests cover; pre-existing `TestNewAgent_AppliesDefaults`). |
| R-029 | W3,W6 | **Test determinism — flaky retry timing:** `TestStream_RetryOn429` waits for `Retry-After` of `1s`; on slow CI the test exceeds 5s. Marked flaky; merged with risk. | M | L | Retry policy injected via `retry.Policy`; tests use `BaseDelay = 1ms`, `Jitter = 0`. | `TestStream_RetryOn429` runs with a 1ms-base policy, asserting two `Stream` round-trips happened within 100ms. |
| R-030 | W6 | **Test determinism — TurnTimeout flaky on slow CI:** `TestRun_TurnTimeout` sets timeout to `100ms` and asserts `EventEnd` within `200ms`. Fails on cold CI. | M | L | Use `TurnTimeout = 50ms`, assertion bound `500ms`. Document that timing tests have generous bounds. | `TestRun_TurnTimeout` |
| R-031 | W3 | **Test determinism — real network leaks into CI:** a developer accidentally writes a test using `openrouter.NewClient(...)` against the real endpoint. CI hits OpenRouter, fails (no key), or worse, succeeds and burns API credits. | L | M | All non-fixture tests use `httptest.Server` with the mock URL passed via `Options.BaseURL`. Add a CI guard: grep for `openrouter.ai` in `_test.go` files and fail. | (process control — add to `just check` as a script). |
| R-032 | W6 | **Test determinism — goroutine leak on test failure:** test fails mid-stream; producer goroutine is left running; subsequent tests in the same package share the leaked goroutine and behave unpredictably. | M | M | Every streaming test ends with `t.Cleanup` calling `cancel()` on the test's context. CI runs with `goleak.VerifyTestMain` in TestMain. | `TestStream_ContextCancelled_ChannelCloses` (assert via `goleak`). |
| R-033 | W4 | **Reflection schema — invopop/jsonschema breaking change:** the dep ships v6 with new field names; aikido's `SchemaFromType` produces nonsense schemas. | L | M | Pin minor version in `go.mod`; schema reflection helper is opt-in (not in the agent's hot path); CI catches regression via `TestSchemaFromType_*`. | `TestSchemaFromType_FlatStruct` |
| R-034 | W8 | **Notes — consolidation prompt drift:** the default prompt changes between releases; users relying on stable consolidation behavior surprised. | L | L | Default prompt frozen in `notes/prompts.go` as a const; changes require ADR + release-note. Override via `instructions` argument. | `TestNotebook_Consolidate_InstructionsAppended` (locks the contract). |
| R-035 | W8 | **Notes — partial consolidation failure leaves orphan notes:** stub returns text; storage write succeeds; note-deletion fails partway. Half the notes deleted, target file written. | M | M | Wrap consolidation in a tx if `TxStorage`. Without tx, accept best-effort and document. | `TestNotebook_Consolidate_PartialFailure` |
| R-036 | W8 | **Notes — turnID collision across runs:** caller reuses turnID across `Run` calls; second `Add` overwrites first turn's notes file at `_notes/<turnID>.md`. | L | M | turnID is library-generated per `Run`. Caller cannot inject. Documented. | (process — internal turnID generation). Verified via `TestRun_HappyPath_*`. |
| R-037 | scope | **Scope creep — image package sneaking into v1:** PR adds `image/` to satisfy a v2 use case. v1 release blocked. | L | M | DECISIONS.md ADR-010 explicit. CONTRIBUTING.md "What not to PR" lists image. PR review catches. | (process). |
| R-038 | scope | **Scope creep — CLI binary in v1:** PR adds `cmd/aikido/`. Pulls in cobra. Library users now transitively own cobra. | L | M | DECISIONS.md ADR-001 + ADR-010. PR review catches. | (process). |
| R-039 | scope | **Scope creep — vfs/postgres reference impl:** PR adds `vfs/postgres/`. Pulls in pq driver. v1 users not running postgres now own a dep. | L | M | DECISIONS.md ADR-008. CONTRIBUTING.md "What not to PR" lists postgres. | (process). |
| R-040 | scope | **Scope creep — multi-instance lock in v1:** PR adds `agent.Locker` interface and Redis impl. v1 commits to scope MaPa explicitly punted to v3. | L | M | DECISIONS.md ADR-009. PR review catches. | (process). |
| R-041 | W5 | **Storage — content-type guessing:** a backend that does not preserve `ContentType` returns empty strings on read; agent's content-type logic relies on it. | L | L | `ReadFile` returns `FileMeta.ContentType`; if backend returns empty, agent leaves untouched. Tests pin: backend is responsible for round-tripping the content type. | `WriteRead_PreservesContentType` |
| R-042 | W2 | **Provider — request body shape mismatch:** OpenRouter API rejects `tools: null` vs missing `tools` differently. | L | L | Marshal request with `omitempty` on optional fields; tests assert request body. | `TestStream_RequestBodyShape` |

**42 risk-register entries** across the project. Categorized:

- **Provider** — R-001 through R-008, R-042: 9
- **Concurrency** — R-009 through R-013: 5
- **Security** — R-019 through R-025: 7
- **API shape** — R-026 through R-028: 3
- **Test determinism** — R-029 through R-032: 4
- **Storage** — R-014, R-015, R-016, R-017, R-018, R-041: 6
- **Scope creep** — R-037 through R-040: 4
- **Other (W4, W8)** — R-033, R-034, R-035, R-036: 4

### Coverage of SECURITY.md threats

For each documented threat in `docs/SECURITY.md`, this strategy maps to a test and a risk row:

| SECURITY.md threat | Test | Risk row |
|--------------------|------|----------|
| Model emits malformed JSON args | `TestRegistry_DispatchInvalidJSON` + `TestAssembleToolCalls_DropsIncompleteOnError` | R-003 |
| Model attempts path traversal (`../etc/passwd`) | `TestValidatePath_DotDotPrefix`, `TestWriteFile_PathInvalid`, `WriteFile_RejectsInvalidPath` (conformance) | R-019 |
| Model attempts absolute path | `TestValidatePath_Absolute`, `TestWriteFile_PathInvalid` | R-019 |
| Model attempts null-byte injection | `TestValidatePath_NullByte` | R-020 |
| Model calls unknown tool | `TestRegistry_DispatchUnknown`, `TestRun_UnknownTool_DoesNotAbort` | R-025 |
| Model loops indefinitely | `TestRun_MaxTurnsExhausted`, `TestRun_TurnTimeout` | R-013 |
| Path length exceeded (512 bytes) | `TestValidatePath_TooLong` | R-019 |
| File-size DoS (default 1 MiB cap) | `TestWriteFile_RespectsMaxFileBytes` | R-021 |
| API key leakage in logs | `TestStream_NonStream_AuthFailure` (assert no key in error) | R-022 |
| Tool error must not abort loop | `TestRun_ToolError_DoesNotAbort`, `TestRun_ToolError_ResultEnteredIntoHistory` | (mitigation, not risk) |
| Hidden-path filter | `TestListFiles_HidesUnderscoreByDefault`, `TestSearchFiles_HidesHiddenByDefault` | (cosmetic; not a security boundary per SECURITY.md). |
| Multi-tenancy single-instance | (no test — documented limitation) | (R-040 is scope; multi-instance is v3). |

Every threat has at least one test and at least one risk-register row.

---

## Smoke test protocol

Pre-tag-v0.1.0 sanity check. Run on a clean shell with current `main` checked out. **Real OpenRouter calls. Real cost (estimate: < $0.10 total).** Fails if any step does not produce the expected output shape.

### Setup

```sh
cd ~/github.com/mxcd/aikido
git checkout main
git pull
just check                          # Step 0: CI must already be green locally
export OPENROUTER_API_KEY=sk-or-...  # paste from password manager
```

### Step 1: chat-oneshot

```sh
go run ./examples/chat-oneshot
```

**Expected output shape:**

```
[Stream]
Hello! How can I help you today?
[Done]
Usage: prompt=12 tokens, completion=10 tokens, cost=$0.0001
```

**What's being asserted by eye:**
- The text is non-empty and contains complete sentences.
- `prompt` and `completion` token counts are both > 0.
- `cost` is small but non-zero.
- Process exits 0.

**Failure modes and fixes:**
- `model not found` → check `examples/chat-oneshot/main.go` for hardcoded model ID; verify dot/hyphen normalization.
- `401 unauthorized` → API key is wrong or expired.
- `context deadline exceeded` → network slow; retry. If reproducible, check `Options.HTTPClient.Timeout`.
- No `[Done]` printed → channel did not close. Investigate.

### Step 2: agent-vfs

```sh
go run ./examples/agent-vfs
```

**Expected event sequence (printed live):**

```
[Stream] starting agent for project <uuid>
[Event] Text: "I'll create a notes file for you."
[Event] ToolCall: write_file({"path":"notes.md","content":"..."})
[Event] ToolResult: ok=true path=notes.md size=42
[Event] Text: "Done. The file is created."
[Event] Usage: prompt=420 tokens, completion=58 tokens
[Event] End: stop
[Final state]
  notes.md (42 bytes, text/markdown)
[Done]
```

**What's being asserted by eye:**
- At least one `Text`, one `ToolCall`, one `ToolResult` (OK=true), one `Usage`, one `End("stop")`.
- Final VFS state contains `notes.md`.
- No `EventError`.

**Failure modes:**
- `EventError: unknown tool: write_file` → `RegisterVFSTools` not called or registry not wired into `agent.Options.Tools`.
- `EventEnd: max_turns` → model loops. Check the example's user prompt; should be a single-shot intent.
- No `ToolCall` event → model is not tool-calling on this prompt. Check `agent.Options.Tools` is non-nil and `Defs()` is non-empty.

### Step 3: notes-consolidate

```sh
go run ./examples/notes-consolidate
```

**Expected behavior:**

The example runs four "user messages" through the agent (or batches them — implementer's call):
1. "Remember: the user prefers Postgres."
2. "Remember: they're building a Go API."
3. "Remember: deployment target is Kubernetes."
4. "Now consolidate the notes into profile.md."

**Expected final state:**

`profile.md` contains a structured single document like:

```markdown
# User Profile

## Preferences
- Database: Postgres
- Language: Go
- Deployment: Kubernetes

## Project context
The user is building a Go API targeted for Kubernetes deployment with Postgres backing.
```

**What's being asserted by eye:**
- `profile.md` exists in the final VFS state.
- It is one coherent document, not a concatenation of bullet points (the consolidation prompt should produce structure).
- All facts from the four input messages survive.
- `_notes/` is empty after consolidation.

**Failure modes:**
- `profile.md` looks like a raw concatenation → consolidation prompt is too weak; check `notes/prompts.go`.
- `_notes/` still has files → consolidation succeeded but deletion failed; check `Notebook.Consolidate` ordering.
- `consolidate_notes_into_doc` not called by the model → user prompt is too vague; tweak the example.

### Step 4: tag and push

If steps 1–3 all pass:

```sh
git tag -a v0.1.0 -m "v0.1.0: initial release of aikido"
git push origin v0.1.0
```

Then:

- Update `docs/README.md` "Status" section to reference `v0.1.0`.
- Open a PR: "release: v0.1.0".
- Merge.
- Do **not** retroactively update CI to gate on v0.1.0; the tag is informational.

### What to do if the smoke fails

- One step fails → fix the bug; restart from Step 1. Do not tag.
- All three steps pass but cost is unexpectedly high (> $1) → check for retry loops; the agent may have hit max_turns and the user is paying for the wasted budget.
- Smoke passes but a smoke run a week later fails on the same code → that is provider drift (R-001). File an issue, update fixtures.

---

## Open questions

Items where the existing plan, security doc, or API surface leaves ambiguity that affects test design. Resolve before implementation, not during.

1. **Cancellation event reason.** `agent.Event.EndReason` is documented as `"stop" | "max_turns" | "error" | "timeout"`. What about caller-cancelled context? The current docs imply `"error"` (mid-stream-style); CONTRIBUTING.md TODO suggests adding `"cancelled"`. Decision needed before W6 because `TestRun_CallerCancelsContext` asserts the reason. **Recommend: add `"cancelled"` as a fifth reason in W6**, so the agent tells callers explicitly that they cancelled.

2. **Tool result OK semantics.** `ToolResult.OK` is `false` when the handler returns an error. What about when the handler returns a `Result` whose `Content` indicates failure but `error == nil` (e.g., a "file not found" tool that returns `{ok:false, message:"not found"}` as content)? Two interpretations: (a) `ToolResult.OK` mirrors handler error only, content is opaque; (b) `OK` is derived from content + error. **Recommend (a)**: the registry never inspects `Result.Content`. Locks `TestReadFile_NotFound` semantics.

3. **Path validation: trailing slash.** `foo/` (directory-style) — reject or normalize? VFS is files-only; reject is safer but may surprise users coming from filesystem-style backends. **Recommend reject.** Locks `TestValidatePath_LeadingSlash` and a sibling test for trailing.

4. **Path validation: double slash.** `foo//bar` — reject or normalize to `foo/bar`? Normalization is friendlier; rejection is stricter and easier to reason about. **Recommend reject.**

5. **`content_type` defaults.** If a tool call to `write_file` omits `content_type`, what is the default? Options: `application/octet-stream`, `text/markdown`, infer from extension. **Recommend infer from extension** with `text/markdown` for unknown — locks `TestWriteFile_DefaultContentType`.

6. **Schema reflection: panic vs error on unsupported types.** `SchemaFromType[chan int]()` — panic immediately or return an empty schema with a build-time-warning comment? **Recommend panic at registration time**, since this is a programmer error not a runtime condition. Locks `TestSchemaFromType_UnsupportedTypePanics`.

7. **Stub LLM script exhaustion.** If the agent calls `Stream` more times than the script has turns, what happens? Options: (a) `Stream` returns an error; (b) `Stream` returns an empty turn (just `EventEnd`); (c) test panics with a clear message. **Recommend (a)** with `ErrStubExhausted`. Locks `StubClient` semantics.

8. **`Notebook.Consolidate` empty notebook + existing target.** Is consolidation a no-op or does it call the LLM with empty input? **Recommend no-op** that returns a result with empty `ConsumedNotes` and `Usage = nil`; do not bill the user for nothing.

9. **`agent.Options.MaxConcurrentTurns` field — exists or implicit?** The current API.md does not list it; ARCHITECTURE.md describes the per-project mutex as the implementation. **Confirm**: there is no public option; concurrency is always 1 per project. Locks the absence of `MaxConcurrentTurns` in `Options`.

10. **TxStorage `BeginTx` error fallback.** If `BeginTx` returns an error (network blip in a real backend), does the agent (a) abort the turn or (b) fall back to non-tx mode for that turn? **Recommend (b) with a logged warning** — the model's tool calls still execute; user gets best-effort semantics. Locks R-014's mitigation.

11. **`vfs.HashState` content of the hash.** The current spec says "SHA-256 over sorted `path|size|content-hash` triples." Implementer must commit to: (a) what separator (newline? null? `|`?); (b) is `content-hash` the SHA-256 of the bytes or something else? Lock with `TestHashContent_Empty`'s constant value. **Recommend: separator `\n`, content-hash is hex SHA-256 of bytes.**

12. **Coverage gate.** This doc says no CI gate. Confirm? If MaPa wants a soft gate (`coverage < 70%` warns but does not fail), the workflow needs the extra step. **Recommend: no gate in v1; revisit in v0.2.**

13. **goleak in test packages.** `go.uber.org/goleak` is a third-party dep. Including it for goroutine-leak verification in `TestStream_*` adds one dep to test code. Worth it? **Recommend yes** — the cost of an in-flight goroutine bug in v0.1 is much higher than the cost of one test-only dep. Locks R-011 and R-032.

14. **Smoke test cost cap.** Should `just smoke` print a running cost total and abort if > $0.50? Adds complexity but prevents accidental burn (e.g., agent stuck in `max_turns` loop). **Recommend yes** if it can be done in < 20 lines of Go in the example; otherwise skip and document the expected cost in the smoke protocol above.

15. **Conformance suite parallelism.** Should `vfs.RunConformance` mark each subtest with `t.Parallel()`? Memory backend is fine in parallel; user backends may not be. **Recommend: serial by default; offer a `RunConformanceParallel` variant for backends that opt in.** Affects R-015's mitigation.

---

## Table-driven test conventions

aikido follows the same table-driven idiom as `go-basicauth`. Every test that has more than two related assertions becomes a table. The shape:

```go
func TestValidatePath(t *testing.T) {
    cases := []struct {
        name string
        path string
        want error
    }{
        {"empty", "", vfs.ErrPathInvalid},
        {"absolute", "/foo", vfs.ErrPathInvalid},
        {"dotdot prefix", "../foo", vfs.ErrPathInvalid},
        {"dotdot middle", "foo/../bar", vfs.ErrPathInvalid},
        {"dotdot end", "foo/..", vfs.ErrPathInvalid},
        {"null byte", "foo\x00bar", vfs.ErrPathInvalid},
        {"too long", strings.Repeat("a", 513), vfs.ErrPathInvalid},
        {"happy", "foo.md", nil},
        {"happy nested", "dir/sub/file.txt", nil},
        {"happy unicode", "日本語.md", nil},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            got := vfs.ValidatePath(tc.path)
            if !errors.Is(got, tc.want) {
                t.Errorf("ValidatePath(%q) = %v, want %v", tc.path, got, tc.want)
            }
        })
    }
}
```

**Naming rules within tables:**

- The outer test name is `TestSubject`. The subtest names listed in the W5 table above (`empty`, `absolute`, `dotdot prefix`, ...) are the `t.Run` names. So `TestValidatePath/dotdot_prefix` is what `go test -v` prints.
- The W5 individual test names (`TestValidatePath_DotDotPrefix`) are alternatives if the implementer prefers separate top-level tests over subtests. Both styles are acceptable; pick one per file and stay consistent. The reference in `go-basicauth` uses both depending on count: ≤ 5 cases → separate tests; > 5 → table.

**When NOT to table-drive:**

- Tests that have different setup or different assertions (e.g., two cases need an `httptest.Server`, three need a stub client). Different setup → different test function.
- The conformance suite. Each subtest there asserts a different contract clause; they share the factory but not the assertion logic. They are subtests of a single function but not a table.

## Spy and fake patterns

Tests often need to observe interactions without a full integration setup. Two patterns:

### Spy storage (W6)

For tests asserting that the agent calls `BeginTx`/`Commit`/`Rollback`/`Snapshot` correctly:

```go
type spyStorage struct {
    vfs.Storage // embed memory backend
    beginTx     int
    commit      int
    rollback    int
    snapshots   []spySnapshot
}

type spySnapshot struct {
    projectID   uuid.UUID
    turnID      uuid.UUID
    changedPaths []string
}

func (s *spyStorage) BeginTx(ctx context.Context, projectID uuid.UUID) (vfs.Tx, error) {
    s.beginTx++
    return s.Storage.(vfs.TxStorage).BeginTx(ctx, projectID)
}

// ... etc.
```

Tests then assert `spy.beginTx == 1 && spy.commit == 1 && spy.rollback == 0`.

### Fake LLM (W6, W8)

`StubClient` is a fake (returns canned data per scripted turn). For W8, where consolidation calls the LLM directly (not through the agent), `notes/` tests use `StubClient` directly. The same Go type covers both use cases.

## Test data lifecycle

How fixtures are created and updated.

### Recording new SSE transcripts (W3)

When OpenRouter changes the wire format or a new model surface needs coverage:

```sh
export OPENROUTER_API_KEY=sk-or-...
go run ./tools/record-sse \
    --model "anthropic/claude-sonnet-4-6" \
    --prompt "tell me a poem" \
    --output llm/openrouter/testdata/new_fixture.sse
```

This recording tool does not exist yet — it is **TBD with implementer**. Expectation: a small Go program under `tools/record-sse/main.go` (not part of the published library; under `cmd/` would mix layers). It calls the real API, writes the raw SSE bytes to a file, and prints the request payload to stderr for review.

The recorded fixture must be:

1. **Sanitized.** Strip the `id` field if it leaks any provider-internal identifier. Replace with `gen-redacted-...`. The SSE parser only verifies shape, not specific IDs, so redaction is safe.
2. **Trimmed.** Remove repeated identical text-delta frames if the test does not need 50 deltas of the same shape; 3 is enough for `simple_text.sse`.
3. **Annotated.** A leading comment line `# fixture: <purpose> <date> <model>` keeps provenance. SSE parsers must skip `:`-prefixed lines, so use that prefix.

### Updating fixtures when format changes

If a transcript test fails after an OpenRouter change:

1. Determine if it's a parser bug (our side) or a format change (their side). Smoke-run `examples/chat-oneshot` against the real API; if that fails identically, it's a format change.
2. Re-record the affected fixture.
3. Update the test if the new shape requires new assertions.
4. Note the change in `TODO.md` if it suggests a wider parser deficiency.

## Common test failure modes and debugging

Save yourself an hour. Skim before debugging.

| Symptom | Most likely cause | Next step |
|---------|-------------------|-----------|
| Test passes alone, fails in `go test ./...` | Goroutine leak between tests; test pollution. | Add `goleak.VerifyNone(t)` to the suspect test. Run with `-race` to catch shared-state aliasing. |
| `TestStream_*` flake on slow CI | Real-time timeout too tight. | Bump test timeout; check `retry.Policy.BaseDelay` is set to ms not seconds. |
| `TestSafety_DifferentProjects_Parallel` flake | Hardware/CI scheduler is single-core. | Mark `t.Parallel()` only on the inner sub-runs, not the outer test; assert "≤ 200ms" not "≤ 110ms". |
| `TestRun_TxStorage_*` panic on nil tx | `BeginTx` returned error and code didn't check. | Add nil check around `tx`; that's R-014. |
| `TestStream_*` panic with "send on closed channel" | Producer goroutine writes after `close(out)`. Bad cancel handling. | Producer must own the close. Caller never closes. Refactor with sync.Once or single goroutine ownership. |
| `TestNotebook_Consolidate_*` deletes wrong files | Path filter for `_notes/` regex is wrong. | Lock with explicit prefix check, not regex. |
| Lint fails on `gosec` G304 | `vfs/memory/storage.go` reads file path — false positive on a path that's project-internal. | Add `// #nosec G304 -- internal projectID-keyed map; not OS path` comment. Document liberally. |
| `golangci-lint` says "package not found" | `go mod tidy` not run. | `just tidy`. |
| CI passes but `just check` fails locally | `golangci-lint` version mismatch. | Pin version in `.golangci.yml` and CI; install matching version locally. |

## What aikido tests intentionally do not cover

To bound the test surface explicitly:

- **Provider-side correctness.** We assume OpenRouter returns syntactically valid SSE most of the time. We test our parser against canned inputs, not against fuzz inputs. (Fuzzing the SSE parser is a v1.x improvement; tagged in `TODO.md`.)
- **`invopop/jsonschema` correctness.** We test that `SchemaFromType[T]()` returns *something* valid for our specific use cases. We do not test invopop's behavior on every Go type combination.
- **Go runtime correctness.** `sync.Map` works. `context.WithTimeout` works. We trust these and write a single sanity test (`TestSafety_LoadOrStore_OneMutexPerProject`) for our usage of them.
- **Multi-instance behavior.** v1 is single-instance. Multi-instance is v3. Tests for two-process coordination do not exist in v1.
- **Long-lived stream behavior.** The library is built for streams up to a few minutes (matches `TurnTimeout=120s`). Hour-long streams are not tested. If a downstream consumer needs that, ADR-required.
- **Real-network reliability.** Smoke tests are best-effort sanity. We do not assert the real provider returns specific tokens; we assert event-shape only.
- **Performance.** v1 ships when correct; performance benchmarks land in v0.2+. The library is small enough that micro-optimization is premature.
- **Cross-platform.** v1 is built and tested on Linux (CI) and macOS (MaPa's machine). Windows is unsupported in v1; user reports may surface bugs that get fixed pending demand.

## Test name index — alphabetical

For `grep`-based discovery: scroll the table below or run `grep '^| Test' TEST-STRATEGY.md`. Names are unique across the project.

```
TestAgent_VFSTools_RegisterTwice         (W7)
TestAgent_VFSTools_WriteThenRead         (W7)
TestAssembleToolCalls_ChoiceDoneFlushes  (W3)
TestAssembleToolCalls_DropsIncompleteOnError (W3)
TestAssembleToolCalls_FragmentOrder      (W3)
TestCatalog_HasSeedEntries               (W1)
TestCollect_ContextCancelled             (W1)
TestCollect_PropagatesError              (W1)
TestCollect_StreamError                  (W1)
TestCollect_TextOnly                     (W1)
TestCollect_ToolCalls                    (W1)
TestCollect_Usage                        (W1)
TestDeleteFile_Happy                     (W7)
TestDeleteFile_NotFound                  (W7)
TestDeleteFile_PathInvalid               (W7)
TestDeleteFile_RespectsAllowedExtensions (W7)
TestErrors_AreSentinels                  (W1)
TestEventKind_String                     (W1, optional)
TestEvent_FieldShape                     (R-026 mitigation)
TestFindModel_DotsToHyphens              (W1)
TestFindModel_KnownID                    (W1)
TestFindModel_UnknownID                  (W1)
TestHashContent_ContentSensitive         (W5)
TestHashContent_Deterministic            (W5)
TestHashContent_Empty                    (W5)
TestHashContent_OrderInvariant           (W5)
TestHashContent_PathSensitive            (W5)
TestListFiles_Happy                      (W7)
TestListFiles_HidesDotByDefault          (W7)
TestListFiles_HidesUnderscoreByDefault   (W7)
TestListFiles_ShowsHidden_WhenDisabled   (W7)
TestMemory_ConcurrentReadsAreSafe        (W5)
TestMemory_ConcurrentWritesSerialize     (W5)
TestMemory_RunConformance                (W5)
TestMemory_SatisfiesTxStorage            (W5)
TestNewAgent_AppliesDefaults             (W6)
TestNewAgent_RequiresClient              (W6)
TestNewAgent_RequiresStorage             (W6)
TestNewClient_DefaultsApplied            (W2)
TestNewClient_OptionsOverride            (W2)
TestNewClient_RequiresAPIKey             (W2)
TestNewNotebook_AppliesDefaults          (W8)
TestNewNotebook_RequiresStorage          (W8)
TestNormalizeModelID_DotsToHyphens       (W2)
TestNormalizeModelID_RequestBodyApplies  (W2)
TestNotebook_Add_AppendsWithinSameTurn   (W8)
TestNotebook_Add_WritesToCorrectPath     (W8)
TestNotebook_Consolidate_Happy           (W8)
TestNotebook_Consolidate_InstructionsAppended (W8)
TestNotebook_Consolidate_InvalidTargetPath (W8)
TestNotebook_Consolidate_LLMError        (W8)
TestNotebook_Consolidate_NoExistingTarget (W8)
TestNotebook_Consolidate_NoNotes         (W8)
TestNotebook_Consolidate_PartialFailure  (W8)
TestNotebook_Consolidate_TargetIsNotePath (W8)
TestNotebook_List_ChronologicalOrder     (W8)
TestNotebook_List_Empty                  (W8)
TestNotebook_List_Preview                (W8)
TestNotebook_Read_Concatenates           (W8)
TestNotebook_Read_Empty                  (W8)
TestNotes_EndToEnd_StubAgent             (W8)
TestReadFile_Happy                       (W7)
TestReadFile_NotFound                    (W7)
TestReadFile_PathInvalid                 (W7)
TestReadFile_TooLarge                    (W7)
TestRegisterTools_AllThreeRegistered     (W8)
TestRegisterTools_DispatchAddNote        (W8)
TestRegisterTools_DispatchConsolidate    (W8)
TestRegisterTools_DispatchListNotes      (W8)
TestRegistry_Defs_PreservesOrder         (W4)
TestRegistry_DispatchInjectsEnv          (W4)
TestRegistry_DispatchInvalidJSON         (W4)
TestRegistry_DispatchPropagatesContext   (W4)
TestRegistry_DispatchPropagatesHandlerError (W4)
TestRegistry_DispatchUnknown             (W4)
TestRegistry_Has                         (W4)
TestRegistry_NewIsEmpty                  (W4)
TestRegistry_RegisterAndDispatch         (W4)
TestRegistry_RegisterDuplicate           (W4)
TestRunWithMessages_PrependsSystemPrompt (W6)
TestRunWithMessages_RejectsSystemInHistory (W6)
TestRun_AssistantHistoryAccumulated      (W6)
TestRun_CallerCancelsContext             (W6)
TestRun_HappyPath_MultipleToolsOneTurn   (W6)
TestRun_HappyPath_NoTools                (W6)
TestRun_HappyPath_OneToolThenStop        (W6)
TestRun_MaxTurnsExhausted                (W6)
TestRun_MidStreamError                   (W6)
TestRun_NonTxStorage_NoBeginTx           (W6)
TestRun_SystemPromptIncluded             (W6)
TestRun_ToolError_DoesNotAbort           (W6)
TestRun_ToolError_ResultEnteredIntoHistory (W6)
TestRun_TurnTimeout                      (W6)
TestRun_TxStorage_BeginTxErrorFallsBack  (W6, R-014)
TestRun_TxStorage_CommitOnNoErr          (W6)
TestRun_TxStorage_RollbackOnToolErr      (W6)
TestRun_TxStorage_SnapshotBeforeCommit   (W6)
TestRun_UnknownTool_DoesNotAbort         (W6)
TestRun_UsagePropagated                  (W6)
TestRun_UserMessageIncluded              (W6)
TestSafety_DifferentProjects_Parallel    (W6)
TestSafety_LoadOrStore_OneMutexPerProject (W6)
TestSafety_MutexReleased_OnError         (W6)
TestSafety_MutexReleased_OnPanic         (W6)
TestSafety_MutexReleased_OnTimeout       (W6)
TestSafety_SameProject_Serialize         (W6)
TestSchemaFromType_EmptyStruct           (W4)
TestSchemaFromType_FlatStruct            (W4)
TestSchemaFromType_RequiredViaTag        (W4)
TestSchemaFromType_UnsupportedTypePanics (W4)
TestSchema_Array                         (W4)
TestSchema_Enum                          (W4)
TestSchema_Integer_Number_Boolean        (W4)
TestSchema_NestedObject                  (W4)
TestSchema_Object_NoRequired             (W4)
TestSchema_Object_Required               (W4)
TestSchema_String                        (W4)
TestSearchFiles_CaseInsensitive          (W7)
TestSearchFiles_GlobFilter               (W7)
TestSearchFiles_GlobMalformed            (W7)
TestSearchFiles_HidesHiddenByDefault     (W7)
TestSearchFiles_NoMatches                (W7)
TestSearchFiles_Substring                (W7)
TestSSEParse_DoneTerminatesStream        (W3)
TestSSEParse_HandlesCRLF                 (W3)
TestSSEParse_IgnoresComments             (W3)
TestSSEParse_LineByLine                  (W3)
TestSSEParse_PartialReads                (W3)
TestStream_CacheControlTokens            (W3)
TestStream_ContextCancelled_ChannelCloses (W3)
TestStream_HTTPRefererPropagated         (W2)
TestStream_MidStreamError                (W3)
TestStream_MultiToolInterleaved          (W3)
TestStream_NoRetryAfterStreamStarted     (W3)
TestStream_NoRetryOn400                  (W3)
TestStream_NoRetryOn401                  (W3)
TestStream_NonStream_AuthFailure         (W2)
TestStream_NonStream_BadRequest          (W2)
TestStream_NonStream_TextOnly            (W2)
TestStream_NonStream_ToolCall            (W2)
TestStream_RequestBodyShape              (W2)
TestStream_RetryExhausted                (W3)
TestStream_RetryOn429                    (W3)
TestStream_RetryOn5xx                    (W3)
TestStream_SimpleText                    (W3)
TestStream_SingleToolCall                (W3)
TestStream_UsageEmittedBeforeEnd         (W3)
TestValidatePath_Absolute                (W5)
TestValidatePath_BackslashTraversal      (W5)
TestValidatePath_DotDotEnd               (W5)
TestValidatePath_DotDotMiddle            (W5)
TestValidatePath_DotDotPrefix            (W5)
TestValidatePath_DotPrefix_Allowed       (W5)
TestValidatePath_DoubleSlash             (W5)
TestValidatePath_Empty                   (W5)
TestValidatePath_Happy                   (W5)
TestValidatePath_LeadingSlash            (W5)
TestValidatePath_NullByte                (W5)
TestValidatePath_TooLong                 (W5)
TestValidatePath_UnderscorePrefix_Allowed (W5)
TestWriteFile_DefaultContentType         (W7)
TestWriteFile_Happy                      (W7)
TestWriteFile_PathInvalid                (W7)
TestWriteFile_RespectsAllowedExtensions  (W7)
TestWriteFile_RespectsMaxFileBytes       (W7)
```

Conformance subtests (run via `vfs.RunConformance`, named `TestMemory_RunConformance/<sub>`):

```
CreateProject_Unique
DeleteFile_NotFound
DeleteFile_RejectsInvalidPath
DeleteFile_RemovesEntry
HashState_ChangesOnDelete
HashState_ChangesOnWrite
HashState_OrderIndependent
HashState_Stable
ListFiles_AfterWrites
ListFiles_DeterministicOrder
ListFiles_Empty
ProjectExists_False
ProjectExists_True
ReadFile_NotFound
Snapshot_CapturesPreState
Snapshot_OnlyChangedPathsTracked
Snapshot_RestoreCreatedFile
Snapshot_RestoreDeletedFile
Snapshot_RestoreModifiedFile
Tx_BeginCommit_AppliesWrites
Tx_BeginRollback_DiscardsWrites
Tx_ContextCancelDuringTx
Tx_DoubleCommitErr
Tx_Isolated
Tx_RollbackThenCommitErr
WriteFile_OverwriteUpdatesMeta
WriteFile_RejectsInvalidPath
WriteFile_RejectsUnknownProject
WriteRead_PreservesContentType
WriteRead_RoundTrip
```

## Risk register cross-reference by category

For when you want to scan one category at a time. Each row points to the canonical risk-register entry above; severity (likelihood × impact) is repeated for at-a-glance triage.

### Provider-side risks (9)

| ID | Severity | One-line reminder |
|----|----------|-------------------|
| R-001 | M·M | OpenRouter SSE field drift |
| R-002 | M·M | finish_reason `tool_calls` vs `function_call` |
| R-003 | M·M | Invalid JSON args from model |
| R-004 | L·L | Usage emitted mid-stream |
| R-005 | H·M | Model ID dots vs hyphens |
| R-006 | M·L | Silent cache_control drop |
| R-007 | L·M | Retry-After date format |
| R-008 | L·H | Mid-stream replay |
| R-042 | L·L | Request body shape (omitempty) |

### Concurrency risks (5)

| ID | Severity | One-line reminder |
|----|----------|-------------------|
| R-009 | M·H | LoadOrStore race for first-time projectID |
| R-010 | M·H | Mutex leak on tool panic |
| R-011 | M·H | Channel close race |
| R-012 | M·M | Context cancellation lost in stream forwarding |
| R-013 | M·L | TurnTimeout double-cancel ambiguity |

### Security risks (7)

| ID | Severity | One-line reminder |
|----|----------|-------------------|
| R-019 | M·H | Path traversal `../etc/passwd` |
| R-020 | L·H | Null-byte path injection |
| R-021 | M·H | File-size DoS |
| R-022 | L·H | API key leakage in error logs |
| R-023 | M·H | Tool args bypass schema validation |
| R-024 | L·M | Content-type bypass for HTML/JS |
| R-025 | L·M | Tool name late-registration collision |

### API-shape risks (3)

| ID | Severity | One-line reminder |
|----|----------|-------------------|
| R-026 | M·L | Type bikeshedding before v0.1.0 |
| R-027 | L·H | Non-additive change post-v0.1.0 |
| R-028 | L·M | Required field added to Options |

### Test-determinism risks (4)

| ID | Severity | One-line reminder |
|----|----------|-------------------|
| R-029 | M·L | Retry timing flake |
| R-030 | M·L | TurnTimeout flake on cold CI |
| R-031 | L·M | Real network leak in CI |
| R-032 | M·M | Goroutine leak on test failure |

### Storage risks (6)

| ID | Severity | One-line reminder |
|----|----------|-------------------|
| R-014 | L·M | TxStorage capability detection bypass |
| R-015 | L·M | Snapshot capture parallelism |
| R-016 | M·M | Conformance gap on tx isolation |
| R-017 | L·H | Hash determinism across versions |
| R-018 | L·M | Path validation Unicode bypass |
| R-041 | L·L | Backend doesn't preserve content type |

### Scope-creep risks (4)

| ID | Severity | One-line reminder |
|----|----------|-------------------|
| R-037 | L·M | Image package in v1 |
| R-038 | L·M | CLI binary in v1 |
| R-039 | L·M | vfs/postgres reference impl in v1 |
| R-040 | L·M | agent.Locker in v1 |

### Other risks (4)

| ID | Severity | One-line reminder |
|----|----------|-------------------|
| R-033 | L·M | invopop/jsonschema breaking change |
| R-034 | L·L | Notes consolidation prompt drift |
| R-035 | M·M | Partial consolidation orphan notes |
| R-036 | L·M | turnID collision (process control) |

## Pre-merge checklist for each wave

When opening a PR for wave WN:

- [ ] All tests in "W{N} test scenarios" section above pass.
- [ ] `just check` is green.
- [ ] `just test-race` is green (run locally; CI also runs with `-race`).
- [ ] Every risk row tagged with WN has its mitigation test passing or has a one-line note in the PR explaining why the test is deferred.
- [ ] If new fixture under `testdata/`: a one-line `:`-prefixed comment at top of file documents purpose and date recorded.
- [ ] If schema change to public types: ADR added to `docs/DECISIONS.md` in the same PR.
- [ ] PR description references wave letter and exit criteria from `docs/v1/PLAN.md`.
- [ ] Coverage summary inspected (no enforcement, but a 10%+ regression is worth a note in the PR).

## Open questions checklist

The 15 open questions above need MaPa's resolution before W6 starts (where most of them surface). Track resolution in `TODO.md` or by inline edit of this file. **Do not begin W6 implementation with any of Q1, Q9, Q10, Q15 unresolved**, since they affect agent semantics that downstream tests depend on.

---

## Closing note for implementers

When you start a wave:

1. Read the wave's section in [PLAN.md](../PLAN.md).
2. Read this document's matching "Test scenarios per wave" section.
3. Open the package's test files; add stub `func TestXxx(t *testing.T) { t.Skip("TODO") }` for every named test in the section. Commit.
4. Implement. Replace skips one by one. Re-run `just check` after each.
5. When all tests in the section pass and `just check` is green, the wave is exit-criteria-met.

If a test name from this doc does not match what you ended up needing, do **not** rename silently. Either (a) update this doc in the same PR explaining why, or (b) keep the old name and put a comment linking to the relevant section here.

The risk register is a checklist. When you finish a wave, run through the risks tagged with that wave letter and confirm the "Test that proves mitigation works" passes. If any risk's test is missing or skipped, the wave is not done.

---
