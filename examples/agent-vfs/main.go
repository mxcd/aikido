// agent-vfs runs a session over an in-memory VFS with the built-in tools.
//
// The model is asked to write a notes file and then read it back. Set
// OPENROUTER_API_KEY to run against the real provider:
//
//	go run ./examples/agent-vfs
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/mxcd/aikido/agent"
	"github.com/mxcd/aikido/llm/openrouter"
	"github.com/mxcd/aikido/tools"
	vfsmem "github.com/mxcd/aikido/vfs/memory"
)

func main() {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENROUTER_API_KEY not set")
	}

	client, err := openrouter.NewClient(&openrouter.Options{
		APIKey:      apiKey,
		HTTPReferer: "https://github.com/mxcd/aikido",
		XTitle:      "aikido agent-vfs example",
	})
	if err != nil {
		log.Fatal(err)
	}

	storage := vfsmem.NewStorage()
	registry := tools.NewRegistry()
	if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{
		Storage:         storage,
		HideHiddenPaths: true,
	}); err != nil {
		log.Fatal(err)
	}

	session, err := agent.NewLocalSession(&agent.SessionOptions{
		ID:     "demo",
		Client: client,
		Tools:  registry,
		Model:  "anthropic/claude-sonnet-4.6",
		SystemPrompt: "You are a research assistant working in a virtual file system. " +
			"You can read, write, list, delete, and search files using your tools. " +
			"Files end in .md and use markdown formatting.",
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	prompt := "Create a file called facts.md with three short facts about the Go programming language, " +
		"one per bullet point. Then read the file back and tell me what you wrote."

	events, err := session.Run(ctx, prompt)
	if err != nil {
		log.Fatal(err)
	}

	for ev := range events {
		switch ev.Kind {
		case agent.EventText:
			fmt.Print(ev.Text)
		case agent.EventThinking:
			// silenced; uncomment to surface reasoning fragments
			// fmt.Printf("[thinking] %s\n", ev.Text)
		case agent.EventToolCall:
			fmt.Printf("\n[tool-call] %s(%s)\n", ev.ToolCall.Name, ev.ToolCall.Arguments)
		case agent.EventToolResult:
			ok := "✓"
			if !ev.ToolResult.OK {
				ok = "✗"
			}
			fmt.Printf("[tool-result] %s %s\n", ok, ev.ToolResult.Name)
		case agent.EventUsage:
			fmt.Printf("\n[usage] prompt=%d completion=%d cost=$%.6f\n",
				ev.Usage.PromptTokens, ev.Usage.CompletionTokens, ev.Usage.CostUSD)
		case agent.EventError:
			fmt.Printf("\n[error] %v\n", ev.Err)
		case agent.EventEnd:
			fmt.Printf("\n[end] %s\n", ev.EndReason)
		}
	}

	fmt.Println("\n--- final VFS state ---")
	files, _ := storage.ListFiles(ctx)
	for _, f := range files {
		fmt.Printf("- %s (%d bytes)\n", f.Path, f.Size)
	}
}
