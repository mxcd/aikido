package openrouter

import (
	"bufio"
	"bytes"
	"context"
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
func processStream(ctx context.Context, body io.Reader, out chan<- llm.Event) {
	defer emit(ctx, out, llm.Event{Kind: llm.EventEnd})

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
			emit(ctx, out, llm.Event{
				Kind: llm.EventError,
				Err:  fmt.Errorf("openrouter mid-stream: %s: %w", msg, llm.ErrServerError),
			})
			sawError = true
			break
		}

		// Per-choice processing. Single-choice in practice, but be tolerant.
		for _, ch := range chunk.Choices {
			// Text content fragment.
			if ch.Delta.Content != "" {
				emit(ctx, out, llm.Event{Kind: llm.EventTextDelta, Text: ch.Delta.Content})
			}
			// Reasoning / thinking fragment.
			if ch.Delta.Reasoning != "" {
				emit(ctx, out, llm.Event{Kind: llm.EventThinking, Text: ch.Delta.Reasoning})
			}
			// Tool-call fragments.
			for _, frag := range ch.Delta.ToolCalls {
				asm.feed(frag)
			}
			// finish_reason — assemble and emit any pending tool calls.
			if ch.FinishReason != nil {
				switch *ch.FinishReason {
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
				default:
					// stop, tool_calls, length, content_filter — flush calls.
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
