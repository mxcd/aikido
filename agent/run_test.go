package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mxcd/aikido/agent/history/memory"
	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/llmtest"
	"github.com/mxcd/aikido/tools"
)

func newTestSession(t *testing.T, client llm.Client, reg *tools.Registry, hist History, opts ...func(*SessionOptions)) *Session {
	t.Helper()
	if hist == nil {
		hist = memory.NewHistory()
	}
	o := &SessionOptions{
		ID:           "s-test",
		Client:       client,
		Tools:        reg,
		History:      hist,
		Locker:       NewLocalLocker(),
		Model:        "test/model",
		SystemPrompt: "you are a test",
		MaxTurns:     5,
	}
	for _, opt := range opts {
		opt(o)
	}
	s, err := NewSession(o)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return s
}

func collectEvents(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var out []Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestRun_HappyPath_TextOnly(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "Hello "},
		{Kind: llm.EventTextDelta, Text: "world"},
		{Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 5, CompletionTokens: 3}},
		{Kind: llm.EventEnd},
	}})
	hist := memory.NewHistory()
	s := newTestSession(t, stub, nil, hist)

	ch, err := s.Run(context.Background(), "say hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	events := collectEvents(t, ch)

	var text strings.Builder
	var sawUsage, sawEnd bool
	var endReason string
	for _, ev := range events {
		switch ev.Kind {
		case EventText:
			text.WriteString(ev.Text)
		case EventUsage:
			sawUsage = true
		case EventEnd:
			sawEnd = true
			endReason = ev.EndReason
		}
	}
	if text.String() != "Hello world" {
		t.Errorf("text = %q; want %q", text.String(), "Hello world")
	}
	if !sawUsage {
		t.Error("missing EventUsage")
	}
	if !sawEnd || endReason != EndReasonStop {
		t.Errorf("end = %v reason=%q", sawEnd, endReason)
	}

	// History flushed once: user + assistant.
	stored, _ := hist.Read(context.Background(), "s-test")
	if len(stored) != 2 {
		t.Fatalf("len(stored) = %d; want 2 (user+assistant)", len(stored))
	}
	if stored[0].Role != llm.RoleUser || stored[0].Content != "say hi" {
		t.Errorf("stored[0] = %+v", stored[0])
	}
	if stored[1].Role != llm.RoleAssistant || stored[1].Content != "Hello world" {
		t.Errorf("stored[1] = %+v", stored[1])
	}
}

func TestRun_ToolCallThenEnd(t *testing.T) {
	reg := tools.NewRegistry()
	if err := reg.Register(llm.ToolDef{Name: "echo"}, func(ctx context.Context, args json.RawMessage, env tools.Env) (tools.Result, error) {
		var v map[string]any
		_ = json.Unmarshal(args, &v)
		return tools.Result{Content: map[string]any{"echoed": v["msg"]}}, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	stub := llmtest.NewStubClient(
		// turn 1: model emits one tool call
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "thinking..."},
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "c1", Name: "echo", Arguments: `{"msg":"hi"}`}},
			{Kind: llm.EventEnd},
		}},
		// turn 2: model wraps up
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "done"},
			{Kind: llm.EventEnd},
		}},
	)
	hist := memory.NewHistory()
	s := newTestSession(t, stub, reg, hist)

	ch, _ := s.Run(context.Background(), "do it")
	events := collectEvents(t, ch)

	var sawToolCall, sawToolResult bool
	var endReason string
	for _, ev := range events {
		switch ev.Kind {
		case EventToolCall:
			sawToolCall = true
			if ev.ToolCall == nil || ev.ToolCall.Name != "echo" {
				t.Errorf("tool call wrong: %+v", ev.ToolCall)
			}
		case EventToolResult:
			sawToolResult = true
			if !ev.ToolResult.OK {
				t.Errorf("tool result not OK: %+v", ev.ToolResult)
			}
		case EventEnd:
			endReason = ev.EndReason
		}
	}
	if !sawToolCall || !sawToolResult {
		t.Errorf("tool events: call=%v result=%v", sawToolCall, sawToolResult)
	}
	if endReason != EndReasonStop {
		t.Errorf("endReason = %q; want stop", endReason)
	}

	// Verify exactly one History.Append called (variadic): user + asst1 + tool + asst2 = 4 messages.
	stored, _ := hist.Read(context.Background(), "s-test")
	if len(stored) != 4 {
		t.Errorf("stored len = %d; want 4 (user + asst1 + tool + asst2). msgs=%+v", len(stored), stored)
	}
}

func TestRun_ToolErrorContinues(t *testing.T) {
	reg := tools.NewRegistry()
	sentinel := errors.New("tool exploded")
	_ = reg.Register(llm.ToolDef{Name: "boom"}, func(ctx context.Context, args json.RawMessage, env tools.Env) (tools.Result, error) {
		return tools.Result{}, sentinel
	})
	stub := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "c1", Name: "boom"}},
			{Kind: llm.EventEnd},
		}},
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "i recovered"},
			{Kind: llm.EventEnd},
		}},
	)
	s := newTestSession(t, stub, reg, nil)
	ch, _ := s.Run(context.Background(), "go")
	events := collectEvents(t, ch)

	var sawToolResultFail bool
	var endReason string
	for _, ev := range events {
		if ev.Kind == EventToolResult && ev.ToolResult != nil && !ev.ToolResult.OK {
			sawToolResultFail = true
			if !strings.Contains(ev.ToolResult.Error, "exploded") {
				t.Errorf("tool error = %q", ev.ToolResult.Error)
			}
		}
		if ev.Kind == EventEnd {
			endReason = ev.EndReason
		}
	}
	if !sawToolResultFail {
		t.Error("expected EventToolResult with OK=false")
	}
	if endReason != EndReasonStop {
		t.Errorf("end = %q; want stop (loop continues despite tool error)", endReason)
	}
}

func TestRun_MidStreamErrorSkipsHistoryAppend(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "partial"},
		{Kind: llm.EventError, Err: errors.New("provider blew up")},
		{Kind: llm.EventEnd},
	}})
	hist := memory.NewHistory()
	s := newTestSession(t, stub, nil, hist)

	ch, _ := s.Run(context.Background(), "go")
	events := collectEvents(t, ch)

	var sawErr bool
	var endReason string
	for _, ev := range events {
		if ev.Kind == EventError {
			sawErr = true
		}
		if ev.Kind == EventEnd {
			endReason = ev.EndReason
		}
	}
	if !sawErr {
		t.Error("expected EventError")
	}
	if endReason != EndReasonError {
		t.Errorf("end = %q; want error", endReason)
	}
	stored, _ := hist.Read(context.Background(), "s-test")
	if len(stored) != 0 {
		t.Errorf("History.Append should NOT have been called on mid-stream error; stored=%+v", stored)
	}
}

func TestRun_MaxTurnsHitsCap(t *testing.T) {
	// Always emit a tool call so the loop never reaches stop.
	reg := tools.NewRegistry()
	_ = reg.Register(llm.ToolDef{Name: "loop"}, func(ctx context.Context, args json.RawMessage, env tools.Env) (tools.Result, error) {
		return tools.Result{Content: "ok"}, nil
	})
	turns := make([]llmtest.TurnScript, 10)
	for i := range turns {
		turns[i] = llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "c", Name: "loop"}},
			{Kind: llm.EventEnd},
		}}
	}
	stub := llmtest.NewStubClient(turns...)
	hist := memory.NewHistory()
	s := newTestSession(t, stub, reg, hist, func(o *SessionOptions) { o.MaxTurns = 3 })

	ch, _ := s.Run(context.Background(), "go")
	events := collectEvents(t, ch)

	var endReason string
	for _, ev := range events {
		if ev.Kind == EventEnd {
			endReason = ev.EndReason
		}
	}
	if endReason != EndReasonMaxTurns {
		t.Errorf("end = %q; want max_turns", endReason)
	}
	// History flushed: user + 3 turns of (asst + tool) = 1 + 6 = 7 messages.
	stored, _ := hist.Read(context.Background(), "s-test")
	if len(stored) != 7 {
		t.Errorf("stored len = %d; want 7", len(stored))
	}
}

func TestRun_RunTimeout(t *testing.T) {
	s := newTestSession(t, blockingClient{}, nil, nil, func(o *SessionOptions) {
		o.RunTimeout = 80 * time.Millisecond
		o.LLMCallTimeout = 10 * time.Second // make sure outer timeout fires first
	})
	ch, _ := s.Run(context.Background(), "go")
	events := collectEvents(t, ch)

	var endReason string
	for _, ev := range events {
		if ev.Kind == EventEnd {
			endReason = ev.EndReason
		}
	}
	if endReason != EndReasonTimeout {
		t.Errorf("end = %q; want timeout", endReason)
	}
}

func TestRun_LLMCallTimeout(t *testing.T) {
	s := newTestSession(t, blockingClient{}, nil, nil, func(o *SessionOptions) {
		o.RunTimeout = 5 * time.Second
		o.LLMCallTimeout = 80 * time.Millisecond
	})
	ch, _ := s.Run(context.Background(), "go")
	events := collectEvents(t, ch)

	var endReason string
	for _, ev := range events {
		if ev.Kind == EventEnd {
			endReason = ev.EndReason
		}
	}
	if endReason != EndReasonTimeout {
		t.Errorf("end = %q; want timeout", endReason)
	}
}

func TestRun_Cancellation(t *testing.T) {
	s := newTestSession(t, blockingClient{}, nil, nil, func(o *SessionOptions) {
		o.RunTimeout = 5 * time.Second
	})
	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := s.Run(ctx, "go")
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()
	events := collectEvents(t, ch)

	var endReason string
	for _, ev := range events {
		if ev.Kind == EventEnd {
			endReason = ev.EndReason
		}
	}
	if endReason != EndReasonCancelled {
		t.Errorf("end = %q; want cancelled", endReason)
	}
}

func TestRun_StrictHistoryReadError(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "hi"},
		{Kind: llm.EventEnd},
	}})
	sentinel := errors.New("read boom")
	hist := errorHistory{errOnRead: sentinel}
	s := newTestSession(t, stub, nil, hist)

	ch, _ := s.Run(context.Background(), "go")
	events := collectEvents(t, ch)

	var sawErr bool
	var endReason string
	for _, ev := range events {
		if ev.Kind == EventError && ev.Err != nil && strings.Contains(ev.Err.Error(), "read boom") {
			sawErr = true
		}
		if ev.Kind == EventEnd {
			endReason = ev.EndReason
		}
	}
	if !sawErr {
		t.Error("expected EventError carrying the History error")
	}
	if endReason != EndReasonError {
		t.Errorf("end = %q; want error", endReason)
	}
}

func TestRun_StrictHistoryAppendError(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "hi"},
		{Kind: llm.EventEnd},
	}})
	sentinel := errors.New("append boom")
	hist := errorHistory{errOnAppend: sentinel}
	s := newTestSession(t, stub, nil, hist)

	ch, _ := s.Run(context.Background(), "go")
	events := collectEvents(t, ch)

	var endReason string
	var sawErr bool
	for _, ev := range events {
		if ev.Kind == EventError && strings.Contains(ev.Err.Error(), "append boom") {
			sawErr = true
		}
		if ev.Kind == EventEnd {
			endReason = ev.EndReason
		}
	}
	if !sawErr {
		t.Error("expected EventError")
	}
	if endReason != EndReasonError {
		t.Errorf("end = %q; want error", endReason)
	}
}

func TestRun_StrictLockerError(t *testing.T) {
	stub := llmtest.NewStubClient()
	sentinel := errors.New("lock boom")
	o := &SessionOptions{
		ID: "s-test", Client: stub, Model: "m",
		History: memory.NewHistory(), Locker: errorLocker{err: sentinel},
	}
	s, err := NewSession(o)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ch, _ := s.Run(context.Background(), "go")
	events := collectEvents(t, ch)

	var sawErr bool
	var endReason string
	for _, ev := range events {
		if ev.Kind == EventError && strings.Contains(ev.Err.Error(), "lock boom") {
			sawErr = true
		}
		if ev.Kind == EventEnd {
			endReason = ev.EndReason
		}
	}
	if !sawErr {
		t.Error("expected EventError")
	}
	if endReason != EndReasonError {
		t.Errorf("end = %q; want error", endReason)
	}
}

func TestRun_StreamDialError(t *testing.T) {
	sentinel := errors.New("dial fail")
	hist := memory.NewHistory()
	s, err := NewSession(&SessionOptions{
		ID: "s", Client: dialErrClient{err: sentinel}, Model: "m",
		History: hist, Locker: NewLocalLocker(),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ch, _ := s.Run(context.Background(), "go")
	events := collectEvents(t, ch)
	var endReason string
	for _, ev := range events {
		if ev.Kind == EventEnd {
			endReason = ev.EndReason
		}
	}
	if endReason != EndReasonError {
		t.Errorf("end = %q; want error", endReason)
	}
	stored, _ := hist.Read(context.Background(), "s")
	if len(stored) != 0 {
		t.Error("History should not be appended on dial error")
	}
}

func TestRun_LocalLocker_SerializesSameID_AcrossDistinctSessions(t *testing.T) {
	// Two distinct *Session, same ID, sharing one Locker → must serialize.
	locker := NewLocalLocker()
	hist := memory.NewHistory()

	mkClient := func(delay time.Duration) llm.Client {
		return &delayClient{delay: delay}
	}
	mkSession := func(client llm.Client) *Session {
		s, err := NewSession(&SessionOptions{
			ID: "shared", Client: client, Model: "m",
			History: hist, Locker: locker, MaxTurns: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s1 := mkSession(mkClient(120 * time.Millisecond))
	s2 := mkSession(mkClient(0))

	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ch, _ := s1.Run(context.Background(), "first")
		for range ch {
		}
	}()
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond) // give s1 a head start to grab the lock
		ch, _ := s2.Run(context.Background(), "second")
		for range ch {
		}
	}()
	wg.Wait()
	elapsed := time.Since(start)

	// If serialized, total wall-time >= 120ms (s1's delay).
	if elapsed < 100*time.Millisecond {
		t.Errorf("Run calls did not serialize: total=%v, expected >= 100ms", elapsed)
	}

	// History should contain both turns from both sessions, in serialized order.
	stored, _ := hist.Read(context.Background(), "shared")
	if len(stored) != 4 { // 2x (user + assistant)
		t.Errorf("stored len = %d; want 4", len(stored))
	}
}

func TestRun_LocalLocker_DifferentIDsRunInParallel(t *testing.T) {
	locker := NewLocalLocker()
	hist := memory.NewHistory()
	mk := func(id string) *Session {
		s, _ := NewSession(&SessionOptions{
			ID: id, Client: &delayClient{delay: 120 * time.Millisecond}, Model: "m",
			History: hist, Locker: locker,
		})
		return s
	}
	a := mk("a")
	b := mk("b")

	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)
	for _, s := range []*Session{a, b} {
		s := s
		go func() {
			defer wg.Done()
			ch, _ := s.Run(context.Background(), "go")
			for range ch {
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	// If parallel, total ≈ 120ms (not 240ms).
	if elapsed > 220*time.Millisecond {
		t.Errorf("different IDs should run in parallel: elapsed=%v", elapsed)
	}
}

// delayClient sends one EventTextDelta after `delay`, then ends.
type delayClient struct{ delay time.Duration }

func (d *delayClient) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	ch := make(chan llm.Event, 2)
	go func() {
		defer close(ch)
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.delay):
		}
		select {
		case ch <- llm.Event{Kind: llm.EventTextDelta, Text: "x"}:
		case <-ctx.Done():
			return
		}
		select {
		case ch <- llm.Event{Kind: llm.EventEnd}:
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

func TestNewLocalSession_AutoSuppliesDefaults(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "hi"},
		{Kind: llm.EventEnd},
	}})
	s, err := NewLocalSession(&SessionOptions{
		ID: "x", Client: stub, Model: "m",
	})
	if err != nil {
		t.Fatalf("NewLocalSession: %v", err)
	}
	if s.opts.History == nil {
		t.Error("History should be auto-supplied")
	}
	if s.opts.Locker == nil {
		t.Error("Locker should be auto-supplied")
	}
	ch, _ := s.Run(context.Background(), "say hi")
	for range ch {
	}
}

func TestNewLocalSession_RespectsProvided(t *testing.T) {
	customHist := memory.NewHistory()
	customLock := NewLocalLocker()
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventEnd}}})
	s, err := NewLocalSession(&SessionOptions{
		ID: "x", Client: stub, Model: "m",
		History: customHist, Locker: customLock,
	})
	if err != nil {
		t.Fatalf("NewLocalSession: %v", err)
	}
	if s.opts.History != customHist {
		t.Error("custom History was overwritten")
	}
	if s.opts.Locker != customLock {
		t.Error("custom Locker was overwritten")
	}
}
