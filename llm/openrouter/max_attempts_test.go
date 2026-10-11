package openrouter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/mxcd/aikido/llm"
)

// TestMaxAttempts pins Options.MaxAttempts on every call path: a server that
// always answers 503 must see exactly the configured number of requests.
func TestMaxAttempts(t *testing.T) {
	t.Parallel()
	paths := map[string]func(context.Context, *Client) error{
		"stream": func(ctx context.Context, c *Client) error {
			_, err := c.Stream(ctx, llm.Request{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
			return err
		},
		"complete": func(ctx context.Context, c *Client) error {
			_, err := c.Complete(ctx, llm.Request{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
			return err
		},
		"transcribe": func(ctx context.Context, c *Client) error {
			_, _, err := llm.Transcribe(ctx, c, llm.TranscribeRequest{Audio: []byte("x"), Format: "wav"})
			return err
		},
		"images": func(ctx context.Context, c *Client) error {
			_, err := c.GenerateImage(ctx, llm.ImageRequest{Model: "m", Prompt: "p"})
			return err
		},
		"speech": func(ctx context.Context, c *Client) error {
			_, err := c.Speak(ctx, llm.SpeechRequest{Model: "m", Input: "hi", Voice: "v"})
			return err
		},
	}
	for name, call := range paths {
		for _, tc := range []struct{ maxAttempts, want int }{{1, 1}, {2, 2}, {0, 3}} {
			t.Run(fmt.Sprintf("%s/max_attempts_%d", name, tc.maxAttempts), func(t *testing.T) {
				t.Parallel()
				var hits atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					hits.Add(1)
					http.Error(w, `{"error":{"message":"down"}}`, http.StatusServiceUnavailable)
				}))
				defer srv.Close()
				c, err := NewClient(&Options{APIKey: "k", BaseURL: srv.URL, MaxAttempts: tc.maxAttempts})
				if err != nil {
					t.Fatal(err)
				}
				if err := call(context.Background(), c); !errors.Is(err, llm.ErrServerError) {
					t.Fatalf("err = %v, want ErrServerError", err)
				}
				if got := int(hits.Load()); got != tc.want {
					t.Fatalf("MaxAttempts=%d: %d requests, want %d", tc.maxAttempts, got, tc.want)
				}
			})
		}
	}
}
