package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mxcd/aikido/agent"
	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/tools"
	vfsmem "github.com/mxcd/aikido/vfs/memory"

	urfavecli "github.com/urfave/cli/v3"
)

const defaultAgentSystemPrompt = "You are a focused assistant working in a virtual " +
	"file system. Use your tools to read, write, list, delete, and search files. Files " +
	"are markdown by default. Be concise."

func agentCommand(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:      "agent",
		Usage:     "Run an agent loop with built-in VFS tools over an in-memory workspace",
		ArgsUsage: "<prompt...>",
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{Name: "model", Aliases: []string{"m"}, Value: "anthropic/claude-sonnet-4.6", Usage: "OpenRouter model id"},
			&urfavecli.StringFlag{Name: "system", Aliases: []string{"s"}, Value: defaultAgentSystemPrompt, Usage: "System prompt"},
			&urfavecli.StringFlag{Name: "session", Value: "cli-session", Usage: "Session id (also used as History key)"},
			&urfavecli.IntFlag{Name: "max-tokens", Value: 4096, Usage: "Max output tokens per turn"},
			&urfavecli.FloatFlag{Name: "temperature", Aliases: []string{"t"}, Value: -1, Usage: "Sampling temperature; <0 leaves provider default"},
			&urfavecli.IntFlag{Name: "max-turns", Value: 20, Usage: "Cap before EndReasonMaxTurns"},
			&urfavecli.BoolFlag{Name: "hide-hidden", Value: true, Usage: "Hide files whose path segments start with _ or ."},
		},
		Action: func(ctx context.Context, cmd *urfavecli.Command) error {
			client, err := factory()
			if err != nil {
				return err
			}
			return runAgent(ctx, agentOpts{
				model:       cmd.String("model"),
				system:      cmd.String("system"),
				sessionID:   cmd.String("session"),
				prompt:      strings.TrimSpace(strings.Join(cmd.Args().Slice(), " ")),
				maxTokens:   int(cmd.Int("max-tokens")),
				temperature: cmd.Float("temperature"),
				maxTurns:    int(cmd.Int("max-turns")),
				hideHidden:  cmd.Bool("hide-hidden"),
			}, resolveWriter(cmd), client)
		},
	}
}

type agentOpts struct {
	model       string
	system      string
	sessionID   string
	prompt      string
	maxTokens   int
	temperature float64
	maxTurns    int
	hideHidden  bool
}

func runAgent(ctx context.Context, o agentOpts, out io.Writer, client llm.Client) error {
	if o.prompt == "" {
		return errors.New("missing prompt: aikido agent [flags] <prompt...>")
	}

	storage := vfsmem.NewStorage()
	registry := tools.NewRegistry()
	if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{
		Storage:         storage,
		HideHiddenPaths: o.hideHidden,
	}); err != nil {
		return fmt.Errorf("RegisterVFSTools: %w", err)
	}

	opts := &agent.SessionOptions{
		ID:           o.sessionID,
		Client:       client,
		Tools:        registry,
		Model:        o.model,
		SystemPrompt: o.system,
		MaxTokens:    o.maxTokens,
		MaxTurns:     o.maxTurns,
	}
	if o.temperature >= 0 {
		opts.Temperature = llm.Float32(float32(o.temperature))
	}
	session, err := agent.NewLocalSession(opts)
	if err != nil {
		return err
	}

	events, err := session.Run(ctx, o.prompt)
	if err != nil {
		return err
	}
	for ev := range events {
		switch ev.Kind {
		case agent.EventText:
			fmt.Fprint(out, ev.Text)
		case agent.EventToolCall:
			fmt.Fprintf(out, "\n[tool-call] %s(%s)\n", ev.ToolCall.Name, truncateForLog(ev.ToolCall.Arguments, 200))
		case agent.EventToolResult:
			ok := "ok"
			if !ev.ToolResult.OK {
				ok = "fail: " + ev.ToolResult.Error
			}
			fmt.Fprintf(out, "[tool-result] %s %s\n", ev.ToolResult.Name, ok)
		case agent.EventUsage:
			fmt.Fprintf(out, "\n[usage] prompt=%d completion=%d cost=$%.6f\n",
				ev.Usage.PromptTokens, ev.Usage.CompletionTokens, ev.Usage.CostUSD)
		case agent.EventError:
			fmt.Fprintf(out, "\n[error] %v\n", ev.Err)
		case agent.EventEnd:
			fmt.Fprintf(out, "\n[end] %s\n", ev.EndReason)
		}
	}

	files, _ := storage.ListFiles(ctx)
	if len(files) > 0 {
		fmt.Fprintln(out, "\n--- final VFS state ---")
		for _, f := range files {
			fmt.Fprintf(out, "- %s (%d bytes, %s)\n", f.Path, f.Size, f.ContentType)
		}
	}
	return nil
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}
