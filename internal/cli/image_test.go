package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
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
	args := []string{"aikido", "image", "--out", dir, "-m", "google/gemini-3.1-flash-image-preview", "--aspect", "16:9", "--size", "2K", "wide shot"}
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
	if err := app.Run(context.Background(), []string{"aikido", "image", "--out", dir, "-m", "google/gemini-3.1-flash-image-preview", "hi"}); err != nil {
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

func (c *capturingClient) GenerateImage(ctx context.Context, req llm.ImageRequest) (llm.ImageResponse, error) {
	gen, ok := c.inner.(llm.ImageGenerator)
	if !ok {
		return llm.ImageResponse{}, errors.New("inner client is not an llm.ImageGenerator")
	}
	return gen.GenerateImage(ctx, req)
}

// chatOnlyClient implements llm.Client and nothing else, standing in for a
// provider that has no images endpoint.
type chatOnlyClient struct{ inner llm.Client }

func (c *chatOnlyClient) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	return c.inner.Stream(ctx, req)
}

func (c *chatOnlyClient) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return c.inner.Complete(ctx, req)
}

// imagesStub returns a stub scripted with one PNG on the images endpoint.
func imagesStub() *llmtest.StubClient {
	stub := llmtest.NewStubClient()
	stub.ScriptImages(llmtest.ImageScript{Response: llm.ImageResponse{
		Images: []llm.ImagePart{{ContentType: "image/png", Data: fakePNG}},
		Usage:  &llm.Usage{CostUSD: 0.0053},
	}})
	return stub
}

func TestNormalizeModelID(t *testing.T) {
	cases := map[string]string{
		"openai/gpt-image-2.5-flare":      "openai/gpt-image-2.5-flare",
		"  openai/gpt-image-2.5-flare  ":  "openai/gpt-image-2.5-flare",
		"openai/gpt-image-2.5-flare:free": "openai/gpt-image-2.5-flare",
		" meta/muse-image:nitro ":         "meta/muse-image",
		"":                                "",
	}
	for in, want := range cases {
		if got := normalizeModelID(in); got != want {
			t.Errorf("normalizeModelID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUsesImagesAPI(t *testing.T) {
	list := defaultImagesAPIModels
	cases := []struct {
		model string
		want  bool
	}{
		{"openai/gpt-image-2.5-flare", true},
		{"openai/gpt-image-2.5-flare:free", true},
		{"  openai/gpt-image-2.5-flare  ", true},
		{"meta/muse-image", true},
		{"google/gemini-3.1-flash-image-preview", false},
		{"openai/gpt-5.4-image-2", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := usesImagesAPI(tc.model, list); got != tc.want {
			t.Errorf("usesImagesAPI(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
	// A list entry may carry the same noise the model id does.
	if !usesImagesAPI("x/y", []string{" x/y:free "}) {
		t.Error("list entries must be normalized too")
	}
	if usesImagesAPI("openai/gpt-image-2.5-flare", []string{"none"}) {
		t.Error(`"none" must disable images routing`)
	}
}

func TestParseImagesAPIModels(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a/b,c/d", []string{"a/b", "c/d"}},
		{" a/b , c/d ", []string{"a/b", "c/d"}},
		{"a/b,,c/d,", []string{"a/b", "c/d"}},
		{"none", []string{"none"}},
		{"", defaultImagesAPIModels},
		{"  ,  ", defaultImagesAPIModels},
	}
	for _, tc := range cases {
		got := parseImagesAPIModels(tc.in)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("parseImagesAPIModels(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// clearImageEnv removes both image env vars for the duration of the test.
// t.Setenv registers the restore; os.Unsetenv then makes them genuinely absent.
func clearImageEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"OPENROUTER_IMAGE_MODEL", "OPENROUTER_IMAGES_API_MODELS"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
}

func TestImageCommand_DefaultModelUsesImagesAPI(t *testing.T) {
	clearImageEnv(t)
	stub := imagesStub()
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	dir := t.TempDir()
	if err := app.Run(context.Background(), []string{"aikido", "image", "--out", dir, "a red cube on white"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}

	reqs := stub.ImageRequests()
	if len(reqs) != 1 {
		t.Fatalf("image requests = %d, want 1", len(reqs))
	}
	if reqs[0].Model != defaultImageModel {
		t.Errorf("model = %q, want %q", reqs[0].Model, defaultImageModel)
	}
	if reqs[0].Prompt != "a red cube on white" {
		t.Errorf("prompt = %q", reqs[0].Prompt)
	}
	if n := len(stub.Requests()); n != 0 {
		t.Errorf("chat requests = %d, want 0 on the images path", n)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".png") {
		t.Errorf("output files = %+v, want one PNG", entries)
	}
}

func TestImageCommand_EmptyEnvFallsBackToDefault(t *testing.T) {
	clearImageEnv(t)
	t.Setenv("OPENROUTER_IMAGE_MODEL", "")
	stub := imagesStub()
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	if err := app.Run(context.Background(), []string{"aikido", "image", "--out", t.TempDir(), "hi"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	reqs := stub.ImageRequests()
	if len(reqs) != 1 || reqs[0].Model != defaultImageModel {
		t.Errorf("image requests = %+v, want one call on %s", reqs, defaultImageModel)
	}
}

func TestImageCommand_HelpShowsFlareDefault(t *testing.T) {
	clearImageEnv(t)
	var out bytes.Buffer
	app := NewApp(func() (llm.Client, error) { return nil, errors.New("factory must not run for --help") })
	app.Writer = &out
	if err := app.Run(context.Background(), []string{"aikido", "image", "--help"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	help := out.String()
	if !strings.Contains(help, defaultImageModel) {
		t.Errorf("--help does not show the default model %q:\n%s", defaultImageModel, help)
	}
	if !strings.Contains(help, "OPENROUTER_IMAGE_MODEL") {
		t.Errorf("--help does not show the env override:\n%s", help)
	}
	for _, flag := range []string{"--ref", "--quality", "--images-api-models"} {
		if !strings.Contains(help, flag) {
			t.Errorf("--help does not list %s:\n%s", flag, help)
		}
	}
}

func TestImageCommand_ImagesAPIModelsEnv(t *testing.T) {
	clearImageEnv(t)
	t.Setenv("OPENROUTER_IMAGES_API_MODELS", "x/y, z/w")

	// x/y is now images-API only.
	stub := imagesStub()
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	if err := app.Run(context.Background(), []string{"aikido", "image", "--out", t.TempDir(), "-m", "x/y", "hi"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	if reqs := stub.ImageRequests(); len(reqs) != 1 || reqs[0].Model != "x/y" {
		t.Errorf("image requests = %+v, want one call on x/y", reqs)
	}

	// flare fell off the list, so it takes chat completions.
	chat := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: fakePNG}},
		{Kind: llm.EventEnd},
	}})
	app2 := NewApp(func() (llm.Client, error) { return chat, nil })
	if err := app2.Run(context.Background(), []string{"aikido", "image", "--out", t.TempDir(), "hi"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	if reqs := chat.Requests(); len(reqs) != 1 || reqs[0].Model != defaultImageModel {
		t.Errorf("chat requests = %+v, want one call on %s", reqs, defaultImageModel)
	}
	if n := len(chat.ImageRequests()); n != 0 {
		t.Errorf("image requests = %d, want 0", n)
	}
}

func TestImageCommand_FlagBeatsEnv(t *testing.T) {
	clearImageEnv(t)
	t.Setenv("OPENROUTER_IMAGES_API_MODELS", "x/y")

	stub := imagesStub()
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	args := []string{"aikido", "image", "--out", t.TempDir(), "--images-api-models", "openai/gpt-image-2.5-flare", "hi"}
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	if reqs := stub.ImageRequests(); len(reqs) != 1 || reqs[0].Model != defaultImageModel {
		t.Errorf("image requests = %+v, want the flag list to win over the env", reqs)
	}
}

func TestImageCommand_ThreadsQualityAndRefs(t *testing.T) {
	clearImageEnv(t)
	ref := filepath.Join(t.TempDir(), "ref.png")
	if err := os.WriteFile(ref, fakePNG, 0o644); err != nil {
		t.Fatalf("write reference: %v", err)
	}
	stub := imagesStub()
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	args := []string{
		"aikido", "image", "--out", t.TempDir(),
		"--quality", "high", "--aspect", "9:16", "--size", "2K", "--ref", ref,
		"a red cube on white",
	}
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	reqs := stub.ImageRequests()
	if len(reqs) != 1 {
		t.Fatalf("image requests = %d, want 1", len(reqs))
	}
	got := reqs[0]
	if got.Quality != "high" || got.AspectRatio != "9:16" || got.ImageSize != "2K" {
		t.Errorf("request = %+v", got)
	}
	if len(got.References) != 1 {
		t.Fatalf("references = %d, want 1", len(got.References))
	}
	wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(fakePNG)
	if got.References[0].URL != wantURL {
		t.Errorf("reference URL = %q, want %q", got.References[0].URL, wantURL)
	}
}

func TestImageCommand_RefWithComma(t *testing.T) {
	clearImageEnv(t)
	ref := filepath.Join(t.TempDir(), "a,b.png")
	if err := os.WriteFile(ref, fakePNG, 0o644); err != nil {
		t.Fatalf("write reference: %v", err)
	}
	stub := imagesStub()
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	if err := app.Run(context.Background(), []string{"aikido", "image", "--out", t.TempDir(), "--ref", ref, "hi"}); err != nil {
		t.Fatalf("app.Run: %v", err)
	}
	reqs := stub.ImageRequests()
	if len(reqs) != 1 || len(reqs[0].References) != 1 {
		t.Fatalf("a comma in the path split the flag: %+v", reqs)
	}
}

func TestRunImage_RefsOnChatPath(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref.png")
	if err := os.WriteFile(ref, fakePNG, 0o644); err != nil {
		t.Fatalf("write reference: %v", err)
	}
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: fakePNG}},
		{Kind: llm.EventEnd},
	}})
	var out bytes.Buffer
	err := runImage(context.Background(), imageOpts{
		model:  "google/gemini-3.1-flash-image-preview",
		outDir: dir,
		prompt: "a fox",
		refs:   []string{ref},
	}, &out, stub)
	if err != nil {
		t.Fatalf("runImage: %v", err)
	}
	reqs := stub.Requests()
	if len(reqs) != 1 || len(reqs[0].Messages) != 1 {
		t.Fatalf("requests = %+v", reqs)
	}
	imgs := reqs[0].Messages[0].Images
	if len(imgs) != 1 || !strings.HasPrefix(imgs[0].URL, "data:image/png;base64,") {
		t.Errorf("Messages[0].Images = %+v", imgs)
	}
}

func TestRunImage_QualityRejectedOnChatPath(t *testing.T) {
	stub := llmtest.NewStubClient()
	var out bytes.Buffer
	err := runImage(context.Background(), imageOpts{
		model:   "google/gemini-3.1-flash-image-preview",
		outDir:  t.TempDir(),
		prompt:  "hi",
		quality: "high",
	}, &out, stub)
	if err == nil || !strings.Contains(err.Error(), "--quality") {
		t.Errorf("err = %v, want a --quality rejection", err)
	}
}

func TestRunImage_ClientWithoutImageGenerator(t *testing.T) {
	client := &chatOnlyClient{inner: llmtest.NewStubClient()}
	var out bytes.Buffer
	err := runImage(context.Background(), imageOpts{
		model:           defaultImageModel,
		outDir:          t.TempDir(),
		prompt:          "hi",
		imagesAPIModels: defaultImagesAPIModels,
	}, &out, client)
	if err == nil || !strings.Contains(err.Error(), "images endpoint") {
		t.Errorf("err = %v, want a clear capability error", err)
	}
}

func TestLoadReferenceImages(t *testing.T) {
	dir := t.TempDir()

	png := filepath.Join(dir, "a.png")
	if err := os.WriteFile(png, fakePNG, 0o644); err != nil {
		t.Fatal(err)
	}
	// No extension: the MIME type has to be sniffed from the bytes.
	sniffed := filepath.Join(dir, "noext")
	if err := os.WriteFile(sniffed, fakePNG, 0o644); err != nil {
		t.Fatal(err)
	}
	notAnImage := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notAnImage, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, err := loadReferenceImages(nil); err != nil || got != nil {
		t.Errorf("loadReferenceImages(nil) = %+v, %v", got, err)
	}

	parts, err := loadReferenceImages([]string{png, sniffed})
	if err != nil {
		t.Fatalf("loadReferenceImages: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	for i, p := range parts {
		if p.ContentType != "image/png" {
			t.Errorf("part %d ContentType = %q, want image/png", i, p.ContentType)
		}
		if p.URL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(fakePNG) {
			t.Errorf("part %d URL = %q", i, p.URL)
		}
	}

	if _, err := loadReferenceImages([]string{notAnImage}); err == nil || !strings.Contains(err.Error(), "not an image") {
		t.Errorf("err = %v, want a non-image rejection", err)
	}
	if _, err := loadReferenceImages([]string{filepath.Join(dir, "missing.png")}); err == nil {
		t.Error("expected an error for a missing file")
	}

	big := filepath.Join(dir, "big.png")
	if err := os.WriteFile(big, append(append([]byte{}, fakePNG...), make([]byte, maxReferenceBytes)...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReferenceImages([]string{big}); err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Errorf("err = %v, want a size-budget rejection", err)
	}
}

func TestHintImagesAPI(t *testing.T) {
	if hintImagesAPI(nil) != nil {
		t.Error("nil error must stay nil")
	}
	plain := errors.New("openrouter: status 401: bad key")
	if got := hintImagesAPI(plain); got != plain {
		t.Errorf("unrelated error was rewritten: %v", got)
	}
	for _, msg := range []string{
		`openrouter: status 404: this model is only available via the /api/v1/images endpoint`,
		`openrouter: status 400: model does not support the requested output modalities`,
	} {
		got := hintImagesAPI(errors.New(msg))
		if !strings.Contains(got.Error(), "OPENROUTER_IMAGES_API_MODELS") {
			t.Errorf("hint missing for %q: %v", msg, got)
		}
		if !strings.Contains(got.Error(), msg) {
			t.Errorf("original message lost: %v", got)
		}
	}
}
