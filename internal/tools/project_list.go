package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/boxsie/smith/internal/runtime"
)

// ProjectList implements the project.list tool.
// Lists files and directories at a path relative to the scoped root.
type ProjectList struct{}

func (p *ProjectList) RequiredScope() []string { return []string{ScopeRoot} }

func (p *ProjectList) Definition() runtime.ToolDef {
	return runtime.ToolDef{
		ID:          "project.list",
		Description: "List files and directories at a path relative to project root",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative directory path to list (defaults to root)"}}}`),
	}
}

func (p *ProjectList) Execute(_ context.Context, input json.RawMessage, scope map[string]string) (json.RawMessage, error) {
	var req struct {
		Path string `json:"path"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &req); err != nil {
			return nil, fmt.Errorf("invalid input: %w", err)
		}
	}
	if req.Path == "" {
		req.Path = "."
	}

	// Reject .smith/ paths — runner-private, consistent with project.read.
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
			return nil, fmt.Errorf("path not found: %q", req.Path)
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %q", req.Path)
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, err
	}

	type entry struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Size int64  `json:"size"`
	}

	result := make([]entry, 0, len(entries))
	for _, e := range entries {
		if IsSmithDir(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			return nil, err
		}
		typ := "file"
		var size int64
		if fi.IsDir() {
			typ = "directory"
		} else {
			size = fi.Size()
		}
		result = append(result, entry{
			Name: e.Name(),
			Type: typ,
			Size: size,
		})
	}

	return json.Marshal(result)
}
