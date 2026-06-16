package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mxcd/aikido/llm"

	urfavecli "github.com/urfave/cli/v3"
)

const defaultImageModel = "google/gemini-3.1-flash-image-preview"

func imageCommand(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:      "image",
		Usage:     "Generate one or more images and write them to disk",
		ArgsUsage: "<prompt...>",
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{
				Name:    "model",
				Aliases: []string{"m"},
				Value:   defaultImageModel,
				Sources: urfavecli.EnvVars("OPENROUTER_IMAGE_MODEL"),
				Usage:   "OpenRouter image-capable model id",
			},
			&urfavecli.StringFlag{
				Name:    "out",
				Aliases: []string{"o"},
				Value:   "out",
				Usage:   "Output directory for generated images",
			},
			&urfavecli.IntFlag{Name: "max-tokens", Value: 1024, Usage: "Max output tokens"},
			&urfavecli.StringFlag{
				Name:    "aspect",
				Aliases: []string{"a"},
				Usage:   "Output aspect ratio: 1:1, 16:9, 9:16, 4:3, 3:4, 3:2, 2:3, 4:5, 5:4, 21:9 (or 1:4/4:1/1:8/8:1 on gemini-3.1-flash-image-preview)",
			},
			&urfavecli.StringFlag{
				Name:  "size",
				Usage: "Output resolution: 1K (default), 2K, 4K (or 0.5K on gemini-3.1-flash-image-preview)",
			},
		},
		Action: func(ctx context.Context, cmd *urfavecli.Command) error {
			client, err := factory()
			if err != nil {
				return err
			}
			return runImage(ctx, imageOpts{
				model:       cmd.String("model"),
				outDir:      cmd.String("out"),
				maxTokens:   int(cmd.Int("max-tokens")),
				aspectRatio: cmd.String("aspect"),
				imageSize:   cmd.String("size"),
				prompt:      strings.TrimSpace(strings.Join(cmd.Args().Slice(), " ")),
			}, resolveWriter(cmd), client)
		},
	}
}

type imageOpts struct {
	model       string
	outDir      string
	prompt      string
	maxTokens   int
	aspectRatio string
	imageSize   string
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

	req := llm.Request{
		Model: o.model,
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: o.prompt},
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
		return err
	}
	text, images, usage := resp.Text, resp.Images, resp.Usage
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
