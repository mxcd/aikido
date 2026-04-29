package llm

import (
	"context"
	"strings"
)

// Float32 returns a pointer to v.
//
// Convenience for SessionOptions.Temperature so callers can write inline values.
// Float32(0) returns a non-nil pointer to zero — the deterministic-zero case.
func Float32(v float32) *float32 {
	return &v
}

// Collect drains a stream into a final result. Useful for non-streaming callers.
//
// Returns text accumulated from EventTextDelta, all complete tool calls, final
// Usage if the provider emitted one, and the first error encountered. Thinking
// text is not included in the returned text.
//
// Collect respects ctx cancellation: if ctx is cancelled before the stream
// closes, Collect returns ctx.Err() without waiting for the producer.
func Collect(ctx context.Context, c Client, req Request) (text string, calls []ToolCall, usage *Usage, err error) {
	events, err := c.Stream(ctx, req)
	if err != nil {
		return "", nil, nil, err
	}
	var sb strings.Builder
	for {
		select {
		case <-ctx.Done():
			return sb.String(), calls, usage, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return sb.String(), calls, usage, nil
			}
			switch ev.Kind {
			case EventTextDelta:
				sb.WriteString(ev.Text)
			case EventToolCall:
				if ev.Tool != nil {
					calls = append(calls, *ev.Tool)
				}
			case EventUsage:
				usage = ev.Usage
			case EventError:
				return sb.String(), calls, usage, ev.Err
			case EventThinking, EventEnd:
				// thinking is not added to text; end is informational
			}
		}
	}
}
