package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mxcd/aikido/llm"

	urfavecli "github.com/urfave/cli/v3"
)

func chatCommand(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:      "chat",
		Usage:     "Run a one-shot chat completion",
		ArgsUsage: "<prompt...>",
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{Name: "model", Aliases: []string{"m"}, Value: "anthropic/claude-haiku-4.5", Usage: "OpenRouter model id"},
			&urfavecli.StringFlag{Name: "system", Aliases: []string{"s"}, Usage: "System prompt"},
			&urfavecli.IntFlag{Name: "max-tokens", Value: 512, Usage: "Max output tokens"},
			&urfavecli.FloatFlag{Name: "temperature", Aliases: []string{"t"}, Value: -1, Usage: "Sampling temperature; <0 leaves provider default"},
		},
		Action: func(ctx context.Context, cmd *urfavecli.Command) error {
			client, err := factory()
			if err != nil {
				return err
			}
			return runChat(ctx, chatOpts{
				model:       cmd.String("model"),
				system:      cmd.String("system"),
				maxTokens:   int(cmd.Int("max-tokens")),
				temperature: cmd.Float("temperature"),
				prompt:      strings.TrimSpace(strings.Join(cmd.Args().Slice(), " ")),
			}, resolveWriter(cmd), client)
		},
	}
}

type chatOpts struct {
	model       string
	system      string
	prompt      string
	maxTokens   int
	temperature float64
}

func runChat(ctx context.Context, o chatOpts, out io.Writer, client llm.Client) error {
	if o.prompt == "" {
		return errors.New("missing prompt: aikido chat [flags] <prompt...>")
	}

	msgs := make([]llm.Message, 0, 2)
	if o.system != "" {
		msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: o.system})
	}
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: o.prompt})

	req := llm.Request{
		Model:     o.model,
		Messages:  msgs,
		MaxTokens: o.maxTokens,
	}
	if o.temperature >= 0 {
		req.Temperature = llm.Float32(float32(o.temperature))
	}

	text, _, _, usage, err := llm.Collect(ctx, client, req)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, text)
	if usage != nil {
		fmt.Fprintf(out, "\n[usage] prompt=%d completion=%d cost=$%.6f\n",
			usage.PromptTokens, usage.CompletionTokens, usage.CostUSD)
	}
	return nil
}
