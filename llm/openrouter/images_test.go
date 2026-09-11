package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mxcd/aikido/llm"
)

// readFixture loads a recorded response body from testdata.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestGenerateImage_RequestOnWire(t *testing.T) {
	t.Parallel()
	var (
		gotPath   string
		gotHeader http.Header
		gotBody   []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(readFixture(t, "images_b64.json"))
	}))
	defer srv.Close()

	c, err := NewClient(&Options{
		APIKey:      "sk-test",
		BaseURL:     srv.URL,
		HTTPReferer: "https://github.com/mxcd/aikido",
		XTitle:      "aikido CLI",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := c.GenerateImage(context.Background(), llm.ImageRequest{
		Model:       "openai/gpt-image-2.5-flare",
		Prompt:      "a red cube on white",
		AspectRatio: "9:16",
		ImageSize:   "1K",
		Quality:     "high",
		References:  []llm.ImagePart{{URL: "data:image/jpeg;base64,QUJD", ContentType: "image/jpeg"}},
	}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}

	if gotPath != "/images" {
		t.Errorf("path = %q, want /images", gotPath)
	}
	if got := gotHeader.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want application/json", got)
	}
	if got := gotHeader.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q", got)
	}
	if got := gotHeader.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := gotHeader.Get("HTTP-Referer"); got != "https://github.com/mxcd/aikido" {
		t.Errorf("HTTP-Referer = %q", got)
	}
	if got := gotHeader.Get("X-Title"); got != "aikido CLI" {
		t.Errorf("X-Title = %q", got)
	}

	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, gotBody)
	}
	want := map[string]any{
		"model":        "openai/gpt-image-2.5-flare",
		"prompt":       "a red cube on white",
		"aspect_ratio": "9:16",
		"resolution":   "1K",
		"quality":      "high",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%q] = %v, want %v", k, body[k], v)
		}
	}
	if _, ok := body["size"]; ok {
		t.Error("body carries a `size` field; the tier must ride on `resolution`")
	}
	refs, ok := body["input_references"].([]any)
	if !ok || len(refs) != 1 {
		t.Fatalf("input_references = %v", body["input_references"])
	}
	ref := refs[0].(map[string]any)
	if ref["type"] != "image_url" {
		t.Errorf("reference type = %v", ref["type"])
	}
	if url := ref["image_url"].(map[string]any)["url"]; url != "data:image/jpeg;base64,QUJD" {
		t.Errorf("reference url = %v", url)
	}
}

func TestGenerateImage_OmitsUnsetFields(t *testing.T) {
	t.Parallel()
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(readFixture(t, "images_b64.json"))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	if _, err := c.GenerateImage(context.Background(), llm.ImageRequest{
		Model:  "openai/gpt-image-2.5-flare",
		Prompt: "a red cube on white",
	}); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	for _, k := range []string{"aspect_ratio", "resolution", "size", "quality", "input_references"} {
		if _, ok := body[k]; ok {
			t.Errorf("body carries %q when unset: %s", k, gotBody)
		}
	}
}

func TestBuildImagesBody_Size(t *testing.T) {
	body, err := buildImagesBody(llm.ImageRequest{Model: "m", Prompt: "p", Size: "1024x1280", Quality: "medium"})
	if err != nil {
		t.Fatalf("buildImagesBody: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["size"] != "1024x1280" {
		t.Errorf("size = %v", got["size"])
	}
	for _, k := range []string{"aspect_ratio", "resolution"} {
		if _, ok := got[k]; ok {
			t.Errorf("body carries %q next to size", k)
		}
	}
	for _, bad := range []llm.ImageRequest{
		{Model: "m", Prompt: "p", Size: "1K"},
		{Model: "m", Prompt: "p", Size: "1024x"},
		{Model: "m", Prompt: "p", Size: "1024x1280", AspectRatio: "4:5"},
		{Model: "m", Prompt: "p", Size: "1024x1280", ImageSize: "1K"},
	} {
		if _, err := buildImagesBody(bad); !errors.Is(err, llm.ErrInvalidRequest) {
			t.Errorf("Size %q with ratio %q tier %q: err = %v, want ErrInvalidRequest", bad.Size, bad.AspectRatio, bad.ImageSize, err)
		}
	}
}

func TestGenerateImage_DecodesB64(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(jsonHandler(t, readFixture(t, "images_b64.json")))
	defer srv.Close()
	c := newTestClient(t, srv)

	resp, err := c.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(resp.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(resp.Images))
	}
	img := resp.Images[0]
	if len(img.Data) == 0 {
		t.Fatal("no inline bytes decoded")
	}
	if !isPNG(img.Data) {
		t.Errorf("decoded bytes are not a PNG: %x", img.Data[:8])
	}
	if img.ContentType != "image/png" {
		t.Errorf("ContentType = %q", img.ContentType)
	}
	if img.URL != "" {
		t.Errorf("URL = %q, want empty for an inline image", img.URL)
	}
	if resp.Usage == nil || resp.Usage.CostUSD == 0 {
		t.Errorf("usage = %+v, want a non-zero cost", resp.Usage)
	}
}

func TestGenerateImage_URLKeepsContentType(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(jsonHandler(t, readFixture(t, "images_url.json")))
	defer srv.Close()
	c := newTestClient(t, srv)

	resp, err := c.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(resp.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(resp.Images))
	}
	if got := resp.Images[0].URL; got != "https://cdn.openrouter.ai/gen/abc.png" {
		t.Errorf("URL = %q", got)
	}
	if got := resp.Images[0].ContentType; got != "image/png" {
		t.Errorf("ContentType = %q, want image/png", got)
	}
	if len(resp.Images[0].Data) != 0 {
		t.Error("URL datum must not carry inline bytes")
	}
}

func TestGenerateImage_SniffsContentType(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(jsonHandler(t, readFixture(t, "images_no_media_type.json")))
	defer srv.Close()
	c := newTestClient(t, srv)

	resp, err := c.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(resp.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(resp.Images))
	}
	if got := resp.Images[0].ContentType; got != "image/png" {
		t.Errorf("sniffed ContentType = %q, want image/png", got)
	}
}

func TestGenerateImage_ErrorEnvelopeNoRetry(t *testing.T) {
	t.Parallel()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(readFixture(t, "images_error_envelope.json"))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	_, err := c.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"})
	if !errors.Is(err, llm.ErrServerError) {
		t.Fatalf("err = %v, want ErrServerError", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("hits = %d, want 1 (a 200 envelope is parsed outside the retry loop)", got)
	}
}

func TestGenerateImage_ContentFilterEnvelope(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(jsonHandler(t, readFixture(t, "images_content_filter.json")))
	defer srv.Close()
	c := newTestClient(t, srv)

	_, err := c.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"})
	if !errors.Is(err, llm.ErrContentFiltered) {
		t.Fatalf("err = %v, want ErrContentFiltered", err)
	}
}

func TestGenerateImage_400NoRetry(t *testing.T) {
	t.Parallel()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(readFixture(t, "images_400_aspect.json"))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	_, err := c.GenerateImage(context.Background(), llm.ImageRequest{
		Model: "openai/gpt-image-2.5-flare", Prompt: "p", AspectRatio: "4:5",
	})
	if !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("hits = %d, want 1 (no retry on 400)", got)
	}
}

func TestGenerateImage_5xxRetryThenSuccess(t *testing.T) {
	t.Parallel()
	var (
		hits   int32
		bodies [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(readFixture(t, "images_b64.json"))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	resp, err := c.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(resp.Images) != 1 {
		t.Errorf("images = %d, want 1", len(resp.Images))
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("hits = %d, want 2 (one 503 + one success)", got)
	}
	if len(bodies) != 2 || string(bodies[0]) != string(bodies[1]) {
		t.Errorf("retry sent a different body:\n%s\n%s", bodies[0], bodies[1])
	}
}

// TestGenerateImage_201IsError pins the 200-only rule (ADR-029): model-arena
// accepts any 2xx, aikido does not, so an unexpected 201 surfaces as a
// retried server error rather than being silently parsed.
func TestGenerateImage_201IsError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(readFixture(t, "images_b64.json"))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	if _, err := c.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"}); err == nil {
		t.Fatal("expected an error on 201")
	}
}

func TestBuildImagesBody_Validation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		req  llm.ImageRequest
	}{
		{"empty model", llm.ImageRequest{Prompt: "p"}},
		{"blank model", llm.ImageRequest{Model: "  ", Prompt: "p"}},
		{"empty prompt", llm.ImageRequest{Model: "m"}},
		{"blank prompt", llm.ImageRequest{Model: "m", Prompt: "  "}},
		{"reference without url", llm.ImageRequest{Model: "m", Prompt: "p", References: []llm.ImagePart{{Data: []byte("x")}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildImagesBody(tc.req)
			if !errors.Is(err, llm.ErrInvalidRequest) {
				t.Errorf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func isPNG(b []byte) bool {
	return len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n"
}
