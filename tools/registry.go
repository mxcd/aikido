package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/mxcd/aikido/llm"
)

// Registry holds tool definitions and their handlers.
type Registry struct {
	mu       sync.RWMutex
	order    []string
	defs     map[string]llm.ToolDef
	handlers map[string]Handler
}

func NewRegistry() *Registry {
	return &Registry{
		defs:     make(map[string]llm.ToolDef),
		handlers: make(map[string]Handler),
	}
}

// Register adds a tool. Returns ErrDuplicateTool if the name is taken.
func (r *Registry) Register(def llm.ToolDef, h Handler) error {
	if def.Name == "" {
		return fmt.Errorf("aikido/tools: tool def has empty name")
	}
	if h == nil {
		return fmt.Errorf("aikido/tools: handler for %q is nil", def.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.defs[def.Name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateTool, def.Name)
	}
	r.defs[def.Name] = def
	r.handlers[def.Name] = h
	r.order = append(r.order, def.Name)
	return nil
}

// Defs returns all registered ToolDefs in registration order.
func (r *Registry) Defs() []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]llm.ToolDef, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.defs[name])
	}
	return out
}

// Has reports whether a tool is registered.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.defs[name]
	return ok
}

// Dispatch routes a ToolCall to its handler.
//
// Returns ErrUnknownTool if the name is not registered. Argument bytes are
// passed verbatim — handlers parse them.
func (r *Registry) Dispatch(ctx context.Context, call llm.ToolCall, env Env) (Result, error) {
	r.mu.RLock()
	h, ok := r.handlers[call.Name]
	r.mu.RUnlock()
	if !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrUnknownTool, call.Name)
	}
	return h(ctx, json.RawMessage(call.Arguments), env)
}
