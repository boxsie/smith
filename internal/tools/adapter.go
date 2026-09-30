package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// Adapter executes tool calls. Permission checks (whitelist) are NOT this layer's
// responsibility — they belong in the executor's tool call loop.
type Adapter interface {
	Execute(ctx context.Context, toolID string, input json.RawMessage) (json.RawMessage, error)
}

// ScopedAdapter extends Adapter with scope injection.
// WithScope returns a new adapter with the given scope applied.
// Scope values are immutable after WithScope.
type ScopedAdapter interface {
	Adapter
	WithScope(scope map[string]string) ScopedAdapter
}

// Scope parameter constants.
const (
	ScopeRoot       = "root"
	ScopeProposalID = "proposal_id"
)

// FakeAdapter is a test double that dispatches to registered handler functions.
type FakeAdapter struct {
	Handlers map[string]func(json.RawMessage) (json.RawMessage, error)
}

// Execute dispatches to the handler registered for toolID.
// Returns an error if no handler is registered.
func (f *FakeAdapter) Execute(_ context.Context, toolID string, input json.RawMessage) (json.RawMessage, error) {
	h, ok := f.Handlers[toolID]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %q", toolID)
	}
	return h(input)
}
