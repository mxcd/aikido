//go:build smoke

package cli

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mxcd/aikido/llm/openrouter"
)

// recordingTransport remembers the path and body of the last request so the
// smoke test can assert which endpoint actually took the call.
type recordingTransport struct {
	mu   sync.Mutex
	path string
	body []byte
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil && req.GetBody != nil {
		if b, err := req.GetBody(); err == nil {
			buf := new(bytes.Buffer)
			_, _ = buf.ReadFrom(b)
			_ = b.Close()
			body = buf.Bytes()
		}
	}
	rt.mu.Lock()
	rt.path = req.URL.Path
	rt.body = body
	rt.mu.Unlock()
	return http.DefaultTransport.RoundTrip(req)
}

func (rt *recordingTransport) snapshot() (string, []byte) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.path, rt.body
}

// TestSmoke_ImageGeneration is the acceptance check from the plan, run live
// against OpenRouter. It costs roughly 0.05 USD per run and never runs in CI:
// it needs both the `smoke` build tag and AIKIDO_LIVE_SMOKE=1.
//
//	just smoke
func TestSmoke_ImageGeneration(t *testing.T) {
	if os.Getenv("AIKIDO_LIVE_SMOKE") != "1" {
		t.Skip("set AIKIDO_LIVE_SMOKE=1 to run the live smoke test")
	}
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		t.Skip("OPENROUTER_API_KEY is not set")
	}

	cases := []struct {
		name     string
		model    string
		list     []string
		wantPath string
	}{
		{
			name:     "flare_images_api",
			model:    defaultImageModel,
			list:     defaultImagesAPIModels,
			wantPath: "/api/v1/images",
		},
		{
			name:     "gemini_chat_path",
			model:    "google/gemini-3.1-flash-image-preview",
			list:     defaultImagesAPIModels,
			wantPath: "/api/v1/chat/completions",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := &recordingTransport{}
			client, err := openrouter.NewClient(&openrouter.Options{
				APIKey:      apiKey,
				HTTPClient:  &http.Client{Transport: rt},
				HTTPReferer: "https://github.com/mxcd/aikido",
				XTitle:      "aikido CLI",
			})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			dir := t.TempDir()
			var out bytes.Buffer
			err = runImage(context.Background(), imageOpts{
				model:           tc.model,
				outDir:          dir,
				prompt:          "a red cube on white",
				maxTokens:       1024,
				imagesAPIModels: tc.list,
			}, &out, client)
			if err != nil {
				t.Fatalf("runImage: %v\n%s", err, out.String())
			}

			path, body := rt.snapshot()
			if path != tc.wantPath {
				t.Errorf("endpoint = %q, want %q", path, tc.wantPath)
			}
			if !bytes.Contains(body, []byte(`"`+tc.model+`"`)) {
				t.Errorf("request body does not name %s: %s", tc.model, body)
			}

			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("read out dir: %v", err)
			}
			if len(entries) != 1 {
				t.Fatalf("wrote %d files, want 1", len(entries))
			}
			f, err := os.Open(filepath.Join(dir, entries[0].Name()))
			if err != nil {
				t.Fatalf("open image: %v", err)
			}
			defer f.Close()
			if tc.name == "flare_images_api" {
				if _, err := png.Decode(f); err != nil {
					t.Errorf("png.Decode: %v", err)
				}
				return
			}
			if _, _, err := image.Decode(f); err != nil {
				t.Errorf("image.Decode: %v", err)
			}
		})
	}
}
