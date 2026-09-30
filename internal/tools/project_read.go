package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"unicode/utf8"

	"github.com/boxsie/smith/internal/runtime"
)

// ProjectRead implements the project.read tool.
// Reads file contents as UTF-8 string relative to the scoped root.
type ProjectRead struct{}

func (p *ProjectRead) RequiredScope() []string { return []string{ScopeRoot} }

func (p *ProjectRead) Definition() runtime.ToolDef {
	return runtime.ToolDef{
		ID:          "project.read",
		Description: "Read file contents as UTF-8 text relative to project root",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative file path to read"}},"required":["path"]}`),
	}
}

func (p *ProjectRead) Execute(_ context.Context, input json.RawMessage, scope map[string]string) (json.RawMessage, error) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return nil, fmt.Errorf("invalid input: %w", err)
	}
	if req.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	// Reject .smith/ paths before resolving.
	if HasSmithComponent(req.Path) {
		return nil, fmt.Errorf("path rejected: .smith/ is runner-private")
	}

	resolved, err := SafeResolve(scope[ScopeRoot], req.Path)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %q", req.Path)
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory: %q", req.Path)
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, err
	}

	if isBinary(data) {
		return nil, fmt.Errorf("binary file not supported: %q", req.Path)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("invalid UTF-8: %q", req.Path)
	}

	type result struct {
		Content string `json:"content"`
		Path    string `json:"path"`
	}
	return json.Marshal(result{Content: string(data), Path: req.Path})
}

// isBinary checks if content contains null bytes in the first 8192 bytes.
// Same heuristic as internal/prompt/static.go.
func isBinary(data []byte) bool {
	limit := min(len(data), 8192)
	for i := range limit {
		if data[i] == 0 {
			return true
		}
	}
	return false
}
