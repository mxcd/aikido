package openrouter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/mxcd/aikido/llm"
)

// sseDoneToken is the OpenRouter terminator. Once seen, no more useful events
// arrive.
const sseDoneToken = "[DONE]"

// processStream reads the SSE body and emits llm.Events onto out. It is
// designed to be called from a producer goroutine; it always emits exactly
// one EventEnd as the final event before returning. Channel close is the
// caller's responsibility (the main client wraps this in a goroutine that
// closes the channel after processStream returns).
//
// processStream respects ctx by checking on every send. On ctx.Done it
// returns silently — the caller-side goroutine will still close the channel.
//
// Mid-stream errors (provider drops the connection mid-SSE, or the chunk
// includes a top-level `error` envelope) are emitted as EventError followed
// by EventEnd. They are NOT retried (per ADR-006 / ADR-022).
//
// Content-filter signals come in two forms (per ADR-028):
//  1. finish_reason="content_filter" on a regular chunk — emitted as
//     EventError{Err: ErrContentFiltered} so callers can branch on it.
//  2. error envelope with code/message indicating content filtering — same.
//
// The closing EventEnd carries FinishReason populated from the provider's
// last finish_reason (or "content_filter"/"error" when surfaced as an error).
func processStream(ctx context.Context, body io.Reader, out chan<- llm.Event) {
	var endFinishReason string
	defer func() {
		emit(ctx, out, llm.Event{Kind: llm.EventEnd, FinishReason: endFinishReason})
	}()

	asm := newToolCallAssembler()
	scanner := bufio.NewScanner(body)
	// Allow lines up to 1 MiB — provider tool-call payloads can be large.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var sawError bool

	for scanner.Scan() {
		// Cooperative cancellation check: bail before doing more work.
		if ctx.Err() != nil {
			return
		}

		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			continue
		}
		// Comment lines (single colon prefix). OpenRouter sends
		// `: OPENROUTER PROCESSING` as a TCP-keepalive substitute.
		if strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			// Unknown line shape — be permissive, ignore.
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == sseDoneToken {
			break
		}
		if payload == "" {
			continue
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// Permissive: skip malformed JSON rather than abort the whole
			// stream. The spec says decoders ignore unknown fields; treat
			// invalid lines the same way.
			continue
		}

		// Top-level error envelope = mid-stream error. Emit and stop.
		if chunk.Error != nil {
			// Flush any buffered tool calls (defensive — usually empty).
			emitAssembledCalls(ctx, out, asm.flush())
			msg := chunk.Error.Message
			if msg == "" {
				msg = "provider error"
			}
			cause := llm.ErrServerError
			if isContentFilterErrorEnvelope(chunk.Error) {
				cause = llm.ErrContentFiltered
				endFinishReason = "content_filter"
			} else {
				endFinishReason = "error"
			}
			emit(ctx, out, llm.Event{
				Kind: llm.EventError,
				Err:  fmt.Errorf("openrouter mid-stream: %s: %w", msg, cause),
			})
			sawError = true
			break
		}

		// Per-choice processing. Single-choice in practice, but be tolerant.
		for _, ch := range chunk.Choices {
			emitPayload(ctx, out, asm, ch.Delta)
			// Some image-capable models deliver the generated image as a
			// non-streaming `choices[0].message` payload instead of a `delta`.
			// Process both shapes; harmless when only one is populated.
			if ch.Message != nil {
				emitPayload(ctx, out, asm, *ch.Message)
			}
			// finish_reason — assemble and emit any pending tool calls.
			if ch.FinishReason != nil {
				reason := *ch.FinishReason
				endFinishReason = reason
				switch reason {
				case "error":
					// finish_reason "error" should have come with a top-level
					// error envelope already handled above. If not, surface
					// a generic server error.
					if !sawError {
						emitAssembledCalls(ctx, out, asm.flush())
						emit(ctx, out, llm.Event{
							Kind: llm.EventError,
							Err:  fmt.Errorf("openrouter mid-stream: finish_reason=error: %w", llm.ErrServerError),
						})
						sawError = true
					}
				case "content_filter":
					// Provider's safety policy aborted the generation. Surface
					// distinctly so callers can skip retry and show a
					// user-actionable error.
					emitAssembledCalls(ctx, out, asm.flush())
					emit(ctx, out, llm.Event{
						Kind: llm.EventError,
						Err:  fmt.Errorf("openrouter finish_reason=content_filter: %w", llm.ErrContentFiltered),
					})
					sawError = true
				default:
					// stop, tool_calls, length — flush calls.
					emitAssembledCalls(ctx, out, asm.flush())
				}
			}
		}

		// Usage may arrive on any chunk (typically the final content chunk
		// or a dedicated trailing chunk with empty `choices`). Emit once seen.
		if chunk.Usage != nil {
			if u := toLLMUsage(chunk.Usage); u != nil {
				emit(ctx, out, llm.Event{Kind: llm.EventUsage, Usage: u})
			}
		}

		if sawError {
			break
		}
	}

	// Loop fell out of scanner (EOF or [DONE] or error). Handle scanner.Err()
	// as a mid-stream drop if we never saw a clean termination.
	if err := scanner.Err(); err != nil && !sawError && !errors.Is(err, io.EOF) {
		// Defensive flush.
		emitAssembledCalls(ctx, out, asm.flush())
		emit(ctx, out, llm.Event{
			Kind: llm.EventError,
			Err:  fmt.Errorf("openrouter mid-stream read: %w", llm.ErrServerError),
		})
	}
}

// emit sends ev unless ctx is done. It does NOT close out.
func emit(ctx context.Context, out chan<- llm.Event, ev llm.Event) {
	select {
	case <-ctx.Done():
	case out <- ev:
	}
}

// emitAssembledCalls emits one EventToolCall per assembled tool call, in
// ascending index order. Empty input is a no-op.
func emitAssembledCalls(ctx context.Context, out chan<- llm.Event, calls []llm.ToolCall) {
	for i := range calls {
		c := calls[i]
		emit(ctx, out, llm.Event{Kind: llm.EventToolCall, Tool: &c})
	}
}

// emitPayload emits text / image / thinking events from one delta-or-message
// payload and feeds tool-call fragments into the assembler. Used for both
// streaming `delta` and non-streaming `message` shapes.
func emitPayload(ctx context.Context, out chan<- llm.Event, asm *toolCallAssembler, p streamDelta) {
	// Text content fragment. `content` is RawMessage so we can also detect
	// inline image-data URIs and typed content arrays that some image-capable
	// models emit in place of (or alongside) `images`.
	if text, imgs := decodeDeltaContent(p.Content); text != "" || len(imgs) > 0 {
		if text != "" {
			emit(ctx, out, llm.Event{Kind: llm.EventTextDelta, Text: text})
		}
		for i := range imgs {
			img := imgs[i]
			emit(ctx, out, llm.Event{Kind: llm.EventImage, Image: &img})
		}
	}
	if p.Reasoning != "" {
		emit(ctx, out, llm.Event{Kind: llm.EventThinking, Text: p.Reasoning})
	}
	for _, ip := range p.Images {
		if img, ok := decodeAPIImagePart(ip); ok {
			emit(ctx, out, llm.Event{Kind: llm.EventImage, Image: &img})
		}
	}
	for _, frag := range p.ToolCalls {
		asm.feed(frag)
	}
}

// toolCallAssembler buffers incoming fragments by index and produces complete
// llm.ToolCalls when finalized.
type toolCallAssembler struct {
	parts map[int]*partialToolCall
}

type partialToolCall struct {
	id   string
	name string
	args bytes.Buffer
}

func newToolCallAssembler() *toolCallAssembler {
	return &toolCallAssembler{parts: make(map[int]*partialToolCall)}
}

// feed merges a single fragment into the buffer.
func (a *toolCallAssembler) feed(frag toolCallFragment) {
	p, ok := a.parts[frag.Index]
	if !ok {
		p = &partialToolCall{}
		a.parts[frag.Index] = p
	}
	if p.id == "" && frag.ID != "" {
		p.id = frag.ID
	}
	if p.name == "" && frag.Function.Name != "" {
		p.name = frag.Function.Name
	}
	if frag.Function.Arguments != "" {
		p.args.WriteString(frag.Function.Arguments)
	}
}

// decodeDeltaContent extracts plain text and any inline images from a
// streaming `delta.content` (or non-streaming `message.content`) field.
//
// OpenRouter's image-capable models surface generated images in three
// observed shapes:
//
//  1. A plain JSON string — usually descriptive text, but occasionally a raw
//     `data:image/...;base64,...` URI for tiny single-image responses.
//  2. A typed-parts array — `[{type:"text",text:"..."}, {type:"image_url",
//     image_url:{url:"data:..."}}]`.
//  3. The dedicated `images` field on the message/delta envelope (handled
//     separately by the caller).
//
// Empty input or a JSON null returns ("", nil). Unknown shapes return ("", nil)
// rather than erroring — be permissive on the wire.
func decodeDeltaContent(raw json.RawMessage) (string, []llm.ImagePart) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	// Plain string — most common.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if strings.HasPrefix(s, "data:image/") {
			if img, ok := decodeDataURI(s); ok {
				return "", []llm.ImagePart{img}
			}
		}
		return s, nil
	}
	// Typed-parts array.
	var parts []apiContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var (
			sb     strings.Builder
			images []llm.ImagePart
		)
		for _, p := range parts {
			switch p.Type {
			case "text":
				sb.WriteString(p.Text)
			case "image_url":
				if p.ImageURL == nil {
					continue
				}
				if img, ok := decodeImageURL(p.ImageURL.URL); ok {
					images = append(images, img)
				}
			}
		}
		return sb.String(), images
	}
	return "", nil
}

// decodeAPIImagePart maps one apiImagePart onto an llm.ImagePart, decoding
// data: URIs into bytes and leaving HTTP URLs as-is. Returns false on missing
// or unrecognized shapes.
func decodeAPIImagePart(p apiImagePart) (llm.ImagePart, bool) {
	if p.ImageURL == nil {
		return llm.ImagePart{}, false
	}
	return decodeImageURL(p.ImageURL.URL)
}

// decodeImageURL converts a URL string into an llm.ImagePart. data: URIs are
// decoded into bytes; other URLs are passed through verbatim. Returns false
// on empty or malformed input.
func decodeImageURL(url string) (llm.ImagePart, bool) {
	if url == "" {
		return llm.ImagePart{}, false
	}
	if strings.HasPrefix(url, "data:") {
		return decodeDataURI(url)
	}
	return llm.ImagePart{URL: url}, true
}

// decodeDataURI parses a `data:<mime>;base64,<payload>` URI into an ImagePart.
// Non-base64 data URIs and malformed inputs return false.
func decodeDataURI(uri string) (llm.ImagePart, bool) {
	if !strings.HasPrefix(uri, "data:") {
		return llm.ImagePart{}, false
	}
	rest := uri[len("data:"):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return llm.ImagePart{}, false
	}
	meta := rest[:comma]
	payload := rest[comma+1:]
	if !strings.HasSuffix(meta, ";base64") {
		// non-base64 data URIs are valid HTTP-spec but unused by the providers
		// we target; surface only the URL so callers can still see them.
		return llm.ImagePart{URL: uri, ContentType: meta}, true
	}
	contentType := strings.TrimSuffix(meta, ";base64")
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return llm.ImagePart{}, false
	}
	return llm.ImagePart{ContentType: contentType, Data: data}, true
}

// flush returns the assembled tool calls in ascending index order and resets
// the buffer.
func (a *toolCallAssembler) flush() []llm.ToolCall {
	if len(a.parts) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(a.parts))
	for idx := range a.parts {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	out := make([]llm.ToolCall, 0, len(indexes))
	for _, idx := range indexes {
		p := a.parts[idx]
		args := p.args.String()
		if args == "" {
			args = "{}"
		}
		id := p.id
		if id == "" {
			// Defensive fallback per OPENROUTER-DETAILS edge cases.
			id = fmt.Sprintf("call_%d", idx)
		}
		out = append(out, llm.ToolCall{
			ID:        id,
			Name:      p.name,
			Arguments: args,
		})
	}
	a.parts = make(map[int]*partialToolCall)
	return out
}

// isContentFilterErrorEnvelope checks whether a structured error envelope
// indicates content filtering. OpenRouter forwards provider-shaped error codes
// in `error.code` (sometimes a string, sometimes an int HTTP status), and the
// human message in `error.message`. We match the most common signals:
//
//   - code == "content_filter" or "content_policy_violation" or "safety" (string forms)
//   - message contains "content filter", "safety", or "policy" (case-insensitive)
//
// False positives lead to the caller showing "try a different prompt" copy
// instead of "try again later" — which is the correct user action either way
// for any policy-related rejection. Conservative match preferred.
func isContentFilterErrorEnvelope(e *apiError) bool {
	if e == nil {
		return false
	}
	if codeStr := decodeErrorCodeAsString(e.Code); codeStr != "" {
		switch strings.ToLower(codeStr) {
		case "content_filter", "content_policy_violation", "safety":
			return true
		}
	}
	msg := strings.ToLower(e.Message)
	if strings.Contains(msg, "content filter") ||
		strings.Contains(msg, "content policy") ||
		strings.Contains(msg, "safety policy") ||
		strings.Contains(msg, "safety filter") {
		return true
	}
	return false
}

// decodeErrorCodeAsString returns the JSON-encoded code as a string when it's
// a JSON string. Returns "" for numeric codes (HTTP status mirrors) or empty input.
func decodeErrorCodeAsString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}
