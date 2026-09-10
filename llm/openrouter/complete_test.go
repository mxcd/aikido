package openrouter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mxcd/aikido/llm"
)

// jsonHandler returns an http.HandlerFunc that writes the given JSON bytes
// with status 200 and Content-Type: application/json.
func jsonHandler(t *testing.T, body []byte) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

func TestComplete_SimpleText(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"id": "gen-test",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {"role": "assistant", "content": "Hello, world."},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 18, "completion_tokens": 4, "total_tokens": 22, "cost": 0.00012}
	}`)
	srv := httptest.NewServer(jsonHandler(t, body))
	defer srv.Close()
	c := newTestClient(t, srv)

	resp, err := c.Complete(context.Background(), llm.Request{Model: "anthropic/claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text != "Hello, world." {
		t.Errorf("text = %q, want %q", resp.Text, "Hello, world.")
	}
	if resp.FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want %q", resp.FinishReason, "stop")
	}
	if resp.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if resp.Usage.PromptTokens != 18 || resp.Usage.CompletionTokens != 4 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Usage.CostUSD == 0 {
		t.Error("CostUSD == 0; want non-zero")
	}
}

// TestComplete_LargeImage proves Complete handles a single response carrying
// well over 1 MiB of base64 image data — exactly the case that breaks Stream
// because of the SSE per-line scanner cap. Generates ~3 MiB of base64 payload
// (≈2.25 MB raw "PNG"), wraps it in the OpenRouter image-output envelope, and
// asserts the bytes round-trip end-to-end.
func TestComplete_LargeImage(t *testing.T) {
	t.Parallel()

	// 2.25 MB random-ish PNG-shaped bytes (signature + filler). Base64 of this
	// alone is ~3 MB — comfortably above the 1 MiB SSE line cap that breaks
	// Stream-based image-gen against real Gemini responses.
	const rawSize = 2_250_000
	rawData := make([]byte, rawSize)
	// Plausible PNG signature so a downstream sniffer wouldn't reject it.
	copy(rawData, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A})
	// Fill the rest with non-zero bytes so base64 doesn't compress to nothing.
	for i := 8; i < rawSize; i++ {
		rawData[i] = byte(i % 251)
	}
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(rawData)

	envelope := map[string]any{
		"id":     "gen-image-test",
		"object": "chat.completion",
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": "Here is your image.",
				"images": []map[string]any{{
					"type":      "image_url",
					"image_url": map[string]any{"url": dataURI},
				}},
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     18,
			"completion_tokens": 1290,
			"total_tokens":      1308,
			"cost":              0.00038,
		},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	// Sanity check: envelope must comfortably exceed the 1 MiB SSE per-line cap
	// to prove the non-streaming path handles what Stream cannot.
	if len(body) < 1500*1024 {
		t.Fatalf("envelope size = %d, want > 1.5 MiB to exercise the SSE cap", len(body))
	}

	srv := httptest.NewServer(jsonHandler(t, body))
	defer srv.Close()
	c := newTestClient(t, srv)

	resp, err := c.Complete(context.Background(), llm.Request{Model: "google/gemini-3.1-flash-image-preview"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text != "Here is your image." {
		t.Errorf("text = %q", resp.Text)
	}
	if len(resp.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(resp.Images))
	}
	got := resp.Images[0]
	if got.ContentType != "image/png" {
		t.Errorf("contentType = %q, want image/png", got.ContentType)
	}
	if !bytes.Equal(got.Data, rawData) {
		t.Errorf("image bytes round-trip failed: got %d bytes, want %d", len(got.Data), len(rawData))
	}
	if resp.FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", resp.FinishReason)
	}
}

// TestComplete_LargeImage_ProvesStreamingBreaks pins the failure mode the
// non-streaming path was added to fix: feeding the same large image envelope
// through the SSE scanner deterministically returns ErrServerError. If aikido
// ever bumps the per-line buffer to handle arbitrary sizes, this test will
// flag the change so the contract is reconsidered.
func TestComplete_LargeImage_ProvesStreamingBreaks(t *testing.T) {
	t.Parallel()
	// 2.25 MB raw → ~3 MB base64 — same setup as TestComplete_LargeImage.
	const rawSize = 2_250_000
	rawData := make([]byte, rawSize)
	for i := range rawData {
		rawData[i] = byte(i % 251)
	}
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(rawData)
	envelope := map[string]any{
		"id":     "gen-image-test",
		"object": "chat.completion.chunk",
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]any{
				"role": "assistant",
				"images": []map[string]any{{
					"type":      "image_url",
					"image_url": map[string]any{"url": dataURI},
				}},
			},
			"finish_reason": "stop",
		}},
	}
	chunkJSON, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	// SSE wrapping: one `data: <json>\n\n` line then [DONE].
	sse := []byte("data: ")
	sse = append(sse, chunkJSON...)
	sse = append(sse, []byte("\n\ndata: [DONE]\n\n")...)

	srv := httptest.NewServer(sseHandler(t, sse))
	defer srv.Close()
	c := newTestClient(t, srv)

	ch, err := c.Stream(context.Background(), llm.Request{Model: "google/gemini-3.1-flash-image-preview"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, ch, 5*time.Second)

	var sawErr bool
	for _, ev := range events {
		if ev.Kind == llm.EventError && errors.Is(ev.Err, llm.ErrServerError) {
			sawErr = true
		}
	}
	if !sawErr {
		t.Errorf("expected SSE path to fail with ErrServerError on >1 MiB line, but it succeeded; events=%d", len(events))
	}
}

func TestComplete_ToolCalls(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"id": "gen-tool",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\":\"a.md\"}"}}
				]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 30, "completion_tokens": 12, "total_tokens": 42}
	}`)
	srv := httptest.NewServer(jsonHandler(t, body))
	defer srv.Close()
	c := newTestClient(t, srv)

	resp, err := c.Complete(context.Background(), llm.Request{Model: "anthropic/claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "read_file" {
		t.Errorf("tool_call = %+v", tc)
	}
	if tc.Arguments != `{"path":"a.md"}` {
		t.Errorf("tool_call.Arguments = %q", tc.Arguments)
	}
	if !json.Valid([]byte(tc.Arguments)) {
		t.Error("tool_call.Arguments not valid JSON")
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q", resp.FinishReason)
	}
}

func TestComplete_ContentFilterFinishReason(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"id": "gen-cf",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {"role": "assistant", "content": ""},
			"finish_reason": "content_filter"
		}]
	}`)
	srv := httptest.NewServer(jsonHandler(t, body))
	defer srv.Close()
	c := newTestClient(t, srv)

	resp, err := c.Complete(context.Background(), llm.Request{Model: "google/gemini-3.1-flash-image-preview"})
	if err == nil {
		t.Fatal("expected error for finish_reason=content_filter")
	}
	if !errors.Is(err, llm.ErrContentFiltered) {
		t.Errorf("err = %v, want errors.Is(ErrContentFiltered)", err)
	}
	if errors.Is(err, llm.ErrServerError) {
		t.Errorf("err should NOT match ErrServerError when content-filtered")
	}
	// Response is preserved so callers can log usage even on filtered turns.
	if resp.FinishReason != "content_filter" {
		t.Errorf("resp.FinishReason = %q", resp.FinishReason)
	}
}

func TestComplete_ContentFilterErrorEnvelope(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"error": {
			"code": "content_policy_violation",
			"message": "Content was blocked by safety policy"
		}
	}`)
	srv := httptest.NewServer(jsonHandler(t, body))
	defer srv.Close()
	c := newTestClient(t, srv)

	_, err := c.Complete(context.Background(), llm.Request{Model: "google/gemini-3.1-flash-image-preview"})
	if err == nil {
		t.Fatal("expected error for content-filter envelope")
	}
	if !errors.Is(err, llm.ErrContentFiltered) {
		t.Errorf("err = %v, want errors.Is(ErrContentFiltered)", err)
	}
}

func TestComplete_5xxRetryThenSuccess(t *testing.T) {
	t.Parallel()
	successBody := []byte(`{
		"id": "gen-after-503",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {"role": "assistant", "content": "ok"},
			"finish_reason": "stop"
		}]
	}`)
	var hits int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(successBody)
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()
	c := newTestClient(t, srv)

	resp, err := c.Complete(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text != "ok" {
		t.Errorf("text = %q", resp.Text)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("hits = %d, want 2 (one 503 + one success)", got)
	}
}

func TestComplete_4xxNoRetry(t *testing.T) {
	t.Parallel()
	var hits int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()
	c := newTestClient(t, srv)

	_, err := c.Complete(context.Background(), llm.Request{Model: "any"})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !errors.Is(err, llm.ErrAuth) {
		t.Errorf("err = %v, want ErrAuth", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("hits = %d, want 1 (no retry on 401)", got)
	}
}

func TestComplete_RequestBodyHasStreamFalse(t *testing.T) {
	t.Parallel()
	var captured []byte
	handler := func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`))
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()
	c := newTestClient(t, srv)

	if _, err := c.Complete(context.Background(), llm.Request{
		Model:    "any",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// stream:true must not be on the wire when calling Complete; omitempty on
	// the false case means the field is absent entirely.
	if bytes.Contains(captured, []byte(`"stream":true`)) {
		t.Errorf("body contains stream:true; want absent. body=%s", captured)
	}
}

func TestComplete_ImageConfigOnWire(t *testing.T) {
	t.Parallel()
	var captured []byte
	handler := func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()
	c := newTestClient(t, srv)

	if _, err := c.Complete(context.Background(), llm.Request{
		Model:       "any",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		Modalities:  []string{"image", "text"},
		ImageConfig: &llm.ImageConfig{AspectRatio: "16:9", ImageSize: "2K"},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !bytes.Contains(captured, []byte(`"image_config":{"aspect_ratio":"16:9","image_size":"2K"}`)) {
		t.Errorf("image_config not on wire. body=%s", captured)
	}

	// Nil ImageConfig must omit the field entirely.
	captured = nil
	if _, err := c.Complete(context.Background(), llm.Request{
		Model:    "any",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if bytes.Contains(captured, []byte(`image_config`)) {
		t.Errorf("image_config leaked into body when unset. body=%s", captured)
	}
}

func TestComplete_TypedPartsImageInContent(t *testing.T) {
	t.Parallel()
	// Some image-capable models emit the image as a typed-parts content array
	// instead of (or in addition to) the dedicated images[] field.
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkAAIAAAoAAggA9GkAAAAASUVORK5CYII="
	body := fmt.Appendf(nil, `{
		"id": "gen-typed-parts",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": [
					{"type": "text", "text": "Done."},
					{"type": "image_url", "image_url": {"url": "data:image/png;base64,%s"}}
				]
			},
			"finish_reason": "stop"
		}]
	}`, png)
	srv := httptest.NewServer(jsonHandler(t, body))
	defer srv.Close()
	c := newTestClient(t, srv)

	resp, err := c.Complete(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text != "Done." {
		t.Errorf("text = %q, want %q", resp.Text, "Done.")
	}
	if len(resp.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(resp.Images))
	}
	if resp.Images[0].ContentType != "image/png" {
		t.Errorf("contentType = %q", resp.Images[0].ContentType)
	}
	if len(resp.Images[0].Data) == 0 {
		t.Error("image data is empty; want decoded base64 bytes")
	}
}

func TestComplete_NetworkError(t *testing.T) {
	t.Parallel()
	// Server that closes the connection without responding.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("ResponseWriter does not support Hijacker")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.Complete(ctx, llm.Request{Model: "any"})
	if err == nil {
		t.Fatal("expected network error")
	}
	if !errors.Is(err, llm.ErrServerError) {
		t.Errorf("err = %v, want errors.Is(ErrServerError)", err)
	}
}

func TestComplete_MalformedJSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(jsonHandler(t, []byte(`{"this is not valid json`)))
	defer srv.Close()
	c := newTestClient(t, srv)

	_, err := c.Complete(context.Background(), llm.Request{Model: "any"})
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !errors.Is(err, llm.ErrServerError) {
		t.Errorf("err = %v, want ErrServerError wrap", err)
	}
}

func TestComplete_WireContract(t *testing.T) {
	t.Parallel()
	var (
		paths   []string
		headers []http.Header
		bodies  [][]byte
		hits    int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		paths = append(paths, r.URL.Path)
		headers = append(headers, r.Header.Clone())
		bodies = append(bodies, b)
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
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
	if _, err := c.Complete(context.Background(), llm.Request{
		Model:    "any",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if len(paths) != 2 {
		t.Fatalf("attempts = %d, want 2", len(paths))
	}
	for i, p := range paths {
		if p != "/chat/completions" {
			t.Errorf("attempt %d path = %q, want /chat/completions", i, p)
		}
	}
	for i, h := range headers {
		if got := h.Get("Accept"); got != "application/json" {
			t.Errorf("attempt %d Accept = %q", i, got)
		}
		if got := h.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("attempt %d Authorization = %q", i, got)
		}
		if got := h.Get("HTTP-Referer"); got != "https://github.com/mxcd/aikido" {
			t.Errorf("attempt %d HTTP-Referer = %q", i, got)
		}
		if got := h.Get("X-Title"); got != "aikido CLI" {
			t.Errorf("attempt %d X-Title = %q", i, got)
		}
	}
	if string(bodies[0]) != string(bodies[1]) {
		t.Errorf("retry sent a different body:\n%s\n%s", bodies[0], bodies[1])
	}
}

func TestComplete_ErrorEnvelopeNoRetry(t *testing.T) {
	t.Parallel()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":{"code":"server_error","message":"provider blew up"}}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	_, err := c.Complete(context.Background(), llm.Request{Model: "any"})
	if !errors.Is(err, llm.ErrServerError) {
		t.Fatalf("err = %v, want ErrServerError", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("hits = %d, want 1 (a 200 envelope is parsed outside the retry loop)", got)
	}
}

// trackedBody fails mid-read when failAt is reached and records its Close.
type trackedBody struct {
	data   []byte
	pos    int
	failAt int
	closed bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	if b.failAt >= 0 && b.pos >= b.failAt {
		return 0, errors.New("connection reset mid-body")
	}
	if b.pos >= len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.pos:])
	if b.failAt >= 0 && b.pos+n > b.failAt {
		n = b.failAt - b.pos
	}
	b.pos += n
	return n, nil
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

// recordingTransport serves canned responses in order and keeps the request
// bytes so the caller can assert body identity across retries.
type recordingTransport struct {
	bodies   [][]byte
	attempts int
	respond  func(attempt int) *http.Response
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(req.Body)
	rt.bodies = append(rt.bodies, b)
	rt.attempts++
	return rt.respond(rt.attempts), nil
}

// TestPostJSON_ReadFailureRetriesAndClosesBody pins the read-failure branch:
// a body that dies mid-read is an ErrServerError (so it retries), the retry
// resends identical bytes, and every response body is closed.
func TestPostJSON_ReadFailureRetriesAndClosesBody(t *testing.T) {
	t.Parallel()
	first := &trackedBody{data: []byte(`{"id":"x","choices":[]}`), failAt: 4}
	second := &trackedBody{data: []byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`), failAt: -1}
	rt := &recordingTransport{respond: func(attempt int) *http.Response {
		body := io.ReadCloser(second)
		if attempt == 1 {
			body = first
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       body,
		}
	}}
	c, err := NewClient(&Options{APIKey: "sk-test", BaseURL: "https://example.invalid/api/v1", HTTPClient: &http.Client{Transport: rt}})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := c.Complete(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text != "ok" {
		t.Errorf("text = %q, want ok", resp.Text)
	}
	if rt.attempts != 2 {
		t.Fatalf("attempts = %d, want 2", rt.attempts)
	}
	if string(rt.bodies[0]) != string(rt.bodies[1]) {
		t.Errorf("retry sent a different body:\n%s\n%s", rt.bodies[0], rt.bodies[1])
	}
	if !first.closed {
		t.Error("first response body was not closed before the retry")
	}
	if !second.closed {
		t.Error("last response body was not closed")
	}
}

// TestComplete_ChatImageFixture replays a recorded Gemini image response and
// asserts the chat path still carries modalities, image_config and reference
// images on the wire.
func TestComplete_ChatImageFixture(t *testing.T) {
	t.Parallel()
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(readFixture(t, "chat_image_gemini_nonstream.json"))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	resp, err := c.Complete(context.Background(), llm.Request{
		Model: "google/gemini-3.1-flash-image-preview",
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: "a red cube on white",
			Images:  []llm.ImagePart{{URL: "data:image/jpeg;base64,QUJD", ContentType: "image/jpeg"}},
		}},
		Modalities:  []string{"image", "text"},
		ImageConfig: &llm.ImageConfig{AspectRatio: "16:9", ImageSize: "2K"},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.Images) != 1 || len(resp.Images[0].Data) == 0 {
		t.Fatalf("images = %+v, want one inline image", resp.Images)
	}
	if !isPNG(resp.Images[0].Data) {
		t.Errorf("decoded bytes are not a PNG: %x", resp.Images[0].Data[:8])
	}
	if resp.Text != "Here is the image." {
		t.Errorf("text = %q", resp.Text)
	}

	var body struct {
		Modalities  []string `json:"modalities"`
		ImageConfig *struct {
			AspectRatio string `json:"aspect_ratio"`
			ImageSize   string `json:"image_size"`
		} `json:"image_config"`
		Messages []struct {
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL *struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatalf("decode request: %v (%s)", err, captured)
	}
	if len(body.Modalities) != 2 || body.Modalities[0] != "image" {
		t.Errorf("modalities = %v", body.Modalities)
	}
	if body.ImageConfig == nil || body.ImageConfig.AspectRatio != "16:9" || body.ImageConfig.ImageSize != "2K" {
		t.Errorf("image_config = %+v", body.ImageConfig)
	}
	if len(body.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(body.Messages))
	}
	parts := body.Messages[0].Content
	if len(parts) != 2 || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "data:image/jpeg;base64,QUJD" {
		t.Errorf("content parts = %+v", parts)
	}
}
