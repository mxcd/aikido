1. **[MAJOR] Two existing CLI tests fail after the default flip.**  
   **Plan:** §8, “Existing tests keep passing.”  
   **Evidence:** [image_test.go:128](/Users/mapa/github.com/mxcd/aikido/internal/cli/image_test.go:128) and [image_test.go:159](/Users/mapa/github.com/mxcd/aikido/internal/cli/image_test.go:159) omit `-m`, script chat responses, and return `capturingClient`. That wrapper implements only `Stream` and `Complete` ([image_test.go:183](/Users/mapa/github.com/mxcd/aikido/internal/cli/image_test.go:183)); adding `GenerateImage` to its inner stub does not expose the capability. Both tests will fail the planned type assertion.  
   **Fix:** Explicitly select Gemini in these chat-specific tests. Add separate images-path aspect/size tests using an image-capable factory. Test the compiled default through `NewApp.Run`, where flag defaults actually apply.

2. **[MAJOR] Routing-list normalization and empty-env semantics are underspecified and incorrect in common cases.**  
   **Plan:** §§4, 6, 8.  
   **Evidence:** The rule trims only the model before comparing it with list entries. urfave preserves whitespace in string slices ([flag_slice_base.go:59](/Users/mapa/go/pkg/mod/github.com/urfave/cli/v3@v3.8.0/flag_slice_base.go:59)), so `OPENROUTER_IMAGES_API_MODELS='x/y, openai/gpt-image-2.5-flare'` sends flare to chat. An explicitly empty env value does **not** replace slice defaults ([flag_impl.go:131](/Users/mapa/go/pkg/mod/github.com/urfave/cli/v3@v3.8.0/flag_impl.go:131)). Nonempty env replacement works; `Value` itself is not the problem.  
   **Fix:** Normalize list entries, discard empty entries, and define unset versus explicitly empty behavior. If empty means “disable images routing,” handle it explicitly. Test comma-separated values with spaces, empty env, repeated flags, and flag-over-env precedence.

3. **[MAJOR] `--ref` corrupts valid filenames containing commas.**  
   **Plan:** §6, repeatable file-path `StringSliceFlag`.  
   **Evidence:** urfave splits each slice argument with `strings.Split`, defaulting to commas ([flag.go:214](/Users/mapa/go/pkg/mod/github.com/urfave/cli/v3@v3.8.0/flag.go:214)). Shell quoting does not prevent this: `--ref 'front, revised.png'` becomes two paths.  
   **Fix:** Preserve each `--ref` occurrence as one path. One option is disabling automatic slice splitting on the image command and explicitly splitting only model-list values. Add tests for comma-containing filenames and multiple references.

4. **[MAJOR] The smoke test can pass without exercising flare or the intended endpoint.**  
   **Plan:** §§8, 10.  
   **Evidence:** The flare subtest uses the default, but `OPENROUTER_IMAGE_MODEL` overrides it ([image.go:26](/Users/mapa/github.com/mxcd/aikido/internal/cli/image.go:26)); the new routing env can also redirect either model. A successful PNG therefore does not prove flare used `/images`. Additionally, command success alone does not prove Gemini wrote a file: URL-only responses return success while merely printing a URL ([image.go:123](/Users/mapa/github.com/mxcd/aikido/internal/cli/image.go:123)).  
   **Fix:** Isolate both routing-related env vars, create a fresh command and temporary output directory per subtest, record the outgoing model and URL path through a forwarding transport, and assert a decodable image file for both paths, specifically PNG for flare. Keep the build-tag and explicit env gates.

5. **[MAJOR] “Existing Complete tests stay green” does not protect the HTTP extraction’s compatibility contract.**  
   **Plan:** §§5, 6, 8.  
   **Evidence:** Current JSON test handlers accept any path and ignore request headers ([complete_test.go:22](/Users/mapa/github.com/mxcd/aikido/llm/openrouter/complete_test.go:22)). `Complete` currently overrides SSE `Accept`, reads and closes the body inside each retry attempt, and parses JSON after retries finish ([complete.go:38](/Users/mapa/github.com/mxcd/aikido/llm/openrouter/complete.go:38)). Existing tests could remain green with a wrong chat path or `Accept` header; they also do not pin read-failure replay or the retry boundary around error envelopes.  
   **Fix:** Add `Complete` regression tests for `/chat/completions`, JSON `Accept`, attribution/auth headers, identical request bodies across retries, read-failure retry/body closure, and a single request for HTTP-200 error envelopes. Keep JSON parsing outside `postJSON`’s retry loop and preserve `Stream`’s SSE headers. These checks protect behavior covered by the stability promise.

6. **[MINOR] The fixture plan covers only the new endpoint, despite requiring fixtures for both paths.**  
   **Plan:** §8.  
   **Evidence:** All five proposed fixture files are images-endpoint responses. Existing non-streaming image tests construct JSON inline ([complete_test.go:89](/Users/mapa/github.com/mxcd/aikido/llm/openrouter/complete_test.go:89)); the existing fixture helper is used for SSE transcripts ([client_test.go:21](/Users/mapa/github.com/mxcd/aikido/llm/openrouter/client_test.go:21)). A CLI stub test cannot verify chat serialization and parsing.  
   **Fix:** Add a Gemini non-streaming JSON fixture and replay it through the real OpenRouter client, asserting `/chat/completions`, `modalities`, `image_config`, reference content parts, and decoded image bytes.

7. **[MINOR] URL responses lose a supplied wire field.**  
   **Plan:** §6, `parseImagesResponse`: `url -> ImagePart{URL}`.  
   **Evidence:** The reference response explicitly includes both `url` and `media_type` ([arena.go:200](/Users/mapa/github.com/asolabs/model-arena/arena.go:200)). `ImagePart.ContentType` is intended to retain MIME information when known ([message.go:12](/Users/mapa/github.com/mxcd/aikido/llm/message.go:12)). The proposed URL branch discards it.  
   **Fix:** Return `ImagePart{URL: datum.URL, ContentType: datum.MediaType}` and assert both fields in the URL fixture test.

8. **[MINOR] Adding a README snippet leaves the documented Gemini path broken.**  
   **Plan:** §6, README image-generation section “gains” a `GenerateImage` snippet.  
   **Evidence:** The existing example uses streaming `llm.Collect` and omits `Modalities` ([README.md:42](/Users/mapa/github.com/mxcd/aikido/README.md:42)). The code explicitly documents that omitted modalities can prevent image generation ([client.go:14](/Users/mapa/github.com/mxcd/aikido/llm/client.go:14)), and that large image payloads break the streaming path ([client.go:101](/Users/mapa/github.com/mxcd/aikido/llm/client.go:101)). This is pre-existing documentation debt in the exact section being changed.  
   **Fix:** Replace the existing example with `Complete` plus `Modalities: []string{"image", "text"}`, alongside the new `GenerateImage` example. Handle errors in both examples.

9. **[MINOR] The shared helper contradicts the stated success-status contract.**  
   **Plan:** §3 says non-2xx responses are errors; §6 specifies non-200 classification.  
   **Evidence:** The working reference accepts all 2xx responses ([arena.go:263](/Users/mapa/github.com/asolabs/model-arena/arena.go:263)). Existing `Complete` accepts only 200 ([complete.go:51](/Users/mapa/github.com/mxcd/aikido/llm/openrouter/complete.go:51)); its classifier maps other success statuses to retryable `ErrServerError` ([retry.go:60](/Users/mapa/github.com/mxcd/aikido/llm/openrouter/retry.go:60)). Thus a valid JSON 201 response would be retried under the proposed helper.  
   **Fix:** Specify endpoint-specific success handling while preserving `Complete` behavior, or explicitly narrow the images contract to 200 and document the departure from the reference. Add a status-boundary test.

10. **[MINOR] One proposed function signature assigns the wrong type to `ctx`.**  
    **Plan:** §6, `generateViaImagesAPI(ctx, o imageOpts, ...)`.  
    **Evidence:** At [images-api-flare.md:175](/Users/mapa/github.com/mxcd/aikido/docs/plan/impl/images-api-flare.md:175), Go’s grouped-parameter syntax makes both `ctx` and `o` type `imageOpts`. The caller supplies `context.Context` ([image.go:76](/Users/mapa/github.com/mxcd/aikido/internal/cli/image.go:76)). Implemented literally, this cannot compile.  
    **Fix:** Write `generateViaImagesAPI(ctx context.Context, o imageOpts, refs []llm.ImagePart, client llm.Client)` and specify the complete `generateViaChat` signature too.

Verdict: **0 BLOCKER / 5 MAJOR / 5 MINOR.**