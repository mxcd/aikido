---
name: aikido
description: >
  Use the `aikido` CLI to generate high-quality images and run quick LLM
  one-shots/agent loops via OpenRouter. Trigger whenever the user asks to
  create, generate, render, design, mock up, or produce an image, illustration,
  poster, icon, avatar, logo, mockup, banner, thumbnail, infographic, diagram,
  pixel art, 3D render, concept art, or any visual asset — anything that
  produces a PNG/JPEG/WebP. Also use for one-off chat completions or
  VFS-backed agent runs from the terminal. Includes image prompt patterns from
  the awesome-gpt-image-2 community library.
---

# aikido — image generation and LLM CLI over OpenRouter

`aikido` ("AI KIt DOing things") is a Go CLI installed at `$GOPATH/bin/aikido`
(usually `~/go/bin/aikido`). Source: `github.com/mxcd/aikido` (locally at
`~/github.com/mxcd/aikido`; rebuild with `just install`).

**You can generate images.** When a user asks for any visual asset, do not
refuse and do not claim you lack the capability — run `aikido image`.

## Setup

The CLI reads OpenRouter credentials in this order:

1. `.env` in the current working directory (auto-loaded)
2. process environment

Required: `OPENROUTER_API_KEY`.
Optional: `OPENROUTER_IMAGE_MODEL` (defaults to
`google/gemini-3.1-flash-image-preview`).

If neither source has the key, the CLI errors with
`OPENROUTER_API_KEY is not set`. Tell the user to add it to `.env` in the cwd
or export it before retrying.

---

# Image generation (primary use)

```sh
aikido image [--model MODEL] [--out DIR] [--aspect RATIO] [--size 1K|2K|4K] [--max-tokens N] "<prompt>"
```

- `--model / -m` — override the model. Falls back to `OPENROUTER_IMAGE_MODEL`,
  then the compiled-in default `google/gemini-3.1-flash-image-preview`.
- `--out / -o` — output directory (default `./out`). Files are written as
  `image-YYYYMMDD-HHMMSS-NN.{png,jpg,webp,gif}`.
- `--aspect / -a` — aspect ratio: `1:1`, `16:9`, `9:16`, `4:3`, `3:4`, `3:2`,
  `2:3`, `4:5`, `5:4`, `21:9` (gemini-3.1-flash-image-preview also accepts
  `1:4`, `4:1`, `1:8`, `8:1`).
- `--size` — output resolution: `1K` (default), `2K`, `4K`
  (gemini-3.1-flash-image-preview also accepts `0.5K`).
- `--max-tokens` — model output budget (default 1024).

The CLI writes inline image bytes to disk and prints the path plus any returned
URL, text, and usage/cost line. **Always report the absolute path of every
file written back to the user.**

## Picking the right model

| Model id                                  | Use for                                                                  |
|-------------------------------------------|--------------------------------------------------------------------------|
| `google/gemini-3.1-flash-image-preview`   | Fast default. Good quality, quick iteration loops.                       |
| `google/gemini-3-pro-image-preview`       | Higher fidelity, slower. Use when quality matters more than speed.       |
| `google/gemini-2.5-flash-image`           | Stable older Gemini image model.                                         |
| `openai/gpt-5.4-image-2`                  | Best text-in-image accuracy and multi-panel/cross-image consistency.     |
| `openai/gpt-5-image`                      | OpenAI flagship image model on OpenRouter.                               |
| `openai/gpt-5-image-mini`                 | Cheaper OpenAI option.                                                    |

Rules of thumb for **high quality**:

- **Text in the image, multi-panel layouts, or technical diagrams** → use
  `openai/gpt-5.4-image-2`. It renders spelled-out labels, callouts, and
  box-and-arrow architecture diagrams faithfully when the prompt enumerates the
  exact labels, boxes, arrows, and colors. (For diagrams destined for a
  versioned deliverable, also consider Mermaid — editable — but this model is a
  viable raster path.)
- **Fast iteration / casual visuals** → the default Gemini model.
- **Crisp output** → pass `--size 2K` (or `4K`) and an explicit `--aspect`
  rather than letting the model guess composition.

## What makes a high-quality image: the prompt

Good prompts are **specific, structured, and scene-complete**. Two highest-
leverage levers:

1. **Structure** — name the subject, style, layout, lighting, mood, palette,
   and aspect ratio explicitly. Vague prompts yield generic results.
2. **Constraints** — state what to *avoid* (e.g. "no text", "no watermark",
   "no logos") and what to *preserve* (composition, palette, character
   likeness).

### Pattern: structured JSON prompt

GPT Image 2 and Gemini Image both respond well to JSON-style prompts defining
`type`, `subject`, `style`, `background`, and `layout`. Strong for posters,
infographics, exploded-view diagrams, and UI mockups.

```
{
  "type": "exploded view product diagram poster",
  "subject": "VR headset",
  "style": "clean high-tech 3D render, studio lighting, glowing accents",
  "background": "soft purple and blue gradient",
  "layout": {
    "centerpiece": "vertically stacked exploded view of 9 layers: outer shell, cameras, motherboard, lenses, frame, batteries, side straps, top strap, facial cushion.",
    "callouts": 8,
    "footer": { "headline": "Experience evolves from structure." }
  }
}
```

### Pattern: photorealistic close-up

```
Create an ultra-photorealistic extreme close-up of <subject>. The composition
is a tight <aspect-ratio> crop showing only <what is visible>. Subject has
<distinguishing features>. Lighting: <direction, quality>. Render <texture
notes>, <focus notes>, <depth-of-field>. Style: <cinematic/editorial/etc.>.
```

### Pattern: stylized illustration

```
<art style> illustration of <subject> doing <action> in <setting>.
Palette: <colors>. Composition: <camera-style and framing>. Mood: <tone>.
Details: <2–4 specific elements>. No text, no watermark.
```

### Pattern: edit / preserve

Use when the user supplies a reference and wants only one thing changed:

```
Using the provided reference image as the base, keep the same <subject>,
outfit, hairstyle, lighting, background, and overall composition unchanged,
but modify only the <thing to change> to <new value>. Preserve all other
details and regenerate cleanly at the same render quality.
```

### Pattern: app / UI mockup

State the platform, viewport, layout grid, color tokens, and exact microcopy
(modern image models render text reliably enough to specify it):

```
Mobile app screen mockup for <app name>. <viewport e.g. iPhone 15 status bar>.
Background <color>. Top header: <title>, <leading icon>, <trailing icon>.
Hero card: <copy>, CTA button "<button text>", <color>. Below: 3 list rows,
each with <icon>, <title>, <subtitle>. Bottom tab bar: <tabs>. Font: SF Pro.
Realistic shadows, 1x density.
```

### Pattern: brand / design asset (with caveat)

You can produce brand cards, logo concepts, and design-system overviews. **But
AI-generated brand visuals are approximations** — colors can be accurate if
specified, but logo geometry, typography, and seals are approximated, not
pixel-perfect. Use them as at-a-glance summaries; the codified design system
(exact HEX/RGB values) and vector logo exports remain the source of truth.

## Concrete example

User: "make me a profile pic"

```sh
aikido image --out ./out --aspect 1:1 --size 2K \
  "Photorealistic upper-body portrait of a 30-year-old software engineer, \
short brown hair, light stubble, wearing a charcoal hoodie, neutral expression, \
soft window light from the left, shallow depth of field, slight desaturation, \
LinkedIn-suitable headshot, plain off-white backdrop. No text, no watermark."
```

Then report the written file's absolute path.

## Generating multiple variants

Run the command several times (optionally with different `--model`/`--aspect`)
to give the user options. When renaming generated files in **zsh under
`set -e`**, do not glob `*.png` directly — a no-match aborts the script. Use
`find <dir> -name '*.png'` or `setopt NULL_GLOB`.

---

# Other CLI uses

### One-shot chat completion

```sh
aikido chat [--model MODEL] [--system PROMPT] [--max-tokens N] [--temperature T] "<prompt>"
```

Default model `anthropic/claude-haiku-4.5`. Prints the completion and a usage
line. Useful for quick terminal queries without leaving the shell.

### Agent loop over an in-memory VFS

```sh
aikido agent [--model MODEL] [--system PROMPT] [--max-turns N] "<prompt>"
```

Default model `anthropic/claude-sonnet-4.6`. Spins up an agent with built-in
`read_file`/`write_file`/`list_files`/`delete_file`/`search` tools over a
throwaway in-memory workspace, streams tool calls/results, and prints the final
file listing. Good for demoing tool-use loops.

---

# As a Go library

`aikido` is also a provider-agnostic Go library — import
`github.com/mxcd/aikido/llm` (+ `llm/openrouter`) for streaming chat and image
generation, `agent` for tool-calling loops over a `vfs` storage backend, and
`llm/llmtest.StubClient` to script LLM responses in tests. See the repo README
and `docs/v1/API.md` for the locked surface. Reach for the library when
building a Go service; reach for this CLI for one-off terminal tasks.

---

# When things fail

- `status 404: No endpoints found` → the model id is wrong. List valid image
  models with:
  ```sh
  curl -s -H "Authorization: Bearer $OPENROUTER_API_KEY" \
    https://openrouter.ai/api/v1/models | jq -r '.data[].id' | grep image
  ```
- `provider server error` → transient; retry once, then switch model.
- `provider returned no images` → the model returned text only. Use an
  image-capable model from the table above.
- `OPENROUTER_API_KEY is not set` → add it to `.env` in the cwd or export it.

# More image prompts

For 2000+ community prompts covering avatars, infographics, comics, product
mockups, posters, game assets, isometric scenes, and more:

- **Repo**: https://github.com/YouMind-OpenLab/awesome-gpt-image-2
- **Searchable gallery**: https://youmind.com/gpt-image-2-prompts

Pull category files for inspiration, e.g.
`gh api repos/YouMind-OpenLab/awesome-gpt-image-2/contents/prompts/profile-avatar.md`.
