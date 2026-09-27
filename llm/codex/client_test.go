package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mxcd/aikido/llm"
)

func newClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	src := NewTokenSource(Tokens{AccessToken: "at", AccountID: "acct", ExpiresAt: time.Now().Add(time.Hour)}, nil, nil)
	c, err := NewClient(&Options{Tokens: src, Originator: "casa-mapa", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		var probe struct{ Type string }
		_ = json.Unmarshal([]byte(e), &probe)
		b.WriteString("event: " + probe.Type + "\ndata: " + e + "\n\n")
	}
	return b.String()
}

func TestComplete_RequestShapeAndText(t *testing.T) {
	t.Parallel()
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		for h, want := range map[string]string{"Authorization": "Bearer at", "ChatGPT-Account-ID": "acct", "originator": "casa-mapa", "Accept": "text/event-stream"} {
			if got := r.Header.Get(h); got != want {
				t.Errorf("header %s = %q, want %q", h, got, want)
			}
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/responses" || body["stream"] != true || body["store"] != false || body["instructions"] != "be brief" {
			t.Errorf("path %s body %v", r.URL.Path, body)
		}
		if _, ok := body["max_output_tokens"]; ok {
			t.Error("max_output_tokens must not be sent")
		}
		content := body["input"].([]any)[0].(map[string]any)["content"].([]any)
		if img := content[1].(map[string]any); img["type"] != "input_image" || !strings.HasPrefix(img["image_url"].(string), "data:image/png;base64,") {
			t.Errorf("image part = %v", img)
		}
		if f := body["text"].(map[string]any)["format"].(map[string]any); f["type"] != "json_schema" || f["name"] != "meal" || f["strict"] != true {
			t.Errorf("text.format = %v", f)
		}
		_, _ = io.WriteString(w, sse(
			`{"type":"response.output_text.delta","delta":"{\"kcal\":"}`,
			`{"type":"response.output_text.delta","delta":"640}"}`,
			`{"type":"response.output_text.done","text":"{\"kcal\":640}"}`,
			`{"type":"response.completed","response":{"output":[],"usage":{"input_tokens":900,"output_tokens":12}}}`,
		))
	})
	res, err := c.Complete(context.Background(), llm.Request{
		Model: "gpt-5.5",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "be brief"},
			{Role: llm.RoleUser, Content: "what is this?", Images: []llm.ImagePart{{Data: []byte("\x89PNG\r\n\x1a\n0000"), ContentType: "image/png"}}},
		},
		ResponseFormat: &llm.JSONSchema{Name: "meal", Strict: true, Schema: json.RawMessage(`{"type":"object"}`)},
		MaxTokens:      500,
	})
	if err != nil || res.Text != `{"kcal":640}` || res.Usage == nil || res.Usage.PromptTokens != 900 || res.FinishReason != "stop" {
		t.Fatalf("Complete = %+v, %v", res, err)
	}
}

func TestStream_DoneWithoutDeltasAndFailures(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body    string
		text    string
		wantErr error
	}{
		"done only":   {sse(`{"type":"response.output_text.done","text":"hi"}`, `{"type":"response.completed","response":{}}`), "hi", nil},
		"failed":      {sse(`{"type":"response.failed","response":{"error":{"code":"server_error","message":"boom"}}}`), "", llm.ErrServerError},
		"cut off":     {sse(`{"type":"response.output_text.delta","delta":"par"}`), "par", llm.ErrServerError},
		"error event": {sse(`{"type":"error","code":"content_policy","message":"no"}`), "", llm.ErrContentFiltered},
	} {
		c := newClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.body) })
		res, err := c.Complete(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
		errOK := (tc.wantErr == nil && err == nil) || (tc.wantErr != nil && errors.Is(err, tc.wantErr))
		if res.Text != tc.text || !errOK {
			t.Errorf("%s: text %q err %v", name, res.Text, err)
		}
	}
}

func TestStream_RejectsToolsAndMapsStatus(t *testing.T) {
	t.Parallel()
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	if _, err := c.Stream(context.Background(), llm.Request{Tools: []llm.ToolDef{{Name: "x"}}}); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Errorf("tools: %v", err)
	}
	audio := llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Audio: []llm.AudioPart{{Data: []byte{1}, Format: "wav"}}}}}
	if _, err := c.Stream(context.Background(), audio); !errors.Is(err, llm.ErrInvalidRequest) || !strings.Contains(err.Error(), "audio input is not supported") {
		t.Errorf("audio: %v", err)
	}
	if _, err := c.Stream(context.Background(), llm.Request{Model: "m"}); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Errorf("400: %v", err)
	}
}

func TestModels_SkipsHidden(t *testing.T) {
	t.Parallel()
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.URL.Query().Get("client_version") == "" || r.Header.Get("ChatGPT-Account-ID") != "acct" {
			t.Errorf("models request %s", r.URL)
		}
		_, _ = io.WriteString(w, `{"models":[{"slug":"gpt-5.5","visibility":"list"},{"slug":"gpt-reserve","visibility":"hide"},{"slug":"gpt-6-astra"}]}`)
	})
	ids, err := c.Models(context.Background())
	if err != nil || strings.Join(ids, ",") != "gpt-5.5,gpt-6-astra" {
		t.Fatalf("Models = %v, %v", ids, err)
	}
}
