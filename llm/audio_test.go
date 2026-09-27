package llm_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/llmtest"
)

func TestAudioFormat(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"audio/webm;codecs=opus": "webm",
		"audio/webm":             "webm",
		"audio/mp4":              "m4a",
		"audio/x-m4a":            "m4a",
		"audio/aac":              "m4a",
		"audio/mpeg":             "mp3",
		"audio/wav":              "wav",
		"audio/x-wav":            "wav",
		"audio/ogg; codecs=opus": "ogg",
		"audio/flac":             "flac",
		"audio/x-aiff":           "aiff",
		"Audio/WebM":             "webm",
	} {
		if got, err := llm.AudioFormat(in); err != nil || got != want {
			t.Errorf("AudioFormat(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "video/webm", "audio/midi", ";;"} {
		if got, err := llm.AudioFormat(in); err == nil {
			t.Errorf("AudioFormat(%q) = %q, want error", in, got)
		}
	}
}

func textTurn(text string) llmtest.TurnScript {
	return llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: text},
		{Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 7}},
		{Kind: llm.EventEnd, FinishReason: "stop"},
	}}
}

func TestTranscribe_Request(t *testing.T) {
	t.Parallel()
	stub := llmtest.NewStubClient(textTurn("  Ich habe 3.800 € bezahlt.\n"))
	text, usage, err := llm.Transcribe(context.Background(), stub, llm.TranscribeRequest{
		Audio:  []byte("RIFF"),
		Format: "wav",
		Hint:   "The speaker most likely speaks German.",
	})
	if err != nil || text != "Ich habe 3.800 € bezahlt." || usage == nil || usage.PromptTokens != 7 {
		t.Fatalf("Transcribe = %q, %+v, %v", text, usage, err)
	}

	req := stub.Requests()[0]
	if req.Model != llm.DefaultTranscribeModel || req.Temperature == nil || *req.Temperature != 0 {
		t.Errorf("model %q temperature %v", req.Model, req.Temperature)
	}
	m := req.Messages[0]
	if len(req.Messages) != 1 || m.Role != llm.RoleUser {
		t.Fatalf("messages = %+v", req.Messages)
	}
	if len(m.Audio) != 1 || string(m.Audio[0].Data) != "RIFF" || m.Audio[0].Format != "wav" {
		t.Errorf("audio = %+v", m.Audio)
	}
	if !strings.Contains(m.Content, "[no speech]") || !strings.HasSuffix(m.Content, "\n\nThe speaker most likely speaks German.") {
		t.Errorf("prompt = %q", m.Content)
	}
}

func TestTranscribe_NoSpeechAndModel(t *testing.T) {
	t.Parallel()
	stub := llmtest.NewStubClient(textTurn("[no speech]\n"), textTurn(" [no speech]. "))
	for range 2 {
		text, _, err := llm.Transcribe(context.Background(), stub, llm.TranscribeRequest{Model: "m", Audio: []byte{1}, Format: "webm"})
		if err != nil || text != "" {
			t.Errorf("Transcribe = %q, %v; want empty", text, err)
		}
	}
	if got := stub.Requests()[0].Model; got != "m" {
		t.Errorf("model = %q", got)
	}
}

func TestTranscribe_Errors(t *testing.T) {
	t.Parallel()
	stub := llmtest.NewStubClient()
	if _, _, err := llm.Transcribe(context.Background(), stub, llm.TranscribeRequest{Format: "wav"}); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Errorf("no audio: %v", err)
	}
	if _, _, err := llm.Transcribe(context.Background(), stub, llm.TranscribeRequest{Audio: []byte{1}}); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Errorf("no format: %v", err)
	}
	if len(stub.Requests()) != 0 {
		t.Error("invalid request reached the client")
	}

	boom := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 3}},
		{Kind: llm.EventError, Err: llm.ErrServerError},
	}})
	if _, usage, err := llm.Transcribe(context.Background(), boom, llm.TranscribeRequest{Audio: []byte{1}, Format: "wav"}); !errors.Is(err, llm.ErrServerError) || usage == nil {
		t.Errorf("provider error: usage %+v, err %v", usage, err)
	}
}
