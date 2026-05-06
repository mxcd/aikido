// image-generation demonstrates calling an OpenRouter image-capable model and
// writing the returned PNG/JPEG bytes to disk.
//
// Set OPENROUTER_API_KEY to run against the real provider:
//
//	go run ./examples/image-generation
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
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
		XTitle:      "aikido image-generation example",
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	_, _, images, usage, err := llm.Collect(ctx, client, llm.Request{
		Model: "google/gemini-2.5-flash-image-preview",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "Generate a small pixel-art icon of a fox sitting in tall grass."},
		},
		MaxTokens: 1024,
	})
	if err != nil {
		log.Fatalf("Collect: %v", err)
	}
	if len(images) == 0 {
		log.Fatal("provider returned no images")
	}

	outDir := "out"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatal(err)
	}
	for i, img := range images {
		ext := extFromContentType(img.ContentType)
		switch {
		case len(img.Data) > 0:
			path := filepath.Join(outDir, fmt.Sprintf("image-%02d%s", i, ext))
			if err := os.WriteFile(path, img.Data, 0o644); err != nil {
				log.Fatal(err)
			}
			fmt.Printf("wrote %s (%d bytes, %s)\n", path, len(img.Data), img.ContentType)
		case img.URL != "":
			fmt.Printf("image %d available at: %s\n", i, img.URL)
		}
	}
	if usage != nil {
		fmt.Printf("tokens: prompt=%d completion=%d cost=$%.6f\n",
			usage.PromptTokens, usage.CompletionTokens, usage.CostUSD)
	}
}

func extFromContentType(ct string) string {
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
