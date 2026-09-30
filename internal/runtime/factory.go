package runtime

import (
	"fmt"
	"strings"
)

// Factory resolves model strings to Provider instances.
type Factory struct {
	// Override, when set, bypasses prefix dispatch entirely.
	// Use in tests to inject providers without network calls.
	Override func(model string) (Provider, error)
}

// Resolve maps a model string like "anthropic/claude-sonnet-4-6" to a Provider.
func (f *Factory) Resolve(model string) (Provider, error) {
	if f.Override != nil {
		return f.Override(model)
	}

	prefix, name, ok := strings.Cut(model, "/")
	if !ok {
		return nil, fmt.Errorf("invalid model format %q: expected prefix/name", model)
	}

	switch prefix {
	case "anthropic":
		return NewAnthropicProvider(name)
	case "ollama":
		return NewOllamaProvider(name)
	case "mock":
		return &MockProvider{Default: "mock response for " + name}, nil
	default:
		return nil, fmt.Errorf("unsupported model provider %q in model %q", prefix, model)
	}
}

// DefaultFactory returns a factory with no overrides.
func DefaultFactory() *Factory {
	return &Factory{}
}
