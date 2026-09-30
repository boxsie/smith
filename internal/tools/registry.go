package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/boxsie/smith/internal/runtime"
)

// ToolHandler implements a single tool's logic.
type ToolHandler interface {
	// RequiredScope returns the scope keys this tool requires.
	// Empty slice means no scope required.
	RequiredScope() []string

	// Execute runs the tool with the given input and scope.
	// Scope is guaranteed to contain all required keys with non-empty values
	// when called through the Registry.
	Execute(ctx context.Context, input json.RawMessage, scope map[string]string) (json.RawMessage, error)

	// Definition returns the tool's rich metadata for provider integration.
	Definition() runtime.ToolDef
}

// Registry dispatches tool calls to registered ToolHandlers.
// It implements both Adapter and ScopedAdapter.
//
// Thread safety: handlers are read-only after construction.
// Scope is set once via WithScope before execution begins.
// Concurrent Execute calls on the same Registry are safe.
type Registry struct {
	handlers map[string]ToolHandler
	scope    map[string]string // nil until WithScope is called
}

// NewRegistry creates an empty registry with no tools or scope.
func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]ToolHandler),
	}
}

// Register adds a tool handler. Must be called before any Execute or WithScope calls.
func (r *Registry) Register(toolID string, handler ToolHandler) {
	r.handlers[toolID] = handler
}

// Execute satisfies the Adapter interface.
// Dispatches to the registered handler after validating scope requirements.
func (r *Registry) Execute(ctx context.Context, toolID string, input json.RawMessage) (json.RawMessage, error) {
	h, ok := r.handlers[toolID]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %q", toolID)
	}

	// Validate scope requirements.
	for _, key := range h.RequiredScope() {
		val, exists := r.scope[key]
		if !exists {
			return nil, fmt.Errorf("tool %q requires scope %q but it was not set", toolID, key)
		}
		if val == "" {
			return nil, fmt.Errorf("tool %q requires scope %q but it is empty", toolID, key)
		}
	}

	return h.Execute(ctx, input, r.scope)
}

// Definitions returns rich ToolDef metadata for all registered handlers.
func (r *Registry) Definitions() map[string]runtime.ToolDef {
	defs := make(map[string]runtime.ToolDef, len(r.handlers))
	for id, h := range r.handlers {
		defs[id] = h.Definition()
	}
	return defs
}

// WithScope returns a new Registry with an immutable copy of the scope map.
// The returned Registry shares handlers (read-only) but has its own scope.
func (r *Registry) WithScope(scope map[string]string) ScopedAdapter {
	copied := make(map[string]string, len(scope))
	maps.Copy(copied, scope)
	return &Registry{
		handlers: r.handlers,
		scope:    copied,
	}
}
