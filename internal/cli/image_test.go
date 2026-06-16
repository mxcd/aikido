package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/llmtest"
)

var fakePNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'x', 'x'}

func TestRunImage_WritesInlineImageToDisk(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: fakePNG}},
		{Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 5, CompletionTokens: 0, CostUSD: 0.0002}},
		{Kind: llm.EventEnd},
	}})
	dir := t.TempDir()
	var out bytes.Buffer
	err := runImage(context.Background(), imageOpts{
		model:     "google/gemini-3.1-flash-image-preview",
		outDir:    dir,
		prompt:    "a fox",
		maxTokens: 256,
	}, &out, stub)
	if err != nil {
		t.Fatalf("runImage: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read tmp dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 file, got %d: %+v", len(entries), entries)
	}
	if !strings.HasSuffix(entries[0].Name(), ".png") {
		t.Errorf("unexpected extension: %s", entries[0].Name())
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read image: %v", err)
	}
	if !bytes.Equal(data, fakePNG) {
		t.Errorf("file contents mismatch")
	}
	if !strings.Contains(out.String(), "[usage]") {
		t.Errorf("missing usage line: %q", out.String())
	}

	reqs := stub.Requests()
	if len(reqs) != 1 || reqs[0].Model != "google/gemini-3.1-flash-image-preview" {
		t.Errorf("model not threaded: %+v", reqs)
	}
}

func TestRunImage_PrintsURLWhenNoInlineData(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventImage, Image: &llm.ImagePart{URL: "https://example.com/x.png", ContentType: "image/png"}},
		{Kind: llm.EventEnd},
	}})
	dir := t.TempDir()
	var out bytes.Buffer
	if err := runImage(context.Background(), imageOpts{model: "m", outDir: dir, prompt: "hi"}, &out, stub); err != nil {
		t.Fatalf("runImage: %v", err)
	}
	if !strings.Contains(out.String(), "https://example.com/x.png") {
		t.Errorf("missing URL line: %q", out.String())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("unexpected files for URL-only image: %+v", entries)
	}
}

func TestRunImage_RequiresPrompt(t *testing.T) {
	stub := llmtest.NewStubClient()
	var out bytes.Buffer
	if err := runImage(context.Background(), imageOpts{model: "m", outDir: t.TempDir()}, &out, stub); err == nil {
		t.Error("expected error for missing prompt")
	}
}

func TestRunImage_ErrorsWhenNoImages(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "no images here"},
		{Kind: llm.EventEnd},
	}})
	var out bytes.Buffer
	err := runImage(context.Background(), imageOpts{model: "m", outDir: t.TempDir(), prompt: "hi"}, &out, stub)
	if err == nil || !strings.Contains(err.Error(), "no images") {
		t.Errorf("expected 'no images' error, got %v", err)
	}
}

func TestImageCommand_EnvVarOverridesModel(t *testing.T) {
	t.Setenv("OPENROUTER_IMAGE_MODEL", "custom/model-from-env")

	captured := make(chan llm.Request, 1)
	factory := func() (llm.Client, error) {
		stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: fakePNG}},
			{Kind: llm.EventEnd},
		}})
		return &capturingClient{inner: stub, sink: captured}, nil
	}

	app := NewApp(factory)
	dir := t.TempDir()
	if err := app.Run(context.Background(), []string{"aikido", "image", "--out", dir, "say hi"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	select {
	case req := <-captured:
		if req.Model != "custom/model-from-env" {
			t.Errorf("model = %q, want custom/model-from-env", req.Model)
		}
	default:
		t.Fatal("no request captured")
	}
}

func TestImageCommand_ThreadsAspectAndSize(t *testing.T) {
	captured := make(chan llm.Request, 1)
	factory := func() (llm.Client, error) {
		stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: fakePNG}},
			{Kind: llm.EventEnd},
		}})
		return &capturingClient{inner: stub, sink: captured}, nil
	}
	app := NewApp(factory)
	dir := t.TempDir()
	args := []string{"aikido", "image", "--out", dir, "--aspect", "16:9", "--size", "2K", "wide shot"}
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	select {
	case req := <-captured:
		if req.ImageConfig == nil {
			t.Fatal("ImageConfig not threaded")
		}
		if req.ImageConfig.AspectRatio != "16:9" {
			t.Errorf("AspectRatio = %q, want 16:9", req.ImageConfig.AspectRatio)
		}
		if req.ImageConfig.ImageSize != "2K" {
			t.Errorf("ImageSize = %q, want 2K", req.ImageConfig.ImageSize)
		}
	default:
		t.Fatal("no request captured")
	}
}

func TestImageCommand_OmitsImageConfigWhenUnset(t *testing.T) {
	captured := make(chan llm.Request, 1)
	factory := func() (llm.Client, error) {
		stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: fakePNG}},
			{Kind: llm.EventEnd},
		}})
		return &capturingClient{inner: stub, sink: captured}, nil
	}
	app := NewApp(factory)
	dir := t.TempDir()
	if err := app.Run(context.Background(), []string{"aikido", "image", "--out", dir, "hi"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	select {
	case req := <-captured:
		if req.ImageConfig != nil {
			t.Errorf("ImageConfig = %+v, want nil when flags unset", req.ImageConfig)
		}
	default:
		t.Fatal("no request captured")
	}
}

type capturingClient struct {
	inner llm.Client
	sink  chan<- llm.Request
}

func (c *capturingClient) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	select {
	case c.sink <- req:
	default:
	}
	return c.inner.Stream(ctx, req)
}

func (c *capturingClient) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	select {
	case c.sink <- req:
	default:
	}
	return c.inner.Complete(ctx, req)
}
