| Round-1 finding | Resolved? | Assessment |
|---|---|---|
| 1. Default flip breaks two CLI tests | Yes | Both tests explicitly select Gemini; new test defects below. |
| 2. Routing normalization / empty env | Yes | StringFlag plus explicit parsing defines replacement and empty-value behavior. |
| 3. Commas corrupt reference paths | Yes | Command-level separator disabling preserves each occurrence. |
| 4. Smoke can exercise the wrong route | No | Environment isolation improves, but live path assertions are incorrect. |
| 5. Complete extraction lacks regression protection | No | Read-failure replay and body-closure coverage remain missing. |
| 6. No chat JSON fixture | Yes | Adds a real-client Gemini fixture test. |
| 7. URL response loses MIME type | Yes | Preserves and tests `media_type`. |
| 8. README’s Gemini example remains broken | Yes | Replaces it with `Complete`, modalities, and error handling. |
| 9. Success-status contract contradicts helper | Yes | Explicitly chooses and tests the 200-only departure. |
| 10. Incorrect `ctx` parameter type | Yes | Both helper signatures specify `context.Context`. |

1. **[MAJOR] The revised env-override test necessarily fails.**  
   **Plan:** §8, [plan:218](/Users/mapa/github.com/mxcd/aikido/docs/plan/impl/images-api-flare.md:218), adds `-m google/gemini-3.1-flash-image-preview` to `TestImageCommand_EnvVarOverridesModel`.  
   **Evidence:** The test sets `OPENROUTER_IMAGE_MODEL=custom/model-from-env` and asserts that exact model ([image_test.go:102](/Users/mapa/github.com/mxcd/aikido/internal/cli/image_test.go:102), [image_test.go:120](/Users/mapa/github.com/mxcd/aikido/internal/cli/image_test.go:120)). urfave skips environment lookup once a flag is set ([flag_impl.go:131](/Users/mapa/go/pkg/mod/github.com/urfave/cli/v3@v3.8.0/flag_impl.go:131)). The proposed flag therefore overrides the value being tested.  
   **Fix:** Keep this test without `-m`; isolate the routing-list environment so the custom model takes chat. Only the aspect/size and omitted-config tests should gain explicit Gemini flags.

2. **[MAJOR] Both live smoke path assertions reject successful requests.**  
   **Plan:** §8, [plan:233](/Users/mapa/github.com/mxcd/aikido/docs/plan/impl/images-api-flare.md:233), records outgoing request paths but expects `/images` and `/chat/completions`.  
   **Evidence:** The production base URL includes `/api/v1` ([client.go:20](/Users/mapa/github.com/mxcd/aikido/llm/openrouter/client.go:20)); `Complete` appends `/chat/completions` ([complete.go:39](/Users/mapa/github.com/mxcd/aikido/llm/openrouter/complete.go:39)). Thus the transport sees `/api/v1/images` and `/api/v1/chat/completions`. Bare endpoint paths work in fixture tests because their base URL is just `srv.URL` ([client_test.go:50](/Users/mapa/github.com/mxcd/aikido/llm/openrouter/client_test.go:50)).  
   **Fix:** Assert the full production paths and exact outgoing model in each smoke subtest. Keep bare paths only for fixtures configured without the API prefix.

3. **[MAJOR] Round-1’s read-failure compatibility check is still absent.**  
   **Plan:** §§6, 8, [plan:130](/Users/mapa/github.com/mxcd/aikido/docs/plan/impl/images-api-flare.md:130), [plan:210](/Users/mapa/github.com/mxcd/aikido/docs/plan/impl/images-api-flare.md:210).  
   **Evidence:** Current `Complete` reads inside the retry callback, maps read failures to `ErrServerError`, and closes that attempt’s body before retrying ([complete.go:55](/Users/mapa/github.com/mxcd/aikido/llm/openrouter/complete.go:55)). The revised tests cover HTTP 503 replay and error envelopes, but neither exercises a failing HTTP-200 response body. Moving `ReadAll` outside the retry callback or delaying closure could pass every listed regression test.  
   **Fix:** Add a fake-transport test returning a partially readable, failing body followed by valid JSON. Assert replayed request bytes, closure before the next attempt, and closure of the successful body.

4. **[MINOR] The “compiled default” test overrides the flag default before testing it.**  
   **Plan:** §8, [plan:222](/Users/mapa/github.com/mxcd/aikido/docs/plan/impl/images-api-flare.md:222), clears both environment variables using `t.Setenv(..., "")`.  
   **Evidence:** Unlike slice flags, a StringFlag accepts an explicitly empty environment value ([flag_impl.go:133](/Users/mapa/go/pkg/mod/github.com/urfave/cli/v3@v3.8.0/flag_impl.go:133)). This replaces `--model`’s configured value with `""`; `runImage` then supplies its own fallback ([image.go:80](/Users/mapa/github.com/mxcd/aikido/internal/cli/image.go:80)). The test can pass even if the flag’s default and help text are wrong.  
   **Fix:** Actually unset the variables, restoring their previous presence/value with cleanup. Test empty-env fallback separately, and assert the flare default in `image --help`.

5. **[MINOR] The API-documentation changes omit part of the new public surface.**  
   **Plan:** §6 Docs, [plan:184](/Users/mapa/github.com/mxcd/aikido/docs/plan/impl/images-api-flare.md:184).  
   **Evidence:** The plan introduces public `ImageScript` and `StubClient.GenerateImage` ([plan:139](/Users/mapa/github.com/mxcd/aikido/docs/plan/impl/images-api-flare.md:139)), but lists only `ScriptImages` and `ImageRequests` for documentation. The touched API reference also still documents a Stream-only `Client` ([API.md:174](/Users/mapa/github.com/mxcd/aikido/docs/v1/API.md:174)), whereas the actual interface requires `Complete` ([llm/client.go:93](/Users/mapa/github.com/mxcd/aikido/llm/client.go:93)).  
   **Fix:** Include `ImageScript`, the stub’s `GenerateImage` method, and synchronize the chat types/methods used by the revised examples: `Response`, `Complete`, `Modalities`, and `ImageConfig`.

6. **[MINOR] The skill update does not explicitly remove the misleading endpoint-failure diagnosis.**  
   **Plan:** §6 Docs, [plan:180](/Users/mapa/github.com/mxcd/aikido/docs/plan/impl/images-api-flare.md:180), specifies an entry for the new hint.  
   **Evidence:** The existing failure section says `404: No endpoints found` means the model ID is wrong and searches the plain `/models` catalog ([SKILL.md:224](/Users/mapa/github.com/mxcd/aikido/internal/cli/skills/SKILL.md:224)). That overlaps the endpoint-mismatch failure this feature addresses; the revised plan itself specifies the image-filtered catalog for verification ([plan:256](/Users/mapa/github.com/mxcd/aikido/docs/plan/impl/images-api-flare.md:256)). Adding another entry leaves conflicting instructions.  
   **Fix:** Explicitly replace the existing diagnosis, distinguish endpoint mismatch from an invalid ID, and use `/api/v1/models?output_modalities=image` for image-model discovery.

Verdict: **0 BLOCKER / 3 MAJOR / 3 MINOR.**