package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/boxsie/smith/internal/lib"
)

// ResolvedTools holds the results of tool discovery across the resolution chain.
// It contains only app/lib-defined tools — built-in definitions come from
// Registry.Definitions() and are merged by the caller.
type ResolvedTools struct {
	// AppDefs maps tool ID to parsed definition (app and lib tools only).
	AppDefs map[string]*AppToolDef
	// Sources tracks provenance: tool ID → "app" or "lib:<path>".
	Sources map[string]string
}

// DiscoverAndExtract auto-extracts embedded tool definitions to the user lib
// directory, then runs Discover. Use this when the caller wants to ensure
// embedded tools are available (e.g. plan.go's tool registration). Use plain
// Discover when the caller should be read-only (e.g. building planner context).
func DiscoverAndExtract(projectRoot string, builtinIDs []string, nativeIDs []string) (*ResolvedTools, error) {
	userLibDir, err := lib.UserLibDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user lib dir: %w", err)
	}
	lib.ExtractEmbeddedTools(userLibDir)
	return Discover(projectRoot, builtinIDs, nativeIDs)
}

// Discover walks the tool resolution chain and returns all app/lib-defined tools.
// Resolution order: app-local → project lib → user lib → embedded lib.
// First match wins for duplicate IDs across layers.
// builtinIDs is used for shadowing checks — built-in IDs cannot be defined by app/lib.
// This function is read-only — it does not extract embedded tools. Use
// DiscoverAndExtract if embedded extraction is desired.
func Discover(projectRoot string, builtinIDs []string, nativeIDs []string) (*ResolvedTools, error) {
	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve project root: %w", err)
	}

	userLibDir, err := lib.UserLibDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user lib dir: %w", err)
	}

	result := &ResolvedTools{
		AppDefs: make(map[string]*AppToolDef),
		Sources: make(map[string]string),
	}

	// Resolution layers in priority order.
	type layer struct {
		dir    string
		source string // provenance label
	}

	layers := []layer{
		{filepath.Join(absRoot, "tools"), "app"},
		{filepath.Join(lib.ProjectLibDir(absRoot), "tools"), "lib:" + lib.ProjectLibDir(absRoot)},
		{filepath.Join(userLibDir, "tools"), "lib:" + userLibDir},
	}

	for _, l := range layers {
		defs, err := walkToolsDir(l.dir)
		if err != nil {
			return nil, fmt.Errorf("discover tools in %s: %w", l.dir, err)
		}
		for id, def := range defs {
			// Check built-in shadowing.
			if slices.Contains(builtinIDs, id) {
				return nil, fmt.Errorf("tool %q shadows a built-in tool (shadowing built-ins is forbidden)", id)
			}
			// First match wins — skip if already resolved.
			if _, exists := result.AppDefs[id]; exists {
				continue
			}
			if def.ToolYAML.Type == "native" && !slices.Contains(nativeIDs, id) {
				return nil, fmt.Errorf("tool %q declares type native but no compiled handler is registered", id)
			}
			def.ID = id
			result.AppDefs[id] = def
			result.Sources[id] = l.source
		}
	}

	// Resolve source paths for task tools.
	for id, def := range result.AppDefs {
		if def.ToolYAML.Type != "task" {
			continue
		}
		resolved, err := resolveToolSource(def.ToolYAML.Source, def.Dir, absRoot)
		if err != nil {
			return nil, fmt.Errorf("tool %q: resolve source %q: %w", id, def.ToolYAML.Source, err)
		}
		def.ResolvedSource = resolved
	}

	return result, nil
}

// resolveToolSource resolves a task tool's source reference.
// Relative paths (starting with "./" or "../") are resolved from the tool definition dir.
// Everything else is treated as a module name resolved through the standard lib chain.
func resolveToolSource(source, toolDir, projectRoot string) (string, error) {
	if strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
		// Relative path — resolve from tool definition directory.
		abs := filepath.Join(toolDir, source)
		abs, err := filepath.Abs(abs)
		if err != nil {
			return "", fmt.Errorf("resolve relative path: %w", err)
		}
		taskMD := filepath.Join(abs, "task.md")
		if _, err := os.Stat(taskMD); err != nil {
			return "", fmt.Errorf("source path %q does not contain task.md", abs)
		}
		return abs, nil
	}
	// Module name — resolve through lib chain.
	return lib.ResolveModule(source, projectRoot)
}

// walkToolsDir reads a single tools/ directory and parses each tool subdirectory.
// Returns an empty map (not error) if the directory does not exist.
// Bare files in tools/ and subdirectories without tool.yaml are validation errors.
func walkToolsDir(dir string) (map[string]*AppToolDef, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	defs := make(map[string]*AppToolDef)
	for _, entry := range entries {
		name := entry.Name()

		// Reject bare files.
		if !entry.IsDir() {
			return nil, fmt.Errorf("tools/ must contain only directories, found file %q", name)
		}

		// Parse the tool definition.
		toolDir := filepath.Join(dir, name)
		def, err := ParseToolDir(toolDir)
		if err != nil {
			return nil, fmt.Errorf("tool %q: %w", name, err)
		}
		defs[name] = def
	}

	return defs, nil
}
