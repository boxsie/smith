package tools

import (
	"fmt"
	"strings"
)

// ParseScope parses raw --scope flag values into a scope map.
// Each raw value must be in the form "key=value".
// Duplicate keys are rejected. Values may contain '='.
func ParseScope(raw []string) (map[string]string, error) {
	scope := make(map[string]string, len(raw))
	for _, r := range raw {
		key, value, ok := strings.Cut(r, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --scope format: %q (expected key=value)", r)
		}
		if key == "" {
			return nil, fmt.Errorf("invalid --scope: empty key in %q", r)
		}
		if _, exists := scope[key]; exists {
			return nil, fmt.Errorf("duplicate --scope key: %q", key)
		}
		scope[key] = value
	}
	return scope, nil
}
