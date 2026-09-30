package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/boxsie/smith/internal/runtime"
)

// ProjectFind implements the project.find tool.
// Searches for files matching a glob pattern within the scoped root.
type ProjectFind struct{}

func (p *ProjectFind) RequiredScope() []string { return []string{ScopeRoot} }

func (p *ProjectFind) Definition() runtime.ToolDef {
	return runtime.ToolDef{
		ID:          "project.find",
		Description: "Search for files matching a glob pattern within the project",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Glob pattern to match (supports **)"}},"required":["pattern"]}`),
	}
}

func (p *ProjectFind) Execute(_ context.Context, input json.RawMessage, scope map[string]string) (json.RawMessage, error) {
	var req struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return nil, fmt.Errorf("invalid input: %w", err)
	}
	if req.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	// Validate the pattern by checking non-** segments.
	if err := validateGlobPattern(req.Pattern); err != nil {
		return nil, err
	}

	root := scope[ScopeRoot]
	canonRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}

	var matches []string
	err = filepath.WalkDir(canonRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip .smith directories.
		if d.IsDir() && IsSmithDir(d.Name()) {
			return fs.SkipDir
		}

		rel, err := filepath.Rel(canonRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		// Only match files, not directories.
		if d.IsDir() {
			return nil
		}

		if matchGlob(req.Pattern, rel) {
			matches = append(matches, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	slices.Sort(matches)
	if matches == nil {
		matches = []string{}
	}
	return json.Marshal(matches)
}

// validateGlobPattern checks that non-** segments are valid filepath.Match patterns.
func validateGlobPattern(pattern string) error {
	// Remove ** segments and validate the rest.
	parts := strings.Split(filepath.ToSlash(pattern), "/")
	for _, p := range parts {
		if p == "**" {
			continue
		}
		if _, err := filepath.Match(p, ""); err != nil {
			return fmt.Errorf("invalid glob pattern: %w", err)
		}
	}
	return nil
}

// matchGlob matches a path against a glob pattern supporting **.
// ** matches zero or more directory components.
func matchGlob(pattern, path string) bool {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)
	return matchGlobParts(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

func matchGlobParts(patParts, pathParts []string) bool {
	for len(patParts) > 0 {
		pat := patParts[0]

		if pat == "**" {
			patParts = patParts[1:]
			// ** at end matches everything remaining.
			if len(patParts) == 0 {
				return true
			}
			// Try matching the rest of the pattern at every position.
			for i := range len(pathParts) + 1 {
				if matchGlobParts(patParts, pathParts[i:]) {
					return true
				}
			}
			return false
		}

		// No more path parts but pattern still has non-** segments.
		if len(pathParts) == 0 {
			return false
		}

		matched, _ := filepath.Match(pat, pathParts[0])
		if !matched {
			return false
		}

		patParts = patParts[1:]
		pathParts = pathParts[1:]
	}

	return len(pathParts) == 0
}
