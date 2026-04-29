// Package history provides a reusable conformance suite for the agent.History
// interface.
package history

import (
	"context"
	"testing"

	"github.com/mxcd/aikido/agent"
	"github.com/mxcd/aikido/llm"
)

// RunConformance executes the standard suite against a History factory.
//
// Sub-tests are serial. Implementations test their backend by calling this
// from a TestX function in their package.
func RunConformance(t *testing.T, factory func() agent.History) {
	t.Helper()

	t.Run("ReadEmpty", func(t *testing.T) {
		h := factory()
		msgs, err := h.Read(context.Background(), "s")
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if len(msgs) != 0 {
			t.Errorf("Read empty = %v; want []", msgs)
		}
	})

	t.Run("AppendThenRead", func(t *testing.T) {
		h := factory()
		ctx := context.Background()
		err := h.Append(ctx, "s",
			llm.Message{Role: llm.RoleUser, Content: "hi"},
			llm.Message{Role: llm.RoleAssistant, Content: "hello"},
		)
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		msgs, err := h.Read(ctx, "s")
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if len(msgs) != 2 {
			t.Fatalf("len(msgs) = %d; want 2", len(msgs))
		}
		if msgs[0].Content != "hi" || msgs[1].Content != "hello" {
			t.Errorf("msgs = %+v", msgs)
		}
	})

	t.Run("AppendsAccumulate", func(t *testing.T) {
		h := factory()
		ctx := context.Background()
		_ = h.Append(ctx, "s", llm.Message{Role: llm.RoleUser, Content: "a"})
		_ = h.Append(ctx, "s", llm.Message{Role: llm.RoleUser, Content: "b"})
		msgs, _ := h.Read(ctx, "s")
		if len(msgs) != 2 || msgs[0].Content != "a" || msgs[1].Content != "b" {
			t.Errorf("msgs = %+v", msgs)
		}
	})

	t.Run("PerSessionIsolation", func(t *testing.T) {
		h := factory()
		ctx := context.Background()
		_ = h.Append(ctx, "s1", llm.Message{Role: llm.RoleUser, Content: "x"})
		_ = h.Append(ctx, "s2", llm.Message{Role: llm.RoleUser, Content: "y"})
		m1, _ := h.Read(ctx, "s1")
		m2, _ := h.Read(ctx, "s2")
		if len(m1) != 1 || m1[0].Content != "x" {
			t.Errorf("s1 msgs = %+v", m1)
		}
		if len(m2) != 1 || m2[0].Content != "y" {
			t.Errorf("s2 msgs = %+v", m2)
		}
	})

	t.Run("ReadIsolatedFromMutation", func(t *testing.T) {
		h := factory()
		ctx := context.Background()
		_ = h.Append(ctx, "s", llm.Message{Role: llm.RoleUser, Content: "a"})
		msgs, _ := h.Read(ctx, "s")
		if len(msgs) > 0 {
			msgs[0].Content = "MUTATED"
		}
		again, _ := h.Read(ctx, "s")
		if again[0].Content != "a" {
			t.Errorf("Read returned aliased slice; mutation leaked: %v", again[0].Content)
		}
	})

	t.Run("EmptyAppendIsNoop", func(t *testing.T) {
		h := factory()
		if err := h.Append(context.Background(), "s"); err != nil {
			t.Errorf("Append() with no messages = %v; want nil", err)
		}
	})
}
