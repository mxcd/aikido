package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/mxcd/aikido/llm"
)

func TestRegistry_RegisterAndDispatch(t *testing.T) {
	reg := NewRegistry()

	type echoArgs struct {
		Msg string `json:"msg"`
	}
	var seenEnv Env
	var seenArgs echoArgs

	def := llm.ToolDef{Name: "echo", Description: "echo", Parameters: Object(map[string]any{
		"msg": String("text"),
	}, "msg")}
	if err := reg.Register(def, func(ctx context.Context, args json.RawMessage, env Env) (Result, error) {
		seenEnv = env
		if err := json.Unmarshal(args, &seenArgs); err != nil {
			return Result{}, err
		}
		return Result{Content: seenArgs.Msg, Display: "echoed " + seenArgs.Msg}, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Second tool, different name.
	if err := reg.Register(llm.ToolDef{Name: "noop"}, func(ctx context.Context, args json.RawMessage, env Env) (Result, error) {
		return Result{Content: "ok"}, nil
	}); err != nil {
		t.Fatalf("register noop: %v", err)
	}

	if !reg.Has("echo") {
		t.Error("Has(echo) = false; want true")
	}
	if !reg.Has("noop") {
		t.Error("Has(noop) = false; want true")
	}
	if reg.Has("nope") {
		t.Error("Has(nope) = true; want false")
	}

	defs := reg.Defs()
	if len(defs) != 2 {
		t.Fatalf("len(defs) = %d, want 2", len(defs))
	}
	if defs[0].Name != "echo" || defs[1].Name != "noop" {
		t.Errorf("defs order = [%s, %s], want [echo, noop] (registration order)", defs[0].Name, defs[1].Name)
	}

	tid := uuid.New()
	res, err := reg.Dispatch(context.Background(), llm.ToolCall{
		ID:        "c1",
		Name:      "echo",
		Arguments: `{"msg":"hello"}`,
	}, Env{SessionID: "s-1", TurnID: tid})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if seenArgs.Msg != "hello" {
		t.Errorf("seenArgs.Msg = %q", seenArgs.Msg)
	}
	if seenEnv.SessionID != "s-1" || seenEnv.TurnID != tid {
		t.Errorf("seenEnv = %+v", seenEnv)
	}
	if res.Content != "hello" || res.Display != "echoed hello" {
		t.Errorf("res = %+v", res)
	}
}

func TestRegistry_DuplicateTool(t *testing.T) {
	reg := NewRegistry()
	noop := func(ctx context.Context, args json.RawMessage, env Env) (Result, error) {
		return Result{}, nil
	}
	if err := reg.Register(llm.ToolDef{Name: "x"}, noop); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := reg.Register(llm.ToolDef{Name: "x"}, noop)
	if !errors.Is(err, ErrDuplicateTool) {
		t.Errorf("err = %v, want ErrDuplicateTool", err)
	}
}

func TestRegistry_DispatchUnknown(t *testing.T) {
	reg := NewRegistry()
	_, err := reg.Dispatch(context.Background(), llm.ToolCall{Name: "ghost"}, Env{})
	if !errors.Is(err, ErrUnknownTool) {
		t.Errorf("err = %v, want ErrUnknownTool", err)
	}
}

func TestRegistry_RegisterEmptyName(t *testing.T) {
	reg := NewRegistry()
	noop := func(ctx context.Context, args json.RawMessage, env Env) (Result, error) {
		return Result{}, nil
	}
	if err := reg.Register(llm.ToolDef{Name: ""}, noop); err == nil {
		t.Error("expected error for empty name")
	}
}

func TestRegistry_RegisterNilHandler(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(llm.ToolDef{Name: "x"}, nil); err == nil {
		t.Error("expected error for nil handler")
	}
}

func TestRegistry_DispatchHandlerError(t *testing.T) {
	reg := NewRegistry()
	sentinel := errors.New("boom")
	if err := reg.Register(llm.ToolDef{Name: "fail"}, func(ctx context.Context, args json.RawMessage, env Env) (Result, error) {
		return Result{}, sentinel
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, err := reg.Dispatch(context.Background(), llm.ToolCall{Name: "fail"}, Env{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}
