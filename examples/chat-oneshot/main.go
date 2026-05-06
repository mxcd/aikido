// chat-oneshot demonstrates a non-streaming completion via llm.Collect.
//
// Set OPENROUTER_API_KEY to run against the real provider:
//
//	go run ./examples/chat-oneshot
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/openrouter"
)

func main() {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENROUTER_API_KEY not set")
	}

	client, err := openrouter.NewClient(&openrouter.Options{
		APIKey:      apiKey,
		HTTPReferer: "https://github.com/mxcd/aikido",
		XTitle:      "aikido chat-oneshot example",
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	text, _, _, usage, err := llm.Collect(ctx, client, llm.Request{
		Model: "anthropic/claude-haiku-4.5",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "Be concise. Reply in one sentence."},
			{Role: llm.RoleUser, Content: "Give me one fun fact about the Go programming language."},
		},
		MaxTokens:   200,
		Temperature: llm.Float32(0.7),
	})
	if err != nil {
		log.Fatalf("Collect: %v", err)
	}
	fmt.Println("---")
	fmt.Println(text)
	fmt.Println("---")
	if usage != nil {
		fmt.Printf("tokens: prompt=%d completion=%d cost=$%.6f\n",
			usage.PromptTokens, usage.CompletionTokens, usage.CostUSD)
	}
}
