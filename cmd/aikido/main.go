// aikido is a CLI for the aikido library — chat one-shots and agent runs over
// an in-memory VFS, backed by OpenRouter.
//
// Per ADR-010 a CLI is a v2 deliverable; this binary is a v1.x convenience.
// All command logic lives in github.com/mxcd/aikido/internal/cli — main.go
// only wires .env loading, signal handling, and dispatch.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mxcd/aikido/internal/cli"
)

func main() {
	_ = cli.LoadDotEnv(".env")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app := cli.NewApp(cli.NewOpenRouterClient)
	if err := app.Run(ctx, os.Args); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "interrupted")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
