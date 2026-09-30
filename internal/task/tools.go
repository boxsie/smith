package task

import (
	"fmt"
	"strings"
)

// ParseToolsMD parses tools.md content: a markdown bullet list of tool IDs.
func ParseToolsMD(content []byte) ([]string, error) {
	lines := strings.Split(string(content), "\n")
	seen := make(map[string]bool)
	var tools []string

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Reject indented lines (nested bullets, descriptions)
		if line != strings.TrimLeft(line, " \t") {
			return nil, fmt.Errorf("%w: %q", ErrInvalidToolLine, strings.TrimSpace(line))
		}

		trimmed := strings.TrimSpace(line)
		var toolID string
		if after, ok := strings.CutPrefix(trimmed, "- "); ok {
			toolID = strings.TrimSpace(after)
		} else if after, ok := strings.CutPrefix(trimmed, "* "); ok {
			toolID = strings.TrimSpace(after)
		} else {
			return nil, fmt.Errorf("%w: %q", ErrInvalidToolLine, trimmed)
		}

		if toolID == "" {
			continue
		}
		if seen[toolID] {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateTool, toolID)
		}
		seen[toolID] = true
		tools = append(tools, toolID)
	}

	return tools, nil
}
