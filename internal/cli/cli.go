// Package cli wires the aikido command-line tool. The exported NewApp returns
// a *cli.Command (urfave/cli v3) parameterized by a ClientFactory so tests can
// substitute a stub llm.Client for the real OpenRouter client.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/openrouter"

	urfavecli "github.com/urfave/cli/v3"
)

// resolveWriter returns the writer the user configured on the root command.
//
// urfave/cli v3 sets cmd.Writer to os.Stdout by default on every subcommand,
// so we cannot distinguish "user-set" from "library default" there. Root() is
// where any caller-supplied Writer actually lives — prefer it.
func resolveWriter(cmd *urfavecli.Command) io.Writer {
	if cmd != nil {
		if root := cmd.Root(); root != nil && root.Writer != nil {
			return root.Writer
		}
		if cmd.Writer != nil {
			return cmd.Writer
		}
	}
	return os.Stdout
}

// Version is the CLI's reported version.
const Version = "v0.1.0-dev"

// ClientFactory builds the llm.Client used by every subcommand.
//
// Production wiring uses NewOpenRouterClient (reads OPENROUTER_API_KEY from
// the env after .env is loaded). Tests inject a stub.
type ClientFactory func() (llm.Client, error)

// NewApp returns the root *cli.Command tree.
func NewApp(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:                  "aikido",
		Usage:                 "AI KIt DOing things — chat one-shots and agent runs over an in-memory VFS",
		Version:               Version,
		EnableShellCompletion: true,
		Description: "aikido is a thin CLI over the aikido Go library. Subcommands run\n" +
			"completions or agent loops against OpenRouter; the API key is read from\n" +
			"OPENROUTER_API_KEY (a .env in the working directory is loaded as a\n" +
			"fallback before the env is consulted).",
		Commands: []*urfavecli.Command{
			chatCommand(factory),
			agentCommand(factory),
		},
	}
}

// NewOpenRouterClient is the default ClientFactory: it reads the API key from
// the environment and constructs an openrouter.Client.
func NewOpenRouterClient() (llm.Client, error) {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		return nil, errors.New("OPENROUTER_API_KEY is not set (export it or place it in .env)")
	}
	c, err := openrouter.NewClient(&openrouter.Options{
		APIKey:      apiKey,
		HTTPReferer: "https://github.com/mxcd/aikido",
		XTitle:      "aikido CLI",
	})
	if err != nil {
		return nil, fmt.Errorf("openrouter.NewClient: %w", err)
	}
	return c, nil
}
