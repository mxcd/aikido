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

// defaultVideoModel is the cheapest model with first and last frame, audio
// and both 720p and 1080p: a 4 s 720p clip without audio is about 0.12 USD.
const defaultVideoModel = "google/veo-3.1-lite"

func videoCommand(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:      "video",
		Usage:     "Generate a video clip and write it to disk (takes minutes)",
		ArgsUsage: "<prompt...>",
		Description: "Submits a job to OpenRouter's /api/v1/videos, polls it and downloads the clip.\n" +
			"Each model accepts a closed list of durations, resolutions and ratios and\n" +
			"rejects anything else: see `aikido models --video [filter]`. Unset flags are\n" +
			"not sent, leaving the model default.",
		DisableSliceFlagSeparator: true,
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{
				Name:    "model",
				Aliases: []string{"m"},
				Value:   defaultVideoModel,
				Sources: urfavecli.EnvVars("OPENROUTER_VIDEO_MODEL"),
				Usage:   "OpenRouter video model id",
			},
			&urfavecli.StringFlag{Name: "out", Aliases: []string{"o"}, Value: "out", Usage: "Output directory"},
			&urfavecli.StringFlag{Name: "aspect", Aliases: []string{"a"}, Usage: "Aspect ratio W:H, e.g. 16:9, 9:16, 1:1"},
			&urfavecli.StringFlag{Name: "resolution", Aliases: []string{"r"}, Usage: "Resolution tier, e.g. 480p, 720p, 1080p, 4K"},
			&urfavecli.StringFlag{Name: "size", Usage: "Exact WIDTHxHEIGHT, instead of --aspect and --resolution"},
			&urfavecli.IntFlag{Name: "duration", Aliases: []string{"d"}, Usage: "Length in seconds"},
			&urfavecli.BoolFlag{Name: "audio", Usage: "Generate audio (--audio=false for a silent, cheaper clip); unset leaves the model default"},
			&urfavecli.IntFlag{Name: "seed", Usage: "Seed, where the provider honours one"},
			&urfavecli.StringFlag{Name: "first-frame", Usage: "Image file the clip starts from"},
			&urfavecli.StringFlag{Name: "last-frame", Usage: "Image file the clip ends on (models listing last_frame)"},
			&urfavecli.StringSliceFlag{Name: "ref", Usage: "Reference image file for style or identity; repeat for several; ignored when a frame is set"},
		},
		Action: func(ctx context.Context, cmd *urfavecli.Command) error {
			client, err := factory()
			if err != nil {
				return err
			}
			o := videoOpts{
				req: llm.VideoRequest{
					Model:       cmd.String("model"),
					Prompt:      strings.TrimSpace(strings.Join(cmd.Args().Slice(), " ")),
					AspectRatio: cmd.String("aspect"),
					Resolution:  cmd.String("resolution"),
					Size:        cmd.String("size"),
					Duration:    int(cmd.Int("duration")),
				},
				outDir:     cmd.String("out"),
				firstFrame: cmd.String("first-frame"),
				lastFrame:  cmd.String("last-frame"),
				refs:       cmd.StringSlice("ref"),
			}
			if cmd.IsSet("audio") {
				audio := cmd.Bool("audio")
				o.req.GenerateAudio = &audio
			}
			if cmd.IsSet("seed") {
				seed := int(cmd.Int("seed"))
				o.req.Seed = &seed
			}
			return runVideo(ctx, o, resolveWriter(cmd), client)
		},
	}
}

type videoOpts struct {
	req                   llm.VideoRequest
	outDir                string
	firstFrame, lastFrame string
	refs                  []string
}

func runVideo(ctx context.Context, o videoOpts, out io.Writer, client llm.Client) error {
	if o.req.Prompt == "" {
		return errors.New("missing prompt: aikido video [flags] <prompt...>")
	}
	gen, ok := client.(llm.VideoGenerator)
	if !ok {
		return errors.New("client does not support video generation")
	}
	var err error
	if o.req.FirstFrame, err = loadFrame(o.firstFrame); err != nil {
		return err
	}
	if o.req.LastFrame, err = loadFrame(o.lastFrame); err != nil {
		return err
	}
	if o.req.References, err = loadReferenceImages(o.refs); err != nil {
		return err
	}
	if o.outDir == "" {
		o.outDir = "out"
	}
	if err := os.MkdirAll(o.outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", o.outDir, err)
	}

	fmt.Fprintf(os.Stderr, "aikido: rendering with %s, this takes one to five minutes\n", o.req.Model)
	resp, err := gen.GenerateVideo(ctx, o.req)
	if err != nil {
		if resp.JobID != "" {
			return fmt.Errorf("%w (job %s)", err, resp.JobID)
		}
		return err
	}
	ext := ".mp4"
	if resp.ContentType != "video/mp4" {
		ext = ".bin"
	}
	path := filepath.Join(o.outDir, "video-"+time.Now().UTC().Format("20060102-150405")+ext)
	if err := os.WriteFile(path, resp.Video, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintf(out, "wrote %s (%d bytes, %s)\n", path, len(resp.Video), resp.ContentType)
	if resp.Usage != nil {
		fmt.Fprintf(out, "\n[usage] cost=$%.4f duration=%s job=%s\n", resp.Usage.CostUSD, resp.Usage.Duration.Round(time.Second), resp.JobID)
	}
	return nil
}

// loadFrame reads one --first-frame/--last-frame file; empty path is no frame.
func loadFrame(path string) (*llm.ImagePart, error) {
	if path == "" {
		return nil, nil
	}
	parts, err := loadReferenceImages([]string{path})
	if err != nil {
		return nil, err
	}
	return &parts[0], nil
}
