// chatbot is an interactive Q&A loop over an embedded markdown corpus.
//
// The knowledge files under knowledge/ are compiled into the binary using
// the embed package. The agent only sees read-tools (read_file, list_files,
// search) — write_file and delete_file are not registered, so the corpus is
// immutable at runtime. Defense in depth: vfs/embedfs also rejects mutations.
//
//	go run ./examples/chatbot
//
// Set OPENROUTER_API_KEY in the environment first.
package main

import (
	"bufio"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mxcd/aikido/agent"
	"github.com/mxcd/aikido/llm/openrouter"
	"github.com/mxcd/aikido/tools"
	"github.com/mxcd/aikido/vfs/embedfs"
)

//go:embed knowledge/*.md
var knowledgeFS embed.FS

const systemPrompt = `You are a knowledge bot for the Go programming language. You have a small ` +
	`corpus of curated markdown files in your virtual workspace. Use list_files to enumerate them, ` +
	`search to find files mentioning a term, and read_file to read content before answering. ` +
	`Always cite the source file when answering. If the corpus does not contain an answer, say so ` +
	`plainly — do not speculate. Be concise.`

func main() {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENROUTER_API_KEY not set")
	}

	client, err := openrouter.NewClient(&openrouter.Options{
		APIKey:      apiKey,
		HTTPReferer: "https://github.com/mxcd/aikido",
		XTitle:      "aikido chatbot example",
	})
	if err != nil {
		log.Fatal(err)
	}

	knowledge, err := fs.Sub(knowledgeFS, "knowledge")
	if err != nil {
		log.Fatal(err)
	}
	storage := embedfs.NewStorage(knowledge)

	registry := tools.NewRegistry()
	if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{
		Storage:         storage,
		HideHiddenPaths: true,
		ReadOnly:        true, // chat-only: write_file and delete_file are not registered
	}); err != nil {
		log.Fatal(err)
	}

	session, err := agent.NewLocalSession(&agent.SessionOptions{
		ID:           "chatbot",
		Client:       client,
		Tools:        registry,
		Model:        "anthropic/claude-haiku-4.5",
		SystemPrompt: systemPrompt,
		MaxTurns:     10,
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	files, _ := storage.ListFiles(ctx)
	fmt.Println("aikido chatbot — ask anything about Go.")
	fmt.Println("Knowledge corpus:")
	for _, f := range files {
		fmt.Printf("  - %s (%d bytes)\n", f.Path, f.Size)
	}
	fmt.Println("Type 'exit' to quit.")

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for {
		fmt.Print("\n> ")
		if !sc.Scan() {
			break
		}
		prompt := strings.TrimSpace(sc.Text())
		if prompt == "" {
			continue
		}
		if prompt == "exit" || prompt == "quit" {
			break
		}

		turnCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		events, err := session.Run(turnCtx, prompt)
		if err != nil {
			cancel()
			fmt.Fprintln(os.Stderr, "error:", err)
			continue
		}
		for ev := range events {
			switch ev.Kind {
			case agent.EventText:
				fmt.Print(ev.Text)
			case agent.EventToolCall:
				fmt.Printf("\n  · %s(%s)\n", ev.ToolCall.Name, truncate(ev.ToolCall.Arguments, 120))
			case agent.EventError:
				fmt.Fprintf(os.Stderr, "\n[error] %v\n", ev.Err)
			case agent.EventEnd:
				if ev.EndReason != agent.EndReasonStop {
					fmt.Printf("\n[end: %s]\n", ev.EndReason)
				}
			}
		}
		cancel()
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
