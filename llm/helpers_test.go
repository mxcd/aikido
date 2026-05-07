package llm

import (
	"context"
	"errors"
	"testing"
)

// chanClient is a tiny in-package stub Client for testing helpers.
type chanClient struct {
	events []Event
	err    error
}

func (c *chanClient) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	if c.err != nil {
		return nil, c.err
	}
	ch := make(chan Event, len(c.events))
	for _, ev := range c.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func (c *chanClient) Complete(ctx context.Context, req Request) (Response, error) {
	if c.err != nil {
		return Response{}, c.err
	}
	var resp Response
	for _, ev := range c.events {
		switch ev.Kind {
		case EventTextDelta:
			resp.Text += ev.Text
		case EventToolCall:
			if ev.Tool != nil {
				resp.ToolCalls = append(resp.ToolCalls, *ev.Tool)
			}
		case EventImage:
			if ev.Image != nil {
				resp.Images = append(resp.Images, *ev.Image)
			}
		case EventUsage:
			resp.Usage = ev.Usage
		case EventError:
			return resp, ev.Err
		case EventEnd:
			resp.FinishReason = ev.FinishReason
		}
	}
	return resp, nil
}

func TestCollect_HappyPath(t *testing.T) {
	cc := &chanClient{events: []Event{
		{Kind: EventTextDelta, Text: "Hello, "},
		{Kind: EventTextDelta, Text: "world."},
		{Kind: EventToolCall, Tool: &ToolCall{ID: "c1", Name: "fn", Arguments: `{"a":1}`}},
		{Kind: EventUsage, Usage: &Usage{PromptTokens: 4, CompletionTokens: 7}},
		{Kind: EventEnd},
	}}
	text, calls, images, usage, err := Collect(context.Background(), cc, Request{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if text != "Hello, world." {
		t.Errorf("text = %q, want %q", text, "Hello, world.")
	}
	if len(calls) != 1 || calls[0].ID != "c1" || calls[0].Arguments != `{"a":1}` {
		t.Errorf("calls = %+v", calls)
	}
	if len(images) != 0 {
		t.Errorf("images = %+v, want none", images)
	}
	if usage == nil || usage.PromptTokens != 4 || usage.CompletionTokens != 7 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestCollect_Images(t *testing.T) {
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	cc := &chanClient{events: []Event{
		{Kind: EventTextDelta, Text: "here it is"},
		{Kind: EventImage, Image: &ImagePart{ContentType: "image/png", Data: pngBytes}},
		{Kind: EventImage, Image: &ImagePart{URL: "https://example.com/x.png", ContentType: "image/png"}},
		{Kind: EventEnd},
	}}
	text, _, images, _, err := Collect(context.Background(), cc, Request{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if text != "here it is" {
		t.Errorf("text = %q", text)
	}
	if len(images) != 2 {
		t.Fatalf("images len = %d, want 2", len(images))
	}
	if string(images[0].Data) != string(pngBytes) || images[0].ContentType != "image/png" {
		t.Errorf("images[0] = %+v", images[0])
	}
	if images[1].URL != "https://example.com/x.png" || len(images[1].Data) != 0 {
		t.Errorf("images[1] = %+v", images[1])
	}
}

func TestCollect_ThinkingExcludedFromText(t *testing.T) {
	cc := &chanClient{events: []Event{
		{Kind: EventThinking, Text: "let me think about this"},
		{Kind: EventTextDelta, Text: "answer"},
		{Kind: EventThinking, Text: "more thinking"},
		{Kind: EventEnd},
	}}
	text, _, _, _, err := Collect(context.Background(), cc, Request{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if text != "answer" {
		t.Errorf("text = %q, want %q (thinking must not be included)", text, "answer")
	}
}

func TestCollect_PropagatesEventError(t *testing.T) {
	sentinel := errors.New("provider blew up")
	cc := &chanClient{events: []Event{
		{Kind: EventTextDelta, Text: "partial"},
		{Kind: EventError, Err: sentinel},
		{Kind: EventEnd},
	}}
	text, _, _, _, err := Collect(context.Background(), cc, Request{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want %v", err, sentinel)
	}
	if text != "partial" {
		t.Errorf("text = %q, want %q", text, "partial")
	}
}

func TestCollect_PropagatesStreamError(t *testing.T) {
	sentinel := errors.New("dial failure")
	cc := &chanClient{err: sentinel}
	_, _, _, _, err := Collect(context.Background(), cc, Request{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want %v", err, sentinel)
	}
}

func TestCollect_ContextCancellation(t *testing.T) {
	ch := make(chan Event) // never closed; producer never sends
	cc := &fakeClient{ch: ch}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, _, err := Collect(ctx, cc, Request{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

type fakeClient struct{ ch chan Event }

func (f *fakeClient) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	return f.ch, nil
}

func (f *fakeClient) Complete(ctx context.Context, req Request) (Response, error) {
	<-ctx.Done()
	return Response{}, ctx.Err()
}

func TestFloat32(t *testing.T) {
	p := Float32(0)
	if p == nil {
		t.Fatal("Float32(0) returned nil; the deterministic-zero foot-gun is open")
	}
	if *p != 0 {
		t.Errorf("*Float32(0) = %v, want 0", *p)
	}
	q := Float32(0.7)
	if *q != 0.7 {
		t.Errorf("*Float32(0.7) = %v, want 0.7", *q)
	}
	if p == q {
		t.Error("Float32 returned aliased pointers")
	}
}
