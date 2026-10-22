package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/openrouter"

	urfavecli "github.com/urfave/cli/v3"
)

// modelCatalog is the slice of *openrouter.Client the models command needs.
type modelCatalog interface {
	Models(ctx context.Context) ([]openrouter.Model, error)
	ModelEndpoints(ctx context.Context, id string) (openrouter.ModelEndpoints, error)
	VideoModels(ctx context.Context) ([]openrouter.VideoModel, error)
	DecisionModels(ctx context.Context) ([]openrouter.Model, error)
}

func modelsCommand(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:      "models",
		Usage:     "List OpenRouter models, or one model's providers with latency and throughput",
		ArgsUsage: "[filter | author/slug]",
		Description: "Without an argument, lists every model (id, context, $/M in and out).\n" +
			"A plain word filters that list by substring. An author/slug id prints the\n" +
			"providers serving that model: tag (the slug for --provider), quantization,\n" +
			"prices, uptime and p50 latency / throughput over the last 30 minutes.\n" +
			"--video lists the video models with the values `aikido video` accepts,\n" +
			"--decisions the models of the Decisions API (`aikido decide`).",
		Flags: []urfavecli.Flag{
			&urfavecli.BoolFlag{Name: "video", Usage: "List video-generation models (durations, resolutions, ratios, frames, audio)"},
			&urfavecli.BoolFlag{Name: "decisions", Usage: "List Decisions API models (Jev, ...)"},
		},
		Action: func(ctx context.Context, cmd *urfavecli.Command) error {
			client, err := factory()
			if err != nil {
				return err
			}
			catalog, ok := client.(modelCatalog)
			if !ok {
				return errors.New("models: the client has no model catalogue (OpenRouter only)")
			}
			arg := strings.TrimSpace(cmd.Args().First())
			out := resolveWriter(cmd)
			if cmd.Bool("video") {
				return printVideoModels(ctx, out, catalog, arg)
			}
			if cmd.Bool("decisions") {
				return printModels(ctx, out, catalog.DecisionModels, arg)
			}
			if strings.Contains(arg, "/") {
				return printEndpoints(ctx, out, catalog, arg)
			}
			return printModels(ctx, out, catalog.Models, arg)
		},
	}
}

func printModels(ctx context.Context, out io.Writer, list func(context.Context) ([]openrouter.Model, error), filter string) error {
	models, err := list(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tCONTEXT\t$/M IN\t$/M OUT")
	for _, m := range models {
		if filter != "" && !strings.Contains(strings.ToLower(m.ID), strings.ToLower(filter)) {
			continue
		}
		fmt.Fprintf(tw, "%s\t%d\t%.2f\t%.2f\n", m.ID, m.ContextLength, m.Pricing.Prompt.PerMillion(), m.Pricing.Completion.PerMillion())
	}
	return tw.Flush()
}

func printVideoModels(ctx context.Context, out io.Writer, c modelCatalog, filter string) error {
	models, err := c.VideoModels(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tDURATIONS\tRESOLUTIONS\tRATIOS\tFRAMES\tAUDIO")
	for _, m := range models {
		if filter != "" && !strings.Contains(strings.ToLower(m.ID), strings.ToLower(filter)) {
			continue
		}
		audio := "-"
		if m.GenerateAudio != nil {
			audio = fmt.Sprint(*m.GenerateAudio)
		}
		res := m.SupportedResolutions
		if len(res) == 0 {
			res = m.SupportedSizes
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", m.ID, durations(m.SupportedDurations),
			listOrDash(res), listOrDash(m.SupportedAspectRatios), listOrDash(m.SupportedFrameImages), audio)
	}
	return tw.Flush()
}

// durations prints a contiguous run as "3-15" and anything else as a list.
func durations(d []int) string {
	if len(d) == 0 {
		return "-"
	}
	d = slices.Sorted(slices.Values(d))
	if len(d) > 2 && d[len(d)-1]-d[0] == len(d)-1 {
		return fmt.Sprintf("%d-%d", d[0], d[len(d)-1])
	}
	s := make([]string, len(d))
	for i, v := range d {
		s[i] = fmt.Sprint(v)
	}
	return strings.Join(s, ",")
}

func listOrDash(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	return strings.Join(s, ",")
}

func printEndpoints(ctx context.Context, out io.Writer, c modelCatalog, id string) error {
	me, err := c.ModelEndpoints(ctx, id)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s (%s)\n\n", me.ID, me.Name)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tTAG\tQUANT\tCONTEXT\t$/M IN\t$/M OUT\tUPTIME 30m\tLATENCY p50\tTHROUGHPUT p50\tSTATUS")
	for _, e := range me.Endpoints {
		uptime, latency, throughput := "-", "-", "-"
		if e.UptimeLast30m != nil {
			uptime = fmt.Sprintf("%.1f%%", *e.UptimeLast30m)
		}
		if e.LatencyLast30m != nil {
			latency = fmt.Sprintf("%.0f ms", e.LatencyLast30m.P50)
		}
		if e.ThroughputLast30m != nil {
			throughput = fmt.Sprintf("%.0f tok/s", e.ThroughputLast30m.P50)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%.2f\t%.2f\t%s\t%s\t%s\t%d\n",
			e.ProviderName, e.Tag, e.Quantization, e.ContextLength,
			e.Pricing.Prompt.PerMillion(), e.Pricing.Completion.PerMillion(),
			uptime, latency, throughput, e.Status)
	}
	return tw.Flush()
}

// providerFlag is the --provider flag of chat and agent.
var providerFlag = &urfavecli.StringSliceFlag{
	Name:  "provider",
	Usage: "OpenRouter provider slug(s) to route to; one pins it (no fallbacks), several form a fallback chain (see `aikido models author/slug`)",
}

// withProviderFlag applies --provider to ctx for the OpenRouter client.
func withProviderFlag(ctx context.Context, cmd *urfavecli.Command) context.Context {
	order := cmd.StringSlice("provider")
	if len(order) == 0 {
		return ctx
	}
	fallbacks := len(order) > 1
	return openrouter.WithProvider(ctx, &openrouter.ProviderPrefs{Order: order, AllowFallbacks: &fallbacks})
}

// formatUsage renders one model call's usage line.
func formatUsage(u *llm.Usage) string {
	s := fmt.Sprintf("[usage] prompt=%d completion=%d cost=$%.6f", u.PromptTokens, u.CompletionTokens, u.CostUSD)
	if u.Provider != "" {
		s += " provider=" + u.Provider
	}
	if u.Duration > 0 {
		s += fmt.Sprintf(" latency=%dms duration=%dms", u.Latency.Milliseconds(), u.Duration.Milliseconds())
	}
	if u.TokensPerSecond > 0 {
		s += fmt.Sprintf(" throughput=%.1ftok/s", u.TokensPerSecond)
	}
	return s
}
