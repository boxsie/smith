package tools

import "context"

// MaxToolDepth is the maximum task-tool invocation depth.
// A task tool may call a task tool may call a task tool, but no deeper.
const MaxToolDepth = 3

type depthKey struct{}

// WithDepth returns a context carrying the given tool invocation depth.
func WithDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, depthKey{}, depth)
}

// Depth returns the current tool invocation depth from the context.
// Returns 0 if no depth has been set.
func Depth(ctx context.Context) int {
	if v, ok := ctx.Value(depthKey{}).(int); ok {
		return v
	}
	return 0
}
