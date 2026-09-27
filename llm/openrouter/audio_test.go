package openrouter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mxcd/aikido/llm"
)

func TestAudioInputOnWire(t *testing.T) {
	t.Parallel()
	clip := []byte("\x1aE\xdf\xa3 webm bytes")
	req := llm.Request{
		Model: "google/gemini-3.1-flash-lite",
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: "transcribe",
			Audio:   []llm.AudioPart{{Data: clip, Format: "webm"}},
		}},
	}
	for _, stream := range []bool{true, false} {
		var captured []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured, _ = io.ReadAll(r.Body)
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				return
			}
			_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
		}))
		c := newTestClient(t, srv)
		var text string
		var err error
		if stream {
			text, _, _, _, err = llm.Collect(context.Background(), c, req)
		} else {
			var resp llm.Response
			resp, err = c.Complete(context.Background(), req)
			text = resp.Text
		}
		srv.Close()
		if err != nil || text != "hi" {
			t.Fatalf("stream=%v: text %q err %v", stream, text, err)
		}

		var body struct {
			Messages []struct {
				Content []struct {
					Type       string `json:"type"`
					Text       string `json:"text"`
					InputAudio *struct {
						Data   string `json:"data"`
						Format string `json:"format"`
					} `json:"input_audio"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(captured, &body); err != nil {
			t.Fatalf("stream=%v: %v: %s", stream, err, captured)
		}
		parts := body.Messages[0].Content
		if len(parts) != 2 || parts[0].Type != "text" || parts[0].Text != "transcribe" {
			t.Fatalf("stream=%v: parts = %+v", stream, parts)
		}
		a := parts[1]
		if a.Type != "input_audio" || a.InputAudio == nil || a.InputAudio.Format != "webm" || a.InputAudio.Data != base64.StdEncoding.EncodeToString(clip) {
			t.Errorf("stream=%v: audio part = %+v", stream, a)
		}
	}
}
