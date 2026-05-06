package agent

import (
	"encoding/json"
	"strings"

	"github.com/mxcd/aikido/llm"
)

// Drain consumes events from the channel until it closes and returns the
// assembled assistant message followed by tool-result messages, in the
// order they were produced this turn. Returns the first error encountered
// on EventError.
//
// Drain is the recommended way for RunWithMessages callers to obtain the
// turn's output messages for appending to their own history store. Callers
// who also need streaming (e.g., to a UI) fan out the channel themselves
// before passing one branch to Drain.
func Drain(events <-chan Event) ([]llm.Message, error) {
	var (
		msgs           []llm.Message
		accText        strings.Builder
		accCalls       []llm.ToolCall
		accImages      []llm.ImagePart
		accToolResults []llm.Message
		firstErr       error
	)

	flush := func() {
		if accText.Len() > 0 || len(accCalls) > 0 || len(accImages) > 0 {
			msgs = append(msgs, llm.Message{
				Role:      llm.RoleAssistant,
				Content:   accText.String(),
				Images:    append([]llm.ImagePart(nil), accImages...),
				ToolCalls: append([]llm.ToolCall(nil), accCalls...),
			})
		}
		msgs = append(msgs, accToolResults...)
		accText.Reset()
		accCalls = nil
		accImages = nil
		accToolResults = nil
	}

	for ev := range events {
		switch ev.Kind {
		case EventText:
			if len(accToolResults) > 0 {
				flush()
			}
			accText.WriteString(ev.Text)
		case EventThinking:
			// not part of the persisted message log
		case EventToolCall:
			if ev.ToolCall != nil {
				accCalls = append(accCalls, *ev.ToolCall)
			}
		case EventImage:
			if ev.Image != nil {
				if len(accToolResults) > 0 {
					flush()
				}
				accImages = append(accImages, *ev.Image)
			}
		case EventToolResult:
			if ev.ToolResult != nil {
				accToolResults = append(accToolResults, llm.Message{
					Role:       llm.RoleTool,
					ToolCallID: ev.ToolResult.CallID,
					Content:    drainToolResultJSON(ev.ToolResult),
				})
			}
		case EventUsage:
			// not part of the persisted message log
		case EventError:
			if firstErr == nil {
				firstErr = ev.Err
			}
		case EventEnd:
			flush()
		}
	}
	return msgs, firstErr
}

func drainToolResultJSON(tr *ToolResult) string {
	if !tr.OK {
		b, _ := json.Marshal(map[string]any{"error": tr.Error})
		return string(b)
	}
	if tr.Content == nil {
		return "null"
	}
	b, err := json.Marshal(tr.Content)
	if err != nil {
		fallback, _ := json.Marshal(map[string]any{"error": "content marshal failed: " + err.Error()})
		return string(fallback)
	}
	return string(b)
}
