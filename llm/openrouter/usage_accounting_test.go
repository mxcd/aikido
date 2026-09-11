package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mxcd/aikido/llm"
)

// TestUsageAccounting_RequestedOnBothPaths pins the wire contract that makes
// exact cost accounting possible at all: without "usage":{"include":true} the
// provider reports tokens but never a price.
func TestUsageAccounting_RequestedOnBothPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(t *testing.T, c *Client)
		body []byte
	}{
		{
			name: "stream",
			call: func(t *testing.T, c *Client) {
				ch, err := c.Stream(context.Background(), llm.Request{Model: "any"})
				if err != nil {
					t.Fatalf("Stream: %v", err)
				}
				_ = drain(t, ch, 2*time.Second)
			},
		},
		{
			name: "complete",
			call: func(t *testing.T, c *Client) {
				if _, err := c.Complete(context.Background(), llm.Request{Model: "any"}); err != nil {
					t.Fatalf("Complete: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var captured []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`))
			}))
			defer srv.Close()

			tt.call(t, newTestClient(t, srv))

			if !bytes.Contains(captured, []byte(`"usage":{"include":true}`)) {
				t.Errorf("request body missing usage accounting: %s", captured)
			}
			var decoded map[string]any
			if err := json.Unmarshal(captured, &decoded); err != nil {
				t.Fatalf("body is not JSON: %v", err)
			}
			usage, ok := decoded["usage"].(map[string]any)
			if !ok || usage["include"] != true {
				t.Errorf("usage block = %v, want {include:true}", decoded["usage"])
			}
		})
	}
}

// TestComplete_ErrorEnvelopeKeepsUsage: a failed turn the provider priced was
// still billed, so the price has to reach the caller with the error.
func TestComplete_ErrorEnvelopeKeepsUsage(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":{"code":"server_error","message":"provider blew up"},` +
			`"usage":{"prompt_tokens":12,"completion_tokens":0,"total_tokens":12,"cost":0.00042}}`))
	}))
	defer srv.Close()

	resp, err := newTestClient(t, srv).Complete(context.Background(), llm.Request{Model: "any"})
	if !errors.Is(err, llm.ErrServerError) {
		t.Fatalf("err = %v, want ErrServerError", err)
	}
	if resp.Usage == nil {
		t.Fatal("usage dropped on the error path")
	}
	if resp.Usage.CostUSD != 0.00042 {
		t.Errorf("CostUSD = %v, want 0.00042", resp.Usage.CostUSD)
	}
	if resp.Usage.PromptTokens != 12 {
		t.Errorf("PromptTokens = %d, want 12", resp.Usage.PromptTokens)
	}
}

func TestComplete_CostIsMapped(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],` +
			`"usage":{"prompt_tokens":18,"completion_tokens":4,"total_tokens":22,"cost":0.000066}}`))
	}))
	defer srv.Close()

	resp, err := newTestClient(t, srv).Complete(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Usage == nil || resp.Usage.CostUSD != 0.000066 {
		t.Fatalf("usage = %+v, want CostUSD 0.000066", resp.Usage)
	}
}

// TestStream_ErrorEnvelopeEmitsUsageBeforeError: the price of the failed turn
// arrives on the same chunk as the error envelope.
func TestStream_ErrorEnvelopeEmitsUsageBeforeError(t *testing.T) {
	t.Parallel()
	body := []byte(`data: {"id":"g","choices":[{"index":0,"delta":{"content":"partial"}}]}

data: {"id":"g","error":{"code":"server_error","message":"boom"},"usage":{"prompt_tokens":9,"completion_tokens":1,"total_tokens":10,"cost":0.00011}}

data: [DONE]
`)
	srv := httptest.NewServer(sseHandler(t, body))
	defer srv.Close()

	ch, err := newTestClient(t, srv).Stream(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 2*time.Second)

	kinds := eventKinds(events)
	usageAt, errAt := indexOf(kinds, llm.EventUsage), indexOf(kinds, llm.EventError)
	if usageAt < 0 {
		t.Fatalf("no usage event; kinds = %v", kinds)
	}
	if errAt < 0 {
		t.Fatalf("no error event; kinds = %v", kinds)
	}
	if usageAt > errAt {
		t.Errorf("usage arrived after the error; kinds = %v", kinds)
	}
	if got := events[usageAt].Usage.CostUSD; got != 0.00011 {
		t.Errorf("CostUSD = %v, want 0.00011", got)
	}
	if last := events[len(events)-1].Kind; last != llm.EventEnd {
		t.Errorf("last event = %v, want EventEnd", last)
	}
}

// TestStream_TrailingUsageAfterErrorIsStillEmitted: the scanner used to stop
// on the error envelope, which threw away a price the provider sends one chunk
// later.
func TestStream_TrailingUsageAfterErrorIsStillEmitted(t *testing.T) {
	t.Parallel()
	body := []byte(`data: {"id":"g","choices":[{"index":0,"delta":{"content":"partial"}}]}

data: {"id":"g","error":{"code":"server_error","message":"boom"}}

data: {"id":"g","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":1,"total_tokens":10,"cost":0.00013}}

data: [DONE]
`)
	srv := httptest.NewServer(sseHandler(t, body))
	defer srv.Close()

	ch, err := newTestClient(t, srv).Stream(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 2*time.Second)
	kinds := eventKinds(events)

	usageAt := indexOf(kinds, llm.EventUsage)
	if usageAt < 0 {
		t.Fatalf("trailing usage dropped; kinds = %v", kinds)
	}
	if got := events[usageAt].Usage.CostUSD; got != 0.00013 {
		t.Errorf("CostUSD = %v, want 0.00013", got)
	}
	if errAt := indexOf(kinds, llm.EventError); errAt < 0 || errAt > usageAt {
		t.Errorf("expected the error before the trailing usage; kinds = %v", kinds)
	}
	if last := events[len(events)-1].Kind; last != llm.EventEnd {
		t.Errorf("last event = %v, want EventEnd", last)
	}
}

// TestStream_TrailingUsageAfterContentFilter covers the second abort shape: a
// finish_reason rather than an error envelope.
func TestStream_TrailingUsageAfterContentFilter(t *testing.T) {
	t.Parallel()
	body := []byte(`data: {"id":"g","choices":[{"index":0,"delta":{"content":"draw"}}]}

data: {"id":"g","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}

data: {"id":"g","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":0,"total_tokens":7,"cost":0.00002}}

data: [DONE]
`)
	srv := httptest.NewServer(sseHandler(t, body))
	defer srv.Close()

	ch, err := newTestClient(t, srv).Stream(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 2*time.Second)
	kinds := eventKinds(events)

	usageAt := indexOf(kinds, llm.EventUsage)
	if usageAt < 0 {
		t.Fatalf("usage after a content filter dropped; kinds = %v", kinds)
	}
	if got := events[usageAt].Usage.CostUSD; got != 0.00002 {
		t.Errorf("CostUSD = %v, want 0.00002", got)
	}
	if err := events[indexOf(kinds, llm.EventError)].Err; !errors.Is(err, llm.ErrContentFiltered) {
		t.Errorf("err = %v, want ErrContentFiltered", err)
	}
}

// TestStream_UsageIsEmittedOnce guards against a provider that starts sending
// cumulative usage snapshots: only the first one is surfaced.
func TestStream_UsageIsEmittedOnce(t *testing.T) {
	t.Parallel()
	body := []byte(`data: {"id":"g","choices":[{"index":0,"delta":{"content":"hi"}}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4,"cost":0.00001}}

data: {"id":"g","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"cost":0.00002}}

data: [DONE]
`)
	srv := httptest.NewServer(sseHandler(t, body))
	defer srv.Close()

	ch, err := newTestClient(t, srv).Stream(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 2*time.Second)

	var usages int
	for _, ev := range events {
		if ev.Kind == llm.EventUsage {
			usages++
		}
	}
	if usages != 1 {
		t.Errorf("usage events = %d, want 1", usages)
	}
}

func eventKinds(events []llm.Event) []llm.EventKind {
	out := make([]llm.EventKind, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Kind)
	}
	return out
}

func indexOf(kinds []llm.EventKind, want llm.EventKind) int {
	for i, k := range kinds {
		if k == want {
			return i
		}
	}
	return -1
}
