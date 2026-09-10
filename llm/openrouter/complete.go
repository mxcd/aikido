package openrouter

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mxcd/aikido/llm"
)

// Complete sends a non-streaming chat-completions request and returns the
// fully-assembled response in one shot.
//
// Use this for image generation. Image-capable models (gemini-flash-image-preview,
// gpt-image-1, etc.) emit the entire base64 PNG/JPEG payload in a single SSE
// chunk. A 1024×1024 PNG is typically 0.8–2 MB raw → 1.07–2.7 MiB base64,
// which exceeds the SSE scanner's per-line cap and trips ErrServerError on
// every retry. Complete reads the body as one JSON document and is immune to
// that failure mode.
//
// Retry behaviour mirrors Stream: 429/5xx at request-start are retried per
// retryPolicy(); auth and bad-request errors short-circuit. Content-filter
// aborts surface as ErrContentFiltered (either via a top-level error envelope
// on the JSON body or via finish_reason="content_filter"). Network-layer
// errors are mapped onto ErrServerError so CompleteWithRetry can apply the
// same retry policy as CollectWithRetry.
func (c *Client) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	body, err := c.buildBody(req, false)
	if err != nil {
		return llm.Response{}, fmt.Errorf("openrouter: build request: %w", err)
	}

	raw, err := c.postJSON(ctx, "/chat/completions", body)
	if err != nil {
		return llm.Response{}, err
	}
	return parseCompleteResponse(raw)
}

// parseCompleteResponse maps a non-streaming chat-completions JSON body onto
// llm.Response.
//
// The wire shape mirrors streamChunk except that the assistant payload lives
// on choices[i].message instead of choices[i].delta. We tolerate both shapes
// and harmlessly fold whichever is populated — some providers (and gateway
// shims) populate `delta` even on non-streaming responses.
func parseCompleteResponse(body []byte) (llm.Response, error) {
	var chunk streamChunk
	if err := json.Unmarshal(body, &chunk); err != nil {
		return llm.Response{}, fmt.Errorf("openrouter: decode response: %w", llm.ErrServerError)
	}

	// Top-level error envelope. Map onto ErrServerError or ErrContentFiltered.
	// The usage block rides along when the provider priced the failed call, and
	// a priced failure still cost money: return it with the error rather than
	// dropping it.
	if chunk.Error != nil {
		msg := chunk.Error.Message
		if msg == "" {
			msg = "provider error"
		}
		cause := llm.ErrServerError
		if isContentFilterErrorEnvelope(chunk.Error) {
			cause = llm.ErrContentFiltered
		}
		return llm.Response{Usage: toLLMUsage(chunk.Usage)}, fmt.Errorf("openrouter: %s: %w", msg, cause)
	}

	var resp llm.Response
	asm := newToolCallAssembler()
	for _, ch := range chunk.Choices {
		absorbDelta(&resp, asm, ch.Delta)
		if ch.Message != nil {
			absorbDelta(&resp, asm, *ch.Message)
		}
		if ch.FinishReason != nil {
			resp.FinishReason = *ch.FinishReason
		}
	}
	if calls := asm.flush(); len(calls) > 0 {
		resp.ToolCalls = append(resp.ToolCalls, calls...)
	}
	if u := toLLMUsage(chunk.Usage); u != nil {
		resp.Usage = u
	}

	// finish_reason="content_filter" without a top-level error envelope —
	// surface as ErrContentFiltered so the same caller branch fires for both
	// streaming and non-streaming. Keep the partial Response so callers can
	// log usage even on filtered turns.
	if resp.FinishReason == "content_filter" {
		return resp, fmt.Errorf("openrouter finish_reason=content_filter: %w", llm.ErrContentFiltered)
	}

	return resp, nil
}

// absorbDelta folds one streamDelta payload (delta or message) into the
// response builder. Mirrors emitPayload in sse.go but writes to a Response
// rather than emitting events on a channel.
func absorbDelta(resp *llm.Response, asm *toolCallAssembler, p streamDelta) {
	if text, imgs := decodeDeltaContent(p.Content); text != "" || len(imgs) > 0 {
		resp.Text += text
		resp.Images = append(resp.Images, imgs...)
	}
	for _, ip := range p.Images {
		if img, ok := decodeAPIImagePart(ip); ok {
			resp.Images = append(resp.Images, img)
		}
	}
	for _, frag := range p.ToolCalls {
		asm.feed(frag)
	}
}
