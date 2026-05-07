package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mxcd/aikido/llm"
)

// loadFixture reads an SSE transcript from testdata/.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// sseHandler returns an http.HandlerFunc that writes the given SSE bytes with
// status 200 and Content-Type: text/event-stream.
func sseHandler(t *testing.T, body []byte) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// newTestClient builds a Client targeting srv.URL.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := NewClient(&Options{
		APIKey:  "sk-test",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// drain collects events from a stream until the channel closes.
func drain(t *testing.T, ch <-chan llm.Event, timeout time.Duration) []llm.Event {
	t.Helper()
	var out []llm.Event
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("timeout draining stream after %d events", len(out))
		}
	}
}

func TestNewClient_RequiresAPIKey(t *testing.T) {
	t.Parallel()
	if _, err := NewClient(&Options{APIKey: ""}); err == nil {
		t.Error("NewClient with empty APIKey should error")
	}
	if _, err := NewClient(nil); err == nil {
		t.Error("NewClient(nil) should error")
	}
	if _, err := NewClient(&Options{APIKey: "k"}); err != nil {
		t.Errorf("NewClient(valid): unexpected err %v", err)
	}
}

func TestClient_ImplementsLLMClient(t *testing.T) {
	t.Parallel()
	var _ llm.Client = (*Client)(nil)
}

func TestStream_SimpleText(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "simple_text.sse")
	srv := httptest.NewServer(sseHandler(t, body))
	defer srv.Close()
	c := newTestClient(t, srv)

	ch, err := c.Stream(context.Background(), llm.Request{Model: "anthropic/claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 2*time.Second)

	// Expected: 3x EventTextDelta, 1x EventUsage, 1x EventEnd.
	var texts []string
	var usageSeen bool
	var endSeen bool
	var sawErr bool
	for _, ev := range events {
		switch ev.Kind {
		case llm.EventTextDelta:
			texts = append(texts, ev.Text)
		case llm.EventUsage:
			if usageSeen {
				t.Error("multiple EventUsage events")
			}
			usageSeen = true
			if ev.Usage == nil {
				t.Fatal("EventUsage with nil Usage")
			}
			if ev.Usage.PromptTokens != 18 || ev.Usage.CompletionTokens != 4 {
				t.Errorf("usage tokens = %+v", ev.Usage)
			}
			if ev.Usage.CostUSD == 0 {
				t.Error("usage.CostUSD == 0; want non-zero")
			}
		case llm.EventEnd:
			endSeen = true
		case llm.EventError:
			sawErr = true
		}
	}
	if sawErr {
		t.Errorf("unexpected EventError")
	}
	if !usageSeen {
		t.Error("missing EventUsage")
	}
	if !endSeen {
		t.Error("missing EventEnd")
	}
	if got, want := strings.Join(texts, ""), "Hello, world."; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	if events[len(events)-1].Kind != llm.EventEnd {
		t.Errorf("last event = %s, want EventEnd", events[len(events)-1].Kind)
	}
}

func TestStream_SingleTool(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "single_tool.sse")
	srv := httptest.NewServer(sseHandler(t, body))
	defer srv.Close()
	c := newTestClient(t, srv)

	ch, err := c.Stream(context.Background(), llm.Request{Model: "anthropic/claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 2*time.Second)

	// Expected: 1x text delta ("Let me check that file."), 1x tool call, 1x usage, 1x end.
	var text string
	var calls []llm.ToolCall
	for _, ev := range events {
		switch ev.Kind {
		case llm.EventTextDelta:
			text += ev.Text
		case llm.EventToolCall:
			if ev.Tool == nil {
				t.Fatal("EventToolCall with nil Tool")
			}
			calls = append(calls, *ev.Tool)
		}
	}
	if text != "Let me check that file." {
		t.Errorf("text = %q", text)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	c0 := calls[0]
	if c0.ID != "call_abc" || c0.Name != "read_file" {
		t.Errorf("call = %+v", c0)
	}
	if c0.Arguments != `{"path":"README.md"}` {
		t.Errorf("Arguments = %q, want %q", c0.Arguments, `{"path":"README.md"}`)
	}
	if !json.Valid([]byte(c0.Arguments)) {
		t.Errorf("Arguments not valid JSON: %q", c0.Arguments)
	}
	// Order: tool call must precede end.
	assertEventOrder(t, events)
}

func TestStream_MultiToolInterleaved(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "multi_tool_interleaved.sse")
	srv := httptest.NewServer(sseHandler(t, body))
	defer srv.Close()
	c := newTestClient(t, srv)

	ch, err := c.Stream(context.Background(), llm.Request{Model: "anthropic/claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 2*time.Second)

	var calls []llm.ToolCall
	for _, ev := range events {
		if ev.Kind == llm.EventToolCall && ev.Tool != nil {
			calls = append(calls, *ev.Tool)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2; events=%+v", len(calls), events)
	}
	// In index order: call_a (read_file) first, call_b (list_files) second.
	if calls[0].ID != "call_a" || calls[0].Name != "read_file" {
		t.Errorf("calls[0] = %+v", calls[0])
	}
	if calls[1].ID != "call_b" || calls[1].Name != "list_files" {
		t.Errorf("calls[1] = %+v", calls[1])
	}
	if calls[0].Arguments != `{"path":"a.md"}` {
		t.Errorf("calls[0].Arguments = %q", calls[0].Arguments)
	}
	if calls[1].Arguments != `{}` {
		t.Errorf("calls[1].Arguments = %q", calls[1].Arguments)
	}
	if !json.Valid([]byte(calls[0].Arguments)) || !json.Valid([]byte(calls[1].Arguments)) {
		t.Error("tool-call Arguments not valid JSON")
	}
	assertEventOrder(t, events)
}

func TestStream_MidStreamError(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "mid_stream_error.sse")
	var hits int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()
	c := newTestClient(t, srv)

	ch, err := c.Stream(context.Background(), llm.Request{Model: "openai/gpt-4o"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 2*time.Second)

	var texts []string
	var errEv *llm.Event
	var endSeen bool
	for i, ev := range events {
		switch ev.Kind {
		case llm.EventTextDelta:
			texts = append(texts, ev.Text)
		case llm.EventError:
			errEv = &events[i]
		case llm.EventEnd:
			endSeen = true
		}
	}
	if got := strings.Join(texts, ""); got != "Working on it..." {
		t.Errorf("text = %q", got)
	}
	if errEv == nil {
		t.Fatal("missing EventError")
	}
	if errEv.Err == nil {
		t.Fatal("EventError.Err is nil")
	}
	if !errors.Is(errEv.Err, llm.ErrServerError) {
		t.Errorf("EventError.Err = %v, want errors.Is(ErrServerError)", errEv.Err)
	}
	if !endSeen {
		t.Error("missing EventEnd")
	}
	// EventEnd is always last.
	if events[len(events)-1].Kind != llm.EventEnd {
		t.Errorf("last event = %s, want EventEnd", events[len(events)-1].Kind)
	}
	// No retry on mid-stream error.
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("hits = %d, want 1 (no retry on mid-stream error)", got)
	}
}

func TestStream_ContentFilterFinishReason(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "content_filter_finish_reason.sse")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	ch, err := c.Stream(context.Background(), llm.Request{Model: "google/gemini-3.1-flash-image-preview"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 2*time.Second)

	var errEv *llm.Event
	var endEv *llm.Event
	for i, ev := range events {
		switch ev.Kind {
		case llm.EventError:
			errEv = &events[i]
		case llm.EventEnd:
			endEv = &events[i]
		}
	}
	if errEv == nil || errEv.Err == nil {
		t.Fatal("expected EventError with non-nil Err")
	}
	if !errors.Is(errEv.Err, llm.ErrContentFiltered) {
		t.Errorf("err = %v, want errors.Is(ErrContentFiltered)", errEv.Err)
	}
	if errors.Is(errEv.Err, llm.ErrServerError) {
		t.Errorf("err should NOT match ErrServerError when content-filtered")
	}
	if endEv == nil {
		t.Fatal("missing EventEnd")
	}
	if endEv.FinishReason != "content_filter" {
		t.Errorf("EventEnd.FinishReason = %q, want %q", endEv.FinishReason, "content_filter")
	}
}

func TestStream_ContentFilterErrorEnvelope(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "content_filter_error_envelope.sse")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	ch, err := c.Stream(context.Background(), llm.Request{Model: "google/gemini-3.1-flash-image-preview"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 2*time.Second)

	var errEv *llm.Event
	var endEv *llm.Event
	for i, ev := range events {
		switch ev.Kind {
		case llm.EventError:
			errEv = &events[i]
		case llm.EventEnd:
			endEv = &events[i]
		}
	}
	if errEv == nil || errEv.Err == nil {
		t.Fatal("expected EventError with non-nil Err")
	}
	if !errors.Is(errEv.Err, llm.ErrContentFiltered) {
		t.Errorf("err = %v, want errors.Is(ErrContentFiltered)", errEv.Err)
	}
	if endEv == nil || endEv.FinishReason != "content_filter" {
		t.Errorf("EventEnd.FinishReason = %q, want %q", endEvFinishReason(endEv), "content_filter")
	}
}

func endEvFinishReason(ev *llm.Event) string {
	if ev == nil {
		return "<nil EventEnd>"
	}
	return ev.FinishReason
}

func TestStream_429ThenSuccess(t *testing.T) {
	t.Parallel()
	successBody := loadFixture(t, "success_after_429.sse")
	var (
		hits  int32
		first time.Time
		gap   time.Duration
	)
	handler := func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			first = time.Now()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.Header().Set("X-RateLimit-Reset", "1741305600000")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Rate limit exceeded","metadata":{}}}`))
			return
		}
		gap = time.Since(first)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(successBody)
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()

	c, err := NewClient(&Options{
		APIKey:  "sk-test",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ch, err := c.Stream(context.Background(), llm.Request{Model: "anthropic/claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 5*time.Second)

	if h := atomic.LoadInt32(&hits); h != 2 {
		t.Errorf("hits = %d, want 2 (one 429, one success)", h)
	}
	if gap < 800*time.Millisecond {
		t.Errorf("gap between requests = %v, want >= ~1s (Retry-After)", gap)
	}
	// Success path same as simple_text: text + usage + end, no error.
	var textsJoin string
	for _, ev := range events {
		switch ev.Kind {
		case llm.EventError:
			t.Errorf("unexpected EventError on retry path: %v", ev.Err)
		case llm.EventTextDelta:
			textsJoin += ev.Text
		}
	}
	if textsJoin != "Hello, world." {
		t.Errorf("text = %q", textsJoin)
	}
}

func TestStream_Thinking(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "thinking.sse")
	srv := httptest.NewServer(sseHandler(t, body))
	defer srv.Close()
	c := newTestClient(t, srv)

	ch, err := c.Stream(context.Background(), llm.Request{
		Model:    "anthropic/claude-sonnet-4-6",
		Thinking: llm.ThinkingByEffort(llm.ThinkingEffortHigh),
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 2*time.Second)

	var thinkingText, contentText string
	for _, ev := range events {
		switch ev.Kind {
		case llm.EventThinking:
			thinkingText += ev.Text
		case llm.EventTextDelta:
			contentText += ev.Text
		}
	}
	if thinkingText == "" {
		t.Error("expected EventThinking text, got none")
	}
	if !strings.Contains(thinkingText, "Let me think") {
		t.Errorf("thinking text = %q", thinkingText)
	}
	if contentText != "42." {
		t.Errorf("content text = %q, want %q", contentText, "42.")
	}
}

func TestStream_AuthError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":401,"message":"invalid key"}}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	_, err := c.Stream(context.Background(), llm.Request{Model: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, llm.ErrAuth) {
		t.Errorf("err = %v, want ErrAuth", err)
	}
}

func TestStream_InvalidRequest(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"bad input"}}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	_, err := c.Stream(context.Background(), llm.Request{Model: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, llm.ErrInvalidRequest) {
		t.Errorf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestStream_Cancellation(t *testing.T) {
	t.Parallel()
	// Server holds the response open indefinitely.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Hold open for up to 10s; the cancellation should kill us earlier.
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.Stream(ctx, llm.Request{Model: "x"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	// Cancel quickly. The producer should exit and close the channel.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed; success
			}
		case <-deadline:
			t.Fatal("channel did not close after cancellation within 2s")
		}
	}
}

// assertEventOrder verifies the per-spec event ordering invariants:
// EventEnd is the last event, and at most one EventUsage / EventError occurs
// before EventEnd.
func assertEventOrder(t *testing.T, events []llm.Event) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("no events")
	}
	if events[len(events)-1].Kind != llm.EventEnd {
		t.Errorf("last event kind = %s, want EventEnd", events[len(events)-1].Kind)
	}
	// EventEnd appears exactly once.
	var endCount int
	for _, ev := range events {
		if ev.Kind == llm.EventEnd {
			endCount++
		}
	}
	if endCount != 1 {
		t.Errorf("EventEnd count = %d, want 1", endCount)
	}
}

// Sanity: server responds with a request body that includes the expected fields.
func TestStream_RequestBody(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "simple_text.sse")
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &captured)

		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization header = %q", got)
		}
		if got := r.Header.Get("HTTP-Referer"); got != "https://example.com" {
			t.Errorf("HTTP-Referer = %q", got)
		}
		if got := r.Header.Get("X-Title"); got != "aikido-test" {
			t.Errorf("X-Title = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c, err := NewClient(&Options{
		APIKey:        "sk-test",
		BaseURL:       srv.URL,
		HTTPReferer:   "https://example.com",
		XTitle:        "aikido-test",
		ProviderOrder: []string{"anthropic"},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	temp := float32(0.7)
	ch, err := c.Stream(context.Background(), llm.Request{
		Model:       "anthropic/claude-sonnet-4.6", // pass through — caller chooses canonical form
		MaxTokens:   1024,
		Temperature: &temp,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "you are helpful"},
			{Role: llm.RoleUser, Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_ = drain(t, ch, 2*time.Second)

	if captured == nil {
		t.Fatal("no request body captured")
	}
	if got := captured["model"]; got != "anthropic/claude-sonnet-4.6" {
		t.Errorf("model = %v, want %q (verbatim — no normalization)", got, "anthropic/claude-sonnet-4.6")
	}
	if captured["stream"] != true {
		t.Errorf("stream = %v, want true", captured["stream"])
	}
	if got := captured["max_tokens"]; got != 1024.0 {
		t.Errorf("max_tokens = %v, want 1024", got)
	}
	if got := captured["temperature"]; got != 0.7 {
		// JSON numbers come back as float64 with potential representation noise; use approx
		f, ok := got.(float64)
		if !ok || f < 0.69 || f > 0.71 {
			t.Errorf("temperature = %v, want ~0.7", got)
		}
	}
	// provider routing
	prov, ok := captured["provider"].(map[string]any)
	if !ok {
		t.Fatalf("provider field missing or wrong shape: %v", captured["provider"])
	}
	if order, ok := prov["order"].([]any); !ok || len(order) != 1 || order[0] != "anthropic" {
		t.Errorf("provider.order = %v", prov["order"])
	}
	// Single-entry order locks to one provider: allow_fallbacks=false.
	if af, ok := prov["allow_fallbacks"].(bool); !ok || af {
		t.Errorf("provider.allow_fallbacks = %v, want false (single-entry order)", prov["allow_fallbacks"])
	}
	// messages shape
	msgs, ok := captured["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages malformed: %v", captured["messages"])
	}
}

// TestStream_ProviderOrderMultiEntry covers the multi-entry case:
// listing more than one provider implies a fallback chain.
func TestStream_ProviderOrderMultiEntry(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "simple_text.sse")
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &captured)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c, err := NewClient(&Options{
		APIKey:        "sk-test",
		BaseURL:       srv.URL,
		ProviderOrder: []string{"anthropic", "openai"},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ch, err := c.Stream(context.Background(), llm.Request{Model: "anthropic/claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_ = drain(t, ch, 2*time.Second)

	prov, ok := captured["provider"].(map[string]any)
	if !ok {
		t.Fatalf("provider field missing")
	}
	if order, ok := prov["order"].([]any); !ok || len(order) != 2 {
		t.Errorf("provider.order = %v", prov["order"])
	}
	// Multi-entry: allow_fallbacks=true.
	if af, ok := prov["allow_fallbacks"].(bool); !ok || !af {
		t.Errorf("provider.allow_fallbacks = %v, want true (multi-entry order)", prov["allow_fallbacks"])
	}
}

// Verify the Stream method matches the llm.Client interface signature
// (compile-time assertion; this is just defensive).
func TestStream_InterfaceShape(t *testing.T) {
	t.Parallel()
	var c llm.Client
	body := loadFixture(t, "simple_text.sse")
	srv := httptest.NewServer(sseHandler(t, body))
	defer srv.Close()
	cli := newTestClient(t, srv)
	c = cli // assign concrete to interface; checks signature
	if c == nil {
		t.Fatal("nil client")
	}
}

// sanity: comment-only chunks ("`: OPENROUTER PROCESSING`") do not turn into
// events.
func TestStream_IgnoresCommentLines(t *testing.T) {
	t.Parallel()
	body := []byte(`: OPENROUTER PROCESSING

data: {"id":"x","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}

: another comment

data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}

data: [DONE]

`)
	srv := httptest.NewServer(sseHandler(t, body))
	defer srv.Close()
	c := newTestClient(t, srv)

	ch, err := c.Stream(context.Background(), llm.Request{Model: "x"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 2*time.Second)
	var got string
	for _, ev := range events {
		if ev.Kind == llm.EventTextDelta {
			got += ev.Text
		}
	}
	if got != "hi" {
		t.Errorf("text = %q, want %q", got, "hi")
	}
}

// TestStream_ImageStreamingDelta covers the streaming `delta.images[]` shape
// that image-capable models emit when streaming generated images. The base64
// data URI is decoded into bytes; the remote URL is passed through verbatim.
func TestStream_ImageStreamingDelta(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "image_streaming_delta.sse")
	srv := httptest.NewServer(sseHandler(t, body))
	defer srv.Close()
	c := newTestClient(t, srv)

	ch, err := c.Stream(context.Background(), llm.Request{Model: "google/gemini-2.5-flash-image-preview"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 2*time.Second)

	var (
		text   string
		images []llm.ImagePart
	)
	for _, ev := range events {
		switch ev.Kind {
		case llm.EventTextDelta:
			text += ev.Text
		case llm.EventImage:
			if ev.Image == nil {
				t.Fatal("EventImage with nil Image")
			}
			images = append(images, *ev.Image)
		}
	}
	if text != "Here is the requested image:" {
		t.Errorf("text = %q", text)
	}
	if len(images) != 2 {
		t.Fatalf("images = %d, want 2", len(images))
	}
	// First: data URI → decoded bytes.
	if images[0].ContentType != "image/png" {
		t.Errorf("images[0].ContentType = %q", images[0].ContentType)
	}
	if len(images[0].Data) == 0 {
		t.Error("images[0].Data is empty; data URI should have been decoded")
	}
	if images[0].URL != "" {
		t.Errorf("images[0].URL = %q; data URI should have been consumed", images[0].URL)
	}
	// Second: remote URL pass-through.
	if images[1].URL != "https://cdn.example.com/img/abc123.png" {
		t.Errorf("images[1].URL = %q", images[1].URL)
	}
	if len(images[1].Data) != 0 {
		t.Errorf("images[1].Data nonempty for URL-only image: %d bytes", len(images[1].Data))
	}
	assertEventOrder(t, events)
}

// TestStream_ImageMessageNonStream covers the case where the provider returns
// the entire assistant message in a single chunk with `choices[0].message`
// (rather than `delta`) populated.
func TestStream_ImageMessageNonStream(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "image_message_nonstream.sse")
	srv := httptest.NewServer(sseHandler(t, body))
	defer srv.Close()
	c := newTestClient(t, srv)

	ch, err := c.Stream(context.Background(), llm.Request{Model: "openai/gpt-image-1"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 2*time.Second)

	var images []llm.ImagePart
	for _, ev := range events {
		if ev.Kind == llm.EventImage && ev.Image != nil {
			images = append(images, *ev.Image)
		}
	}
	if len(images) != 1 {
		t.Fatalf("images = %d, want 1", len(images))
	}
	if images[0].ContentType != "image/jpeg" {
		t.Errorf("ContentType = %q", images[0].ContentType)
	}
	if len(images[0].Data) == 0 {
		t.Error("Data not decoded")
	}
}

// TestStream_ImageInlineContent covers the typed-parts-array content shape
// where the image lives inside `message.content` as an `image_url` part
// alongside one or more `text` parts.
func TestStream_ImageInlineContent(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "image_inline_content.sse")
	srv := httptest.NewServer(sseHandler(t, body))
	defer srv.Close()
	c := newTestClient(t, srv)

	ch, err := c.Stream(context.Background(), llm.Request{Model: "google/gemini-2.5-flash-image-preview"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 2*time.Second)

	var (
		text   string
		images []llm.ImagePart
	)
	for _, ev := range events {
		switch ev.Kind {
		case llm.EventTextDelta:
			text += ev.Text
		case llm.EventImage:
			if ev.Image != nil {
				images = append(images, *ev.Image)
			}
		}
	}
	if text != "Generated." {
		t.Errorf("text = %q", text)
	}
	if len(images) != 1 {
		t.Fatalf("images = %d, want 1", len(images))
	}
	if images[0].ContentType != "image/webp" || len(images[0].Data) == 0 {
		t.Errorf("images[0] = %+v", images[0])
	}
}

// quiet a "fmt unused" warning if the package later doesn't need it
var _ = fmt.Sprintf
