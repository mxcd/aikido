package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/mxcd/aikido/llm"
)

func newSpeechTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(&Options{APIKey: "sk-test", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestSpeak_RequestAndResponse(t *testing.T) {
	t.Parallel()
	audio := []byte("ID3\x04fake-mp3")
	var (
		gotPath, gotAccept string
		gotBody            map[string]any
		hits               int32
	)
	c := newSpeechTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// First attempt fails retryably; the second must carry the same body.
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"code":502,"message":"upstream failed"}}`))
			return
		}
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("X-Generation-Id", "gen-tts-1")
		_, _ = w.Write(audio)
	})

	resp, err := c.Speak(context.Background(), llm.SpeechRequest{
		Model:  "elevenlabs/eleven-v4-turbo",
		Input:  "Hallo Welt.",
		Voice:  "sarah",
		Format: "mp3",
		Speed:  llm.Float32(1.1),
	})
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if hits != 2 {
		t.Errorf("attempts = %d, want 2", hits)
	}
	if gotPath != "/audio/speech" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAccept == "text/event-stream" {
		t.Errorf("Accept = %q, want audio", gotAccept)
	}
	want := map[string]any{"model": "elevenlabs/eleven-v4-turbo", "input": "Hallo Welt.", "voice": "sarah", "response_format": "mp3"}
	for k, v := range want {
		if gotBody[k] != v {
			t.Errorf("body[%s] = %v, want %v", k, gotBody[k], v)
		}
	}
	if s, _ := gotBody["speed"].(float64); s < 1.09 || s > 1.11 {
		t.Errorf("body[speed] = %v", gotBody["speed"])
	}
	if !bytes.Equal(resp.Audio, audio) || resp.ContentType != "audio/mpeg" || resp.GenerationID != "gen-tts-1" {
		t.Errorf("resp = %q %q %q", resp.Audio, resp.ContentType, resp.GenerationID)
	}
}

func TestSpeak_OptionalFieldsOmitted(t *testing.T) {
	t.Parallel()
	var gotBody map[string]any
	c := newSpeechTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "audio/pcm")
		_, _ = w.Write([]byte{0, 1})
	})
	if _, err := c.Speak(context.Background(), llm.SpeechRequest{Model: "m", Input: "x"}); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if len(gotBody) != 2 {
		t.Errorf("body = %v, want only model and input", gotBody)
	}
}

func TestSpeak_Errors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		req    llm.SpeechRequest
		status int
		ct     string
		body   string
		want   error
	}{
		{"no input", llm.SpeechRequest{Model: "m"}, 0, "", "", llm.ErrInvalidRequest},
		{"no model", llm.SpeechRequest{Input: "x"}, 0, "", "", llm.ErrInvalidRequest},
		{"bad voice", llm.SpeechRequest{Model: "m", Input: "x", Voice: "nope"}, 400, "application/json", `{"error":{"code":400,"message":"unknown voice"}}`, llm.ErrInvalidRequest},
		{"no credits", llm.SpeechRequest{Model: "m", Input: "x"}, 402, "application/json", `{"error":{"code":402,"message":"insufficient credits"}}`, llm.ErrAuth},
		{"json on 200", llm.SpeechRequest{Model: "m", Input: "x"}, 200, "application/json", `{"error":{"code":500,"message":"boom"}}`, llm.ErrServerError},
		{"empty audio", llm.SpeechRequest{Model: "m", Input: "x"}, 200, "audio/mpeg", "", llm.ErrServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var hits int32
			c := newSpeechTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&hits, 1)
				w.Header().Set("Content-Type", tc.ct)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := c.Speak(context.Background(), tc.req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.status == 0 && hits != 0 {
				t.Errorf("invalid request reached the server")
			}
			if tc.status >= 400 && hits != 1 {
				t.Errorf("attempts = %d, want 1 (no retry)", hits)
			}
		})
	}
}
