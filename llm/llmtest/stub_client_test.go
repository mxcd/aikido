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
