package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mxcd/aikido/llm"

	urfavecli "github.com/urfave/cli/v3"
)

// defaultImageModel is the compiled-in model. It is served only by
// OpenRouter's /api/v1/images endpoint, so it also heads defaultImagesAPIModels.
const defaultImageModel = "openai/gpt-image-2.5-flare"

// defaultImagesAPIModels are the OpenRouter models that exist only on
// /api/v1/images: they never answer on chat completions and are missing from a
// plain GET /models listing. OPENROUTER_IMAGES_API_MODELS replaces the list.
// Models that accept both endpoints (the Gemini image family,
// openai/gpt-5.4-image-2) must stay off it so they keep the chat path, which
// is the only one carrying a conversation.
var defaultImagesAPIModels = []string{
	"openai/gpt-image-2.5-flare",
	"openai/gpt-image-2.5-sunburst",
	"openai/gpt-image-2",
	"meta/muse-image",
}

// fallbackImageModel renders the frames the images endpoint cannot: ratios
// wider than 3:1 and the 0.5K tier.
const fallbackImageModel = "google/gemini-3.1-flash-image-preview"

// Images-endpoint frame limits (OpenAI gpt-image): sides multiples of 16, the
// longest edge at most 3840, at most 8,294,400 pixels, at most 3:1.
const (
	imagesMaxEdge   = 3840
	imagesMaxPixels = 3840 * 2160
	imagesMaxRatio  = 3
)

var errFrameUnsupported = errors.New("frame not supported on the images endpoint")

// maxReferenceBytes caps the raw bytes of all --ref images together. Base64
// inflates by a third and OpenRouter rejects more than 30 MB of downloaded
// content, so 20 MB raw is the last comfortable stop before a wasted round trip.
const maxReferenceBytes = 20 << 20

func imageCommand(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:      "image",
		Usage:     "Generate one or more images and write them to disk",
		ArgsUsage: "<prompt...>",
		// --ref takes file paths, which may legitimately contain commas.
		DisableSliceFlagSeparator: true,
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{
				Name:    "model",
				Aliases: []string{"m"},
				Value:   defaultImageModel,
				Sources: urfavecli.EnvVars("OPENROUTER_IMAGE_MODEL"),
				Usage:   "OpenRouter image model id (images-API models see --images-api-models)",
			},
			&urfavecli.StringFlag{
				Name:    "out",
				Aliases: []string{"o"},
				Value:   "out",
				Usage:   "Output directory for generated images",
			},
			&urfavecli.IntFlag{Name: "max-tokens", Value: 1024, Usage: "Max output tokens (chat-completions models only)"},
			&urfavecli.StringFlag{
				Name:    "aspect",
				Aliases: []string{"a"},
				Usage:   "Output aspect ratio W:H, e.g. 1:1, 4:5, 9:16, 16:9, 21:9; images-API models get the exact pixel frame (4:5 at 1K is 1024x1280), up to 3:1 (1:4/4:1/1:8/8:1 need gemini-3.1-flash-image-preview)",
			},
			&urfavecli.StringFlag{
				Name:  "size",
				Usage: "Output resolution: 1K (default), 2K, 4K (or 0.5K on gemini-3.1-flash-image-preview); with --aspect on images-API models the short edge is 1024/2048/4096, capped at 3840 long edge and 8.3 MP",
			},
			&urfavecli.StringSliceFlag{
				Name:  "ref",
				Usage: "Reference image file; repeat for several",
			},
			&urfavecli.StringFlag{
				Name:  "quality",
				Usage: "auto|low|medium|high|xhigh|max, images-API models only; max costs about 16x auto",
			},
			&urfavecli.StringFlag{
				Name:    "images-api-models",
				Value:   strings.Join(defaultImagesAPIModels, ","),
				Sources: urfavecli.EnvVars("OPENROUTER_IMAGES_API_MODELS"),
				Usage:   "Comma-separated model ids routed to /api/v1/images",
			},
		},
		Action: func(ctx context.Context, cmd *urfavecli.Command) error {
			client, err := factory()
			if err != nil {
				return err
			}
			return runImage(ctx, imageOpts{
				model:           cmd.String("model"),
				modelExplicit:   cmd.IsSet("model"),
				outDir:          cmd.String("out"),
				maxTokens:       int(cmd.Int("max-tokens")),
				aspectRatio:     cmd.String("aspect"),
				imageSize:       cmd.String("size"),
				quality:         cmd.String("quality"),
				refs:            cmd.StringSlice("ref"),
				imagesAPIModels: parseImagesAPIModels(cmd.String("images-api-models")),
				prompt:          strings.TrimSpace(strings.Join(cmd.Args().Slice(), " ")),
			}, resolveWriter(cmd), client)
		},
	}
}

type imageOpts struct {
	model           string
	modelExplicit   bool // -m or OPENROUTER_IMAGE_MODEL; no automatic fallback then
	pixelSize       string
	outDir          string
	prompt          string
	maxTokens       int
	aspectRatio     string
	imageSize       string
	quality         string
	refs            []string
	imagesAPIModels []string
}

func runImage(ctx context.Context, o imageOpts, out io.Writer, client llm.Client) error {
	if o.prompt == "" {
		return errors.New("missing prompt: aikido image [flags] <prompt...>")
	}
	if o.model == "" {
		o.model = defaultImageModel
	}
	if o.outDir == "" {
		o.outDir = "out"
	}

	refs, err := loadReferenceImages(o.refs)
	if err != nil {
		return err
	}

	var (
		text   string
		images []llm.ImagePart
		usage  *llm.Usage
	)
	imagesAPI := usesImagesAPI(o.model, o.imagesAPIModels)
	if imagesAPI && o.aspectRatio != "" && o.aspectRatio != "auto" {
		size, err := imagesAPISize(o.aspectRatio, o.imageSize)
		frame := "--aspect " + o.aspectRatio
		if o.imageSize != "" {
			frame += " --size " + o.imageSize
		}
		switch {
		case err == nil:
			o.pixelSize = size
		case !errors.Is(err, errFrameUnsupported):
			return err
		case o.modelExplicit:
			return fmt.Errorf("%s cannot render %s (%w); use -m %s", o.model, frame, err, fallbackImageModel)
		default:
			fmt.Fprintf(os.Stderr, "aikido: %s cannot render %s, using %s\n", o.model, frame, fallbackImageModel)
			o.model, o.quality, imagesAPI = fallbackImageModel, "", false
		}
	}
	if imagesAPI {
		images, usage, err = generateViaImagesAPI(ctx, o, refs, client)
	} else {
		text, images, usage, err = generateViaChat(ctx, o, refs, client)
	}
	if err != nil {
		return err
	}
	if len(images) == 0 {
		return errors.New("provider returned no images")
	}
	if err := os.MkdirAll(o.outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", o.outDir, err)
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	for i, img := range images {
		ext := imageExtFromContentType(img.ContentType)
		switch {
		case len(img.Data) > 0:
			path := filepath.Join(o.outDir, fmt.Sprintf("image-%s-%02d%s", stamp, i, ext))
			if err := os.WriteFile(path, img.Data, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			fmt.Fprintf(out, "wrote %s (%d bytes, %s)\n", path, len(img.Data), img.ContentType)
		case img.URL != "":
			fmt.Fprintf(out, "image %d available at: %s\n", i, img.URL)
		}
	}
	if t := strings.TrimSpace(text); t != "" {
		fmt.Fprintf(out, "\n%s\n", t)
	}
	if usage != nil {
		fmt.Fprintf(out, "\n[usage] prompt=%d completion=%d cost=$%.6f\n",
			usage.PromptTokens, usage.CompletionTokens, usage.CostUSD)
	}
	return nil
}

// generateViaImagesAPI renders through OpenRouter's /api/v1/images. The
// endpoint has no message list, so --max-tokens has nothing to apply to.
func generateViaImagesAPI(ctx context.Context, o imageOpts, refs []llm.ImagePart, client llm.Client) ([]llm.ImagePart, *llm.Usage, error) {
	gen, ok := client.(llm.ImageGenerator)
	if !ok {
		return nil, nil, errors.New("client does not support the images endpoint")
	}
	req := llm.ImageRequest{
		Model:       o.model,
		Prompt:      o.prompt,
		AspectRatio: o.aspectRatio,
		ImageSize:   o.imageSize,
		Quality:     o.quality,
		References:  refs,
	}
	// The endpoint refuses size mixed with aspect_ratio/resolution.
	if o.pixelSize != "" {
		req.Size, req.AspectRatio, req.ImageSize = o.pixelSize, "", ""
	}
	resp, err := gen.GenerateImage(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	return resp.Images, resp.Usage, nil
}

// imagesAPISize turns an aspect ratio and resolution tier into the exact
// WIDTHxHEIGHT the images endpoint takes. The aspect_ratio field alone rejects
// 4:5 and renders 9:16 and 3:4 both as 1024x1536; explicit pixels come back
// exact. The tier sets the short edge (1K = 1024), then the frame shrinks to
// the endpoint's edge and pixel caps, both sides on multiples of 16.
func imagesAPISize(aspect, tier string) (string, error) {
	ws, hs, ok := strings.Cut(strings.TrimSpace(aspect), ":")
	w, errW := strconv.Atoi(ws)
	h, errH := strconv.Atoi(hs)
	if !ok || errW != nil || errH != nil || w <= 0 || h <= 0 {
		return "", fmt.Errorf("invalid --aspect %q: want W:H such as 4:5", aspect)
	}
	var short float64
	switch strings.ToUpper(strings.TrimSpace(tier)) {
	case "", "1K":
		short = 1024
	case "2K":
		short = 2048
	case "4K":
		short = 4096
	default:
		return "", errFrameUnsupported
	}
	lo, hi := float64(min(w, h)), float64(max(w, h))
	if hi/lo > imagesMaxRatio {
		return "", errFrameUnsupported
	}
	long := short * hi / lo
	if long > imagesMaxEdge {
		short, long = short*imagesMaxEdge/long, imagesMaxEdge
	}
	if f := math.Sqrt(imagesMaxPixels / (short * long)); f < 1 {
		short, long = short*f, long*f
	}
	s, l := round16(short), round16(long)
	for l > imagesMaxEdge || s*l > imagesMaxPixels {
		if l > imagesMaxEdge {
			l -= 16
		} else {
			s -= 16
		}
	}
	if w < h {
		return fmt.Sprintf("%dx%d", s, l), nil
	}
	return fmt.Sprintf("%dx%d", l, s), nil
}

func round16(x float64) int { return int(math.Round(x/16)) * 16 }

// generateViaChat renders through chat completions with image output, the path
// every dual-endpoint model (Gemini, gpt-5.4-image-2) takes.
func generateViaChat(ctx context.Context, o imageOpts, refs []llm.ImagePart, client llm.Client) (string, []llm.ImagePart, *llm.Usage, error) {
	if o.quality != "" {
		return "", nil, nil, errors.New("--quality is only supported by images-API models (see --images-api-models)")
	}
	req := llm.Request{
		Model: o.model,
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: o.prompt, Images: refs},
		},
		MaxTokens:  o.maxTokens,
		Modalities: []string{"image", "text"},
	}
	if o.aspectRatio != "" || o.imageSize != "" {
		req.ImageConfig = &llm.ImageConfig{
			AspectRatio: o.aspectRatio,
			ImageSize:   o.imageSize,
		}
	}
	resp, err := client.Complete(ctx, req)
	if err != nil {
		return "", nil, nil, hintImagesAPI(err)
	}
	return resp.Text, resp.Images, resp.Usage, nil
}

// hintImagesAPI annotates the provider error a model raises when it is served
// only by the images endpoint. The id is missing from a plain /models listing,
// so the raw 404 reads like a typo.
func hintImagesAPI(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "/api/v1/images") && !strings.Contains(msg, "output modalities") {
		return err
	}
	return fmt.Errorf("%w (model may need the images endpoint: add it to OPENROUTER_IMAGES_API_MODELS)", err)
}

// parseImagesAPIModels splits the comma-separated flag value. An empty result
// falls back to the compiled-in list; a non-matching value such as "none"
// disables images-endpoint routing entirely.
func parseImagesAPIModels(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return defaultImagesAPIModels
	}
	return out
}

// usesImagesAPI reports whether model is served by /api/v1/images.
func usesImagesAPI(model string, list []string) bool {
	want := normalizeModelID(model)
	if want == "" {
		return false
	}
	for _, entry := range list {
		if normalizeModelID(entry) == want {
			return true
		}
	}
	return false
}

// normalizeModelID trims whitespace and drops OpenRouter's ":variant" suffix
// (":free", ":nitro", ...), which selects routing rather than a different model.
func normalizeModelID(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	return s
}

// loadReferenceImages reads --ref files into data: URIs. Both generation paths
// take the same []llm.ImagePart; only URL is read downstream.
func loadReferenceImages(paths []string) ([]llm.ImagePart, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	out := make([]llm.ImagePart, 0, len(paths))
	remaining := int64(maxReferenceBytes)
	for _, path := range paths {
		data, err := readReferenceFile(path, remaining)
		if err != nil {
			return nil, err
		}
		remaining -= int64(len(data))
		ct := mime.TypeByExtension(filepath.Ext(path))
		if i := strings.IndexByte(ct, ';'); i >= 0 {
			ct = strings.TrimSpace(ct[:i])
		}
		if ct == "" {
			ct = http.DetectContentType(data)
		}
		if !strings.HasPrefix(ct, "image/") {
			return nil, fmt.Errorf("reference %s is not an image (detected %s)", path, ct)
		}
		out = append(out, llm.ImagePart{
			URL:         "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(data),
			ContentType: ct,
		})
	}
	return out, nil
}

// readReferenceFile reads at most limit bytes from a regular file. The stat
// gates the read rather than trailing it: os.ReadFile on /dev/zero never
// returns, and a mistyped --ref movie.mp4 would otherwise pull gigabytes into
// memory before the non-image check rejects it. The LimitReader then catches a
// file that grew between the stat and the read.
func readReferenceFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read reference image %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat reference image %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("reference %s is not a regular file", path)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("reference images exceed %d bytes in total", maxReferenceBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read reference image %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("reference images exceed %d bytes in total", maxReferenceBytes)
	}
	return data, nil
}

func imageExtFromContentType(ct string) string {
	switch strings.ToLower(ct) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".bin"
	}
}
