# Review: images-api-flare

Date 10.09.2026. Reviewer pass over branch `task/images-api-flare` at `b325b22` against
`docs/plan/impl/images-api-flare.md`. Codex round 1 (gpt-6-astra, high) ran against
`git diff 2748fd3...HEAD`; the task prompt's `main...HEAD` baseline was empty because local
`main` had already been reset onto this branch, so the run was restarted with the pre-task
commit as baseline. Codex output: `~/.riker/run/private/aikido/images-api-flare/codex-review.md`.

## State of the branch at review time

- `HEAD` = `b325b22` = local `main` = `origin/main` = tag `v0.2.6` (tagged 02:04:40, 16 s after
  the last commit, pushed). `~/go/bin/aikido` already reports the flare default. The release
  was cut before this review ran, so every fix below ships as a follow-up commit and `v0.2.7`.
- No UI in this task; no screenshots.

## Gates (run by the reviewer)

| Gate | Result |
|---|---|
| `just check` (tidy, vet, golangci-lint v2, test) | pass, 0 lint issues |
| `go test -race -count=1 ./...` | pass |
| `go build ./examples/...`, `go vet -tags=smoke ./internal/cli/` | pass |
| No em dashes in new text (plan §8) | FAIL: 2 in `llm/openrouter/images.go` comments |
| No `Co-Authored-By` | pass |

## Live acceptance (reviewer, ~0.075 USD via `CLAUDE_OPENROUTER_API_KEY`; `riker-env aso/openrouter` is not readable from a `private/` task token)

- `aikido image --out /tmp/x "a red cube on white"`: 1254x1254 PNG, cost 0.006925 USD, default
  model flare through `/api/v1/images`.
- `-m google/gemini-3.1-flash-image-preview`: 1408x768 JPEG, cost 0.068208 USD, chat completions.
- `--quality high` with the Gemini model: rejected with the planned error before any request.
- `image --help`: shows `(default: "openai/gpt-image-2.5-flare") [$OPENROUTER_IMAGE_MODEL]`,
  `--ref`, `--quality`, `--images-api-models` with the four-model default.

## Plan compliance, item by item

| Plan item | Status |
|---|---|
| §3 request body: model, prompt, aspect_ratio, resolution (not size), quality, input_references; unset fields absent | present, pinned by `TestGenerateImage_RequestOnWire` and `_OmitsUnsetFields` |
| §3 response: b64, url + media_type, missing media_type sniffed, 200 + error envelope, content filter, 4xx, 201 | present, one test each, fixtures under `testdata/` |
| §4 routing: `normalizeModelID`, `usesImagesAPI`, `parseImagesAPIModels`, default list of four, env replaces, `none` disables, `:variant` stripped, flag beats env | present, tested |
| §5 `ImageGenerator` optional capability, `postJSON` shared by `Complete` and `GenerateImage`, parsing outside the retry loop | present; `postJSON` lives in `client.go` (implementer deviation 1, sensible) |
| §6 `llm/image.go` types | present, matches the plan field for field |
| §6 `llmtest.StubClient`: `ImageScript`, `ScriptImages`, `GenerateImage`, `ImageRequests`, conformance var | present; exhausted call not recorded (deviation 4, consistent with `Stream`) |
| §6 CLI: `defaultImageModel`, `DisableSliceFlagSeparator`, `--ref`, `--quality`, `--images-api-models`, usage text edits, `loadReferenceImages` with 20 MiB budget, `generateViaImagesAPI`, `generateViaChat`, `hintImagesAPI` | present |
| §6 docs: README (default, flags, env, `Complete` example, `GenerateImage` example), SKILL.md (env, flags, model table, 404 rewrite), API.md additive surface + sync, ADR-029 | present; README `GenerateImage` snippet does not compile (finding 3) |
| §7 every listed test | all present by name; `TestPostJSON_ReadFailureRetriesAndClosesBody` is weaker than specified (finding 5) |
| §7 live smoke: build tag + `AIKIDO_LIVE_SMOKE=1`, env-isolated, asserts full path, model, decodable image | present, vets under the tag |
| §8 no em dashes in new text | FAIL (finding 2); deviation note 3 claims the ban was honored |

## Findings

| Severity | file:line | Finding | Fix | Resolution |
|---|---|---|---|---|
| MINOR | llm/openrouter/images.go:20, :75 | Two em dashes in new doc comments. Plan §8 and the review checklist ban them anywhere in the diff, MaPa's global rule bans them in all output, and implementation note 3 claims compliance. Objective gate failure. | Replace with ` - ` or a full stop; grep the file for U+2014 afterwards, it must return nothing. | Fixed in `llm/openrouter/images.go`: the first becomes a full stop, the second a colon. `grep -c $'\u2014'` over every file the branch adds now returns 0, and the branch diff carries no added line with one. The original check missed them because it grepped `git diff`, and both files were still untracked at that point. |
| MINOR | README.md:73 | `gen, ok := client.(llm.ImageGenerator)` does not compile: `client` in the quickstart is the concrete `*openrouter.Client` (line 30), and a type assertion needs an interface operand. The README example misleads library users at the exact moment they try the new feature. | Either `var client llm.Client = orClient` first, or call `client.GenerateImage(...)` directly and show the assertion only for a generic `llm.Client` variable. | Fixed. The concrete `*openrouter.Client` now calls `GenerateImage` directly, and a second snippet shows the type assertion in a function taking a plain `llm.Client`. Both snippets, plus the `Complete` one above them, were compiled against this branch in a scratch module before committing. |
| MINOR (Codex: MAJOR) | internal/cli/image.go:291 | `os.ReadFile` slurps each `--ref` file whole before the 20 MiB budget is checked. `--ref /dev/zero` never returns; `--ref movie.mp4` reads gigabytes before the non-image check rejects it. Downgraded from Codex's MAJOR: the path is chosen by the local user, no trust boundary is crossed. Still a footgun with a five-line fix. | `os.Stat` first, reject non-regular files and `size > remaining budget`; or read through `io.LimitReader(f, remaining+1)` and reject when more than `remaining` arrived. Add a table case to `TestLoadReferenceImages`. | Fixed. New `readReferenceFile(path, limit)` opens the file, rejects anything that is not a regular file, rejects `info.Size() > limit`, then reads through `io.LimitReader(f, limit+1)` and rechecks, so a file that grows between stat and read cannot exceed the budget either. The budget is now threaded as a remaining count across references rather than a running total. Three test cases added: a directory, the budget spanning several references, and `/dev/zero` under a 5 s deadline (`TestLoadReferenceImages_RejectsCharacterDeviceBeforeReading`), which hangs forever against the old code. |
| MINOR | llm/openrouter/images.go:28 | Doc comment says a content-filter rejection surfaces as `ErrContentFiltered` "whether it arrives as a non-200 or as a 200 carrying a top-level error envelope". `classifyHTTPError` maps by status only; there is no non-200 content-filter detection anywhere. The comment overclaims. | Reword: `ErrContentFiltered` for a 200 error envelope that `isContentFilterErrorEnvelope` recognises; non-200 follows `classifyHTTPError` by status. | Fixed. The comment now says a 200 error envelope that `isContentFilterErrorEnvelope` recognises maps to `ErrContentFiltered`, and that a non-200 is classified by status through `classifyHTTPError`, which has no content-filter case. |
| MINOR | llm/openrouter/complete_test.go:680 | `first.closed` is asserted after `Complete` returns, so the test proves the first body was closed eventually, not "before attempt two" as §7 specifies. A regression that closes bodies at the end of the retry loop would still pass. | In `respond`, when `attempt == 2`, record `first.closed` into a variable and assert that snapshot. | Fixed. `respond` snapshots `first.closed` into `firstClosedAtRetry` when attempt two starts, and the assertion reads the snapshot, so closing bodies only at the end of the retry loop now fails the test. |
| MINOR | process | The branch was merged (fast-forward), tagged `v0.2.6`, pushed and `just install`ed at 02:04, before this review started. Nothing wrong with the code because of it, but the review is post-hoc and any fix means `v0.2.7`. | Riker: gate the tag on the review verdict next time. This round: fix commit on `task/images-api-flare`, fast-forward `main`, tag `v0.2.7`, `just install`. | Acknowledged, implementer side. The release was cut in the same run because the task brief and MaPa's queued feedback both asked for it with no review gate named; gating the tag on the verdict is Riker's call and I agree with it. This round: fix commit on `task/images-api-flare`, ready for `main` fast-forward and `v0.2.7`. |

## Things checked and found correct

- `postJSON` byte-identical body across retries, `Accept: application/json` override, body closed on
  every exit path; `Complete` behaviour unchanged, `Stream` untouched.
- `--size` maps to `resolution`, never `size`; `--max-tokens` ignored on the images path; `--quality`
  rejected on the chat path before any request.
- Error wrapping keeps the `llm.Err*` sentinels through `hintImagesAPI` (`%w`).
- `--ref` files: MIME from extension with charset parameter stripped, sniff fallback, non-image rejected,
  data URL built correctly, commas in paths survive because of `DisableSliceFlagSeparator`.
- Env handling: unset, empty and set `OPENROUTER_IMAGE_MODEL` / `OPENROUTER_IMAGES_API_MODELS` all
  land where the plan says; tests really unset the vars, not just blank them.
- Stub client is mutex-guarded, conformance-asserted for both interfaces, snapshots on read.
- Smoke test isolates env, uses its own transport recorder, asserts endpoint path and decodable bytes.
- `internal/cli.Version` still says `v0.1.0-dev` (pre-existing, out of scope per plan §2, noted by the implementer).

## Verdict reasoning

Functionality is correct and verified live on both paths; all planned tests exist and pass under
`-race`. The reject is for an objective gate failure (plan §8, two em dashes, with a written claim
of compliance) plus a README code sample that does not compile. Both, and the three other MINORs,
fit one small follow-up commit, after which this becomes an APPROVE and Riker can cut `v0.2.7`.

Round 1 verdict: REJECT (superseded by round 2 below).


## Round 1 resolution (implementer, 10.09.2026)

All six findings addressed on `task/images-api-flare`. Gates re-run from a clean tree:
`just check` (tidy, vet, golangci-lint v2 with 0 issues, test), `go test -race -count=1 ./...`,
`go build ./examples/...`, `go vet -tags=smoke ./internal/cli/`, `gofmt -l .` empty.
Test count went from 32 new test functions to 34.

Two notes for the next round:

- The em dash gate needs a tree grep, not a diff grep. `git diff` says nothing about untracked
  files, which is exactly where both offenders lived when the implementer checked.
- README snippets are now compile-checked against the branch in a scratch module. That should
  probably be the standing habit for any Go snippet a doc gains.

## Round 2 (10.09.2026, reviewer, fix commit `a39f654`)

Every round-1 resolution verified against the code, not the description. Constraint from Riker
honored: no tag, no push of `main`, no `just install`; only this file is committed, on the task
branch.

| Round-1 finding | Verified | Evidence |
|---|---|---|
| Em dashes in `images.go` | fixed | `git diff 2748fd3...HEAD` has 0 added lines containing U+2014. The eight touched files that still contain one (`README.md`, `DECISIONS.md`, `API.md`, `SKILL.md`, `api_types.go`, `client.go`, `complete.go`, `complete_test.go`) carry it only on pre-existing lines, out of scope for this task. |
| README `GenerateImage` snippet | fixed | Concrete `*openrouter.Client` calls `GenerateImage` directly; the assertion moved into a function taking `llm.Client`. Both snippets type-check as written. |
| `--ref` unbounded read | fixed | `readReferenceFile`: `os.Open`, `Stat`, reject non-regular, reject `Size() > remaining`, `io.LimitReader(f, limit+1)` recheck. Budget threaded as `remaining` across references. New cases: directory, budget across two files, `/dev/zero` under a 5 s deadline. CLI check: `--ref /dev/zero` returns `reference /dev/zero is not a regular file` immediately. |
| `images.go:28` comment overclaim | fixed | Comment now states 200-envelope content filter via `isContentFilterErrorEnvelope`, non-200 by status with no content-filter case. Matches `classifyHTTPError`. |
| `complete_test.go` closure ordering | fixed | `firstClosedAtRetry` snapshotted inside `respond` at attempt two; assertion reads the snapshot. |
| Release before review | acknowledged | Riker owns the gate; next release is Riker's `v0.3.0` after this approval. |

Gates, round 2: `just check` pass (0 lint issues), `go test -race -count=1 ./...` 11 packages ok,
`go build ./examples/...` and `go vet -tags=smoke ./internal/cli/` pass. Live: flare with
`--ref` (8x8 red PNG), `--quality low`, `--aspect 1:1` produced a 1024x1024 PNG at 0.006 USD, so
the rewritten loader works end to end.

Agree with the implementer's two notes: the em dash gate is a tree grep over the branch's files,
and doc Go snippets get compiled before commit.

No new findings.

VERDICT: APPROVE
