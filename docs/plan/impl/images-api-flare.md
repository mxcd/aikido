# Plan: aikido image speaks OpenRouter /api/v1/images, flare becomes the default

Date 10.09.2026. Branch `feat/images-api-flare`. Names in this plan are contracts. Codex rounds: `images-api-flare-codex-round<n>.md`.

## 1. Goal

`aikido image` calls OpenRouter's `POST /api/v1/images` for models that exist only there
(`openai/gpt-image-2.5-flare` first), routes every other model through the existing
chat-completions path unchanged, and ships `openai/gpt-image-2.5-flare` as the compiled-in default.
The endpoint client lives in `llm/openrouter` so model-arena can drop its private copy later.

## 2. Scope and non-goals

In scope: library types and client method for the images endpoint, CLI routing, `--ref` reference
images on both paths, `--quality` on the images path, default flip, README, `--help`, bundled
SKILL.md, unit tests with fixtures for both paths, one live smoke behind a build tag and an env flag.
Non-goals (follow-ups): exact pixel `size` (`WxH`) flag, reference-image downscaling, catalog
auto-detection via `GET /models?output_modalities=image`, streaming for images, `examples/`, a version bump.

## 3. Wire contract of /api/v1/images (as model-arena uses it, verified live 09.09.2026)

Source: `~/github.com/asolabs/model-arena/arena.go` (`generateImagesAPI`, `openRouterPost`) and
DeepThought `patterns/openrouter-images-only-models-need-the-api-v1-images-endpoint`.
Request `POST {baseURL}/images` (production `https://openrouter.ai/api/v1/images`), headers as
chat completions (`Authorization: Bearer`, `Content-Type: application/json`, `HTTP-Referer`,
`X-Title`) plus `Accept: application/json`:

```json
{"model": "openai/gpt-image-2.5-flare", "prompt": "a red cube on white",
 "aspect_ratio": "9:16", "resolution": "1K", "quality": "auto",
 "input_references": [{"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,..."}}]}
```

- `aspect_ratio`, `resolution`, `quality`, `input_references` are optional and omitted when unset.
  No system role. `input_references[].image_url.url` takes data URLs and https URLs.
- `resolution` takes `1K|2K|4K`. The endpoint's `size` field wants `WxH` pixels for OpenAI and
  rejects `1K`, so the CLI's `--size` tier maps to `resolution`, never to `size`.
- `quality`: `auto|low|medium|high|xhigh|max` (default auto); `max` costs about 16x auto. OpenAI
  accepts aspect ratios 1:1, 3:2, 2:3, 4:3, 3:4, 16:9, 9:16, 21:9, auto; 4:5 is a 400.

Response 200 (model-arena accepts any 2xx; aikido keeps `Complete`'s 200-only rule on both
endpoints, so a non-200 success surfaces as a retried `ErrServerError`; recorded in ADR-029):

```json
{"created": 1757400000, "usage": {"cost": 0.0053},
 "data": [{"b64_json": "<base64 png>", "url": "", "media_type": "image/png"}]}
```

Either `b64_json` or `url` is set per datum; `media_type` may be absent. Errors: non-2xx with
`{"error": {"code": ..., "message": ...}}`, and 200 with a top-level `error` object; both handled
exactly like `Complete`.

## 4. Routing rule

`usesImagesAPI(model string, list []string) bool` is true when `normalizeModelID(model)` (trim
whitespace, drop a `:variant` suffix) equals `normalizeModelID(entry)` for any entry. True routes to
`ImageGenerator.GenerateImage`, false to `Client.Complete` with `modalities: [image, text]` (today's
behaviour). `--max-tokens` is ignored on the images path.

`list` = `parseImagesAPIModels(s string) []string`: split on commas, trim, drop empties; an empty
result returns `defaultImagesAPIModels` = `openai/gpt-image-2.5-flare`, `openai/gpt-image-2.5-sunburst`,
`openai/gpt-image-2`, `meta/muse-image` (in that order). `s` comes from the `--images-api-models`
StringFlag with `Sources: EnvVars("OPENROUTER_IMAGES_API_MODELS")`; flag beats env (urfave default).
A non-empty value replaces the default list; to disable images routing set a non-matching value such
as `none`. Gemini image models accept both endpoints and stay on chat completions.
`openai/gpt-5.4-image-2` is chat-capable and must not be listed.

## 5. Design

Capability interface, the pattern ADR-020 uses for `vfs`: the images endpoint is an optional
interface on the client, not a new `llm.Client` method. `openrouter.Client` and `llmtest.StubClient`
implement it; the CLI type-asserts and errors when the client cannot. `Complete`'s HTTP loop moves
into `postJSON` so both endpoints share retry, headers and error classification; JSON parsing stays
outside the retry loop. ADR-029 records this.

## 6. File-by-file changes

**`llm/image.go` (new)**

```go
type ImageRequest struct {
    Model, Prompt string
    AspectRatio   string      // values as ImageConfig.AspectRatio
    ImageSize     string      // "1K" | "2K" | "4K", values as ImageConfig.ImageSize; sent as resolution
    Quality       string      // "auto" | "low" | "medium" | "high" | "xhigh" | "max"; empty = provider default
    References    []ImagePart // URL must be set (data: or https); Data is ignored
}
type ImageResponse struct { Images []ImagePart; Usage *Usage }
// ImageGenerator is implemented by clients that speak a dedicated images endpoint.
type ImageGenerator interface {
    GenerateImage(ctx context.Context, req ImageRequest) (ImageResponse, error)
}
```

**`llm/openrouter/api_types.go`**: `imagesRequest{Model, Prompt; AspectRatio "aspect_ratio,omitempty";
Resolution "resolution,omitempty"; Quality "quality,omitempty"; InputReferences []apiImagePart "input_references,omitempty"}`,
`imagesResponse{Created int64; Data []imagesDatum; Usage *apiUsage; Error *apiError}`, `imagesDatum{B64JSON "b64_json"; URL "url"; MediaType "media_type"}`.

**`llm/openrouter/images.go` (new)**
- `func (c *Client) GenerateImage(ctx context.Context, req llm.ImageRequest) (llm.ImageResponse, error)`:
  `buildImagesBody` -> `c.postJSON(ctx, "/images", body)` -> `parseImagesResponse`; errors prefixed
  `openrouter: images: `. `var _ llm.ImageGenerator = (*Client)(nil)`.
- `func buildImagesBody(req llm.ImageRequest) ([]byte, error)`: empty Model or Prompt, or a
  reference with empty URL -> error wrapping `llm.ErrInvalidRequest`; ImageSize -> `resolution`.
- `func parseImagesResponse(body []byte) (llm.ImageResponse, error)`: top-level `error` ->
  `ErrServerError`, or `ErrContentFiltered` via `isContentFilterErrorEnvelope`; per datum `b64_json`
  decoded with `base64.StdEncoding` (failure -> `ErrServerError`), `ContentType` = `media_type` or
  `http.DetectContentType(data)` when absent; `url` -> `ImagePart{URL, ContentType: media_type}`;
  neither -> skipped. Empty `data` is not an error (caller decides). Usage via `toLLMUsage`.

**`llm/openrouter/client.go` and `complete.go`**: new `func (c *Client) postJSON(ctx
context.Context, path string, body []byte) ([]byte, error)`, the loop now inside `Complete`,
behaviour unchanged: per attempt a fresh `http.Request` from the same body bytes, `setHeaders` then
`Accept: application/json`, network error -> `ErrServerError`, non-200 -> `classifyHTTPError`,
`io.ReadAll` inside the attempt with read error -> `ErrServerError` (retried), body closed before
the next attempt. `Complete` becomes `postJSON(ctx, "/chat/completions", body)` +
`parseCompleteResponse`. `Stream` and its SSE headers are untouched.

**`llm/llmtest/stub_client.go`**: `type ImageScript struct { Response llm.ImageResponse; Err error }`;
`func (s *StubClient) ScriptImages(scripts ...ImageScript)` appends; `GenerateImage` pops in order,
records the request, `ErrStubExhausted` when none remain; `func (s *StubClient) ImageRequests()
[]llm.ImageRequest`; `var _ llm.ImageGenerator = (*StubClient)(nil)`.

**`internal/cli/image.go`**
- `const defaultImageModel = "openai/gpt-image-2.5-flare"`; `var defaultImagesAPIModels` (§4).
  The command sets `DisableSliceFlagSeparator: true` so `--ref` never splits on commas.
- Flags added: `--images-api-models` (StringFlag, env `OPENROUTER_IMAGES_API_MODELS`,
  `Value: strings.Join(defaultImagesAPIModels, ",")`, Usage `Comma-separated model ids routed to
  /api/v1/images`), `--ref` (StringSliceFlag, Usage `Reference image file; repeat for several`),
  `--quality` (StringFlag, Usage `auto|low|medium|high|xhigh|max, images-API models only; max costs
  about 16x auto`). Usage edits: `--model` `OpenRouter image model id (images-API models see
  --images-api-models)`; `--max-tokens` adds `(chat-completions models only)`; `--aspect` adds `; OpenAI images-API models reject 4:5`.
- `imageOpts` gains `imagesAPIModels []string`, `refs []string`, `quality string`. New helpers
  `func normalizeModelID(s string) string`, `func usesImagesAPI(model string, list []string) bool`,
  `func parseImagesAPIModels(s string) []string` (§4).
- `func loadReferenceImages(paths []string) ([]llm.ImagePart, error)`: `os.ReadFile`, MIME from
  `mime.TypeByExtension` falling back to `http.DetectContentType`, non-`image/*` -> error, builds
  `data:<mime>;base64,<payload>` into `ImagePart{URL, ContentType}`; raw total above
  `maxReferenceBytes = 20 << 20` -> error (OpenRouter rejects >30 MB downloaded content).
- `runImage`: validates prompt, model, outDir as today; `refs, err := loadReferenceImages(o.refs)`;
  `if usesImagesAPI(o.model, o.imagesAPIModels) { images, usage, err = generateViaImagesAPI(ctx, o, refs, client) }
  else { text, images, usage, err = generateViaChat(ctx, o, refs, client) }`; write loop and usage
  line unchanged (tokens print 0 on the images path).
- `func generateViaImagesAPI(ctx context.Context, o imageOpts, refs []llm.ImagePart, client llm.Client) ([]llm.ImagePart, *llm.Usage, error)`:
  type-asserts `llm.ImageGenerator`, else `errors.New("client does not support the images endpoint")`.
- `func generateViaChat(ctx context.Context, o imageOpts, refs []llm.ImagePart, client llm.Client) (string, []llm.ImagePart, *llm.Usage, error)`:
  today's request with `Messages[0].Images = refs`; `o.quality != ""` -> error `--quality is only
  supported by images-API models (see --images-api-models)`; a `Complete` error whose text contains
  `/api/v1/images` or `output modalities` is wrapped by `func hintImagesAPI(err error) error` with
  `model may need the images endpoint: add it to OPENROUTER_IMAGES_API_MODELS`.

**Docs**
- `README.md`: CLI section shows the new default, `--ref`, `--quality`, `OPENROUTER_IMAGES_API_MODELS`,
  Gemini keeps the chat path; the library "Image generation" example switches from `Collect` to
  `Complete` with `Modalities: []string{"image", "text"}` and error handling, plus a `GenerateImage` example.
- `internal/cli/skills/SKILL.md`: setup env vars (`OPENROUTER_IMAGE_MODEL` default flare,
  `OPENROUTER_IMAGES_API_MODELS`), flag list, model table rows for flare (default, product shots) and
  sunburst (precision tier, same price, slower), rule of thumb "fast iteration -> `-m google/gemini-3.1-flash-image-preview`".
  The "When things fail" 404 entry is replaced: a message containing `output modalities` or `use the
  /api/v1/images endpoint` is an endpoint mismatch (add the id to `OPENROUTER_IMAGES_API_MODELS`); any
  other 404 is an unknown id, verified against `GET /api/v1/models?output_modalities=image` (images-only models are missing from plain `/models`).
- `docs/v1/API.md`: additive `ImageRequest`, `ImageResponse`, `ImageGenerator` under `llm`;
  `GenerateImage` under `llm/openrouter`; `ImageScript`, `ScriptImages`, `GenerateImage`,
  `ImageRequests` under `llm/llmtest`; sync the `llm` and `openrouter` sections with what already
  ships (`Client.Complete`, `Response`, `Request.Modalities`, `Request.ImageConfig`).
- `docs/DECISIONS.md`: ADR-029 "Images endpoint as an optional `ImageGenerator` capability",
  10.09.2026, Accepted; records the 200-only rule and the shared `postJSON`.
- `--help` shows `--model, -m ... (default: "openai/gpt-image-2.5-flare") [$OPENROUTER_IMAGE_MODEL]`;
  env and `-m` still override.

## 7. Tests

Fixtures under `llm/openrouter/testdata/`, shapes recorded from the 09.09.2026 live run, payloads a
1x1 PNG: `images_b64.json`, `images_url.json` (url + media_type), `images_no_media_type.json`,
`images_error_envelope.json` (200 + error), `images_content_filter.json`, `images_400_aspect.json`
(4:5 rejection), `chat_image_gemini_nonstream.json` (chat path).

- `llm/openrouter/images_test.go`: `TestGenerateImage_RequestOnWire` (path `/images`, Accept json,
  auth and attribution headers, every field when set, optional fields absent when unset),
  `TestGenerateImage_DecodesB64`, `TestGenerateImage_URLKeepsContentType`, `TestGenerateImage_SniffsContentType`,
  `TestGenerateImage_ErrorEnvelopeNoRetry` (one hit), `TestGenerateImage_ContentFilterEnvelope`,
  `TestGenerateImage_400NoRetry` (ErrInvalidRequest, one hit), `TestGenerateImage_5xxRetryThenSuccess`
  (identical bodies), `TestGenerateImage_201IsError`, `TestBuildImagesBody_Validation`.
- `llm/openrouter/complete_test.go` additions: `TestComplete_WireContract` (path `/chat/completions`,
  Accept json, auth and attribution headers, identical body across a 503 retry),
  `TestComplete_ErrorEnvelopeNoRetry` (200 + error -> one request), `TestPostJSON_ReadFailureRetriesAndClosesBody`
  (fake `http.RoundTripper`: first 200 body fails mid-read, second returns valid JSON; asserts two
  attempts with identical request bytes, first body closed before attempt two, last body closed),
  `TestComplete_ChatImageFixture` (replays the Gemini fixture with `Modalities`, `ImageConfig` and a
  reference image; asserts `modalities`, `image_config`, the `image_url` content part on the wire and decoded bytes).
- `llm/llmtest/stub_client_test.go`: `TestStubClient_GenerateImage_ScriptsAndRecords`.
- `internal/cli/image_test.go`: `capturingClient` gains `GenerateImage` delegating to
  `inner.(llm.ImageGenerator)`; `TestImageCommand_ThreadsAspectAndSize` and
  `TestImageCommand_OmitsImageConfigWhenUnset` pass `-m google/gemini-3.1-flash-image-preview`;
  `TestImageCommand_EnvVarOverridesModel` stays as is (`custom/model-from-env` is not listed, so it
  takes chat). New: `TestNormalizeModelID`, `TestUsesImagesAPI` (exact, `:variant`, spaces, miss),
  `TestParseImagesAPIModels` (spaces, empties, empty string -> default, `none`),
  `TestImageCommand_DefaultModelUsesImagesAPI` (through `NewApp.Run` with no `-m`; both env vars
  really unset via `t.Setenv` then `os.Unsetenv`; stub records an ImageRequest for flare, no
  `Complete` call, PNG written), `TestImageCommand_EmptyEnvFallsBackToDefault` (`""` still lands on
  flare), `TestImageCommand_HelpShowsFlareDefault` (`image --help` through the root Writer),
  `TestImageCommand_ImagesAPIModelsEnv` (env `x/y, z/w` routes `x/y` to images and flare to chat),
  `TestImageCommand_FlagBeatsEnv`,
  `TestImageCommand_ThreadsQualityAndRefs` (images path: quality, aspect, size, references),
  `TestImageCommand_RefWithComma`, `TestRunImage_RefsOnChatPath` (`Messages[0].Images`),
  `TestRunImage_QualityRejectedOnChatPath`, `TestRunImage_ClientWithoutImageGenerator`,
  `TestLoadReferenceImages` (mime, sniff fallback, non-image, budget), `TestHintImagesAPI`.

Live smoke `internal/cli/image_smoke_test.go`, `//go:build smoke`, `TestSmoke_ImageGeneration`:
skips unless `AIKIDO_LIVE_SMOKE=1` and `OPENROUTER_API_KEY` are set. Builds its own
`openrouter.NewClient` (production base URL) with an `http.Client` whose `RoundTripper` records
request path and body, calls `runImage` directly with explicit `imageOpts` (no env, fresh
`t.TempDir()` per subtest). Subtests: `flare_images_api` (model `defaultImageModel`, list
`defaultImagesAPIModels`, prompt "a red cube on white": asserts path `/api/v1/images`, body model
flare, one file, `png.Decode` succeeds) and `gemini_chat_path` (`google/gemini-3.1-flash-image-preview`:
asserts path `/api/v1/chat/completions`, body model, one file, `image.Decode` succeeds). Run via
`just smoke`; about 0.05 USD; never in CI; the implementer runs it only when Riker says so.

## 8. Gates and acceptance

Gates: `just check` (tidy, vet, golangci-lint v2, test), `go test -race ./...`,
`go build ./examples/...`. No em dashes in new text. No `Co-Authored-By` lines.
Acceptance: `riker-env aso/openrouter -- aikido image --out /tmp/x "a red cube on white"` writes a
PNG with the new default through `/api/v1/images`; `aikido image -m google/gemini-3.1-flash-image-preview ...`
still works through chat completions. The two smoke subtests are these two commands.

## 9. Risks and open questions

1. **Model id list drift.** OpenRouter renames or delists without notice. Keep the four ids seen on
   09.09.2026, exact match, env override for the rest; the hinted 404 tells users what to set.
2. **Is `openai/gpt-image-2` really images-only?** DeepThought says so from the catalog; only flare
   and sunburst ran live. Keep it listed; a wrong entry is one env var away from fixed.
3. **Aspect semantics on OpenAI.** 4:5 rejected, 9:16 and 3:4 both return 1024x1536. Pass through
   now, documented in `--aspect` help; `--pixels WxH` -> `size` is a follow-up.
4. **Default flip changes cost and latency for every skill user.** Flare at auto: about 0.005 to 0.01 USD, 10 to 18 s per image. Accepted by the task; Gemini stays one flag away (SKILL.md).
5. **Large bodies, retry double-billing, reference payloads.** `postJSON` has no read cap and retries
   5xx and read failures, as `Complete` today; references get no downscaling. Accept; follow-ups.
6. **Public surface grows** (`ImageGenerator`, stub methods) under the v1 additive promise; ADR-029. **Muse needs the 18+ attestation** (403 -> `ErrAuth`); message passes through.

## Review checklist

- [ ] Request body matches §3 field for field; unset fields absent; `--size` maps to `resolution`.
- [ ] Response decoding covers b64, url with media_type, missing media_type, 200+error, non-2xx, content filter, 201.
- [ ] `postJSON`: path, Accept, headers, retry body identity, read-failure retry with body closure, one request on an error envelope, all pinned by tests.
- [ ] Routing: default list, env replaces list, spaces and empties tolerated, `:variant` stripped, flag beats env, Gemini stays on chat.
- [ ] `--ref` keeps commas and works on both paths; `--quality` only on the images path with a clear error otherwise.
- [ ] Default is flare in code, `--help` (tested), README, SKILL.md; env and `-m` still override; chat-path tests pass explicit Gemini.
- [ ] Stub client implements `ImageGenerator`; CLI errors cleanly when the client does not; every test in §7 exists and passes; smoke is tag plus env gated, env-isolated, asserts full endpoint path, model and a decodable image.
- [ ] ADR-029, API.md sync, README example fix and SKILL.md 404 rewrite present; no em dashes anywhere in the diff.

---

## Implementation notes (10.09.2026)

Built on `task/images-api-flare`, rebased onto `feat/images-api-flare` so the plan
travels with the code. Deviations from the plan above, all minor:

1. **`postJSON` lives in `client.go`.** §6 named "client.go and complete.go"; the
   helper is shared HTTP infrastructure, so it sits beside `setHeaders` and
   `buildBody` rather than in the chat-completions file. `complete.go` keeps
   `Complete` and `parseCompleteResponse` only.
2. **One extra openrouter test.** §7 folded "optional fields absent when unset"
   into `TestGenerateImage_RequestOnWire`; it is its own test,
   `TestGenerateImage_OmitsUnsetFields`, because the two need different requests.
3. **ADR-029's heading uses `-`, not the em dash** every other ADR heading uses.
   The review checklist bans em dashes anywhere in the diff, and that ban wins
   over matching the file's legacy heading style.
4. **The stub does not record an exhausted call.** §6 said `GenerateImage`
   "records the request"; it records only calls that consume a script, matching
   `Stream`, so `ImageRequests()` never reports a call the stub refused.
5. **`internal/cli.Version` still reads `v0.1.0-dev`.** It has been stale since
   v0.2.0 and §2 lists a version bump as a non-goal, so the release tag moves
   without it. Worth a separate fix.

Live acceptance (10.09.2026, about 0.09 USD total, key via the sanctioned
`CLAUDE_OPENROUTER_API_KEY`; `riker-env aso/openrouter` is not readable from a
`private/` task token):

- default model, no flags: 1254x1254 PNG through `/api/v1/images`, cost 0.006925 USD.
- `-m google/gemini-3.1-flash-image-preview`: 1408x768 JPEG through chat completions, cost 0.068220 USD.
- `--ref <png> --quality low --aspect 1:1`: the red cube came back blue with the
  composition intact, prompt tokens 1546, so the reference really rode along.
