package llmtest

import (
	"context"
	"errors"
	"testing"

	"github.com/mxcd/aikido/llm"
)

func TestStubClient_Replay(t *testing.T) {
	stub := NewStubClient(TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "hi"},
		{Kind: llm.EventEnd},
	}})
	events, err := stub.Stream(context.Background(), llm.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got []llm.EventKind
	for ev := range events {
		got = append(got, ev.Kind)
	}
	if len(got) != 2 || got[0] != llm.EventTextDelta || got[1] != llm.EventEnd {
		t.Errorf("got %v", got)
	}
}

func TestStubClient_Exhausted(t *testing.T) {
	stub := NewStubClient(TurnScript{Events: []llm.Event{{Kind: llm.EventEnd}}})
	if _, err := stub.Stream(context.Background(), llm.Request{}); err != nil {
		t.Fatalf("first Stream: %v", err)
	}
	_, err := stub.Stream(context.Background(), llm.Request{})
	if !errors.Is(err, ErrStubExhausted) {
		t.Errorf("err = %v; want ErrStubExhausted", err)
	}
}

func TestStubClient_RequestsCaptured(t *testing.T) {
	stub := NewStubClient(
		TurnScript{Events: []llm.Event{{Kind: llm.EventEnd}}},
		TurnScript{Events: []llm.Event{{Kind: llm.EventEnd}}},
	)
	_, _ = stub.Stream(context.Background(), llm.Request{Model: "m1"})
	_, _ = stub.Stream(context.Background(), llm.Request{Model: "m2"})
	reqs := stub.Requests()
	if len(reqs) != 2 || reqs[0].Model != "m1" || reqs[1].Model != "m2" {
		t.Errorf("requests = %+v", reqs)
	}
}

func TestStubClient_GenerateImage_ScriptsAndRecords(t *testing.T) {
	stub := NewStubClient()
	want := llm.ImageResponse{
		Images: []llm.ImagePart{{ContentType: "image/png", Data: []byte("png-bytes")}},
		Usage:  &llm.Usage{CostUSD: 0.0053},
	}
	boom := errors.New("provider blew up")
	stub.ScriptImages(
		ImageScript{Response: want},
		ImageScript{Err: boom},
	)

	got, err := stub.GenerateImage(context.Background(), llm.ImageRequest{Model: "openai/gpt-image-2.5-flare", Prompt: "a red cube"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(got.Images) != 1 || string(got.Images[0].Data) != "png-bytes" {
		t.Errorf("images = %+v", got.Images)
	}
	if got.Usage == nil || got.Usage.CostUSD != 0.0053 {
		t.Errorf("usage = %+v", got.Usage)
	}

	if _, err := stub.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"}); !errors.Is(err, boom) {
		t.Errorf("second call err = %v, want the scripted error", err)
	}
	if _, err := stub.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"}); !errors.Is(err, ErrStubExhausted) {
		t.Errorf("third call err = %v, want ErrStubExhausted", err)
	}

	// The exhausted call is not recorded, matching Stream.
	reqs := stub.ImageRequests()
	if len(reqs) != 2 {
		t.Fatalf("recorded %d requests, want 2", len(reqs))
	}
	if reqs[0].Model != "openai/gpt-image-2.5-flare" || reqs[0].Prompt != "a red cube" {
		t.Errorf("first request = %+v", reqs[0])
	}
}
