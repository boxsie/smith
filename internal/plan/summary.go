package plan

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/boxsie/smith/internal/executor"
	"github.com/boxsie/smith/internal/output"
	"github.com/boxsie/smith/internal/proposal"
	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/task"
	"github.com/boxsie/smith/internal/tools"
	"github.com/boxsie/smith/internal/validate"
)

// ProjectSummary holds the structured output of BuildProjectSummary.
type ProjectSummary struct {
	Text            string                  // human-readable summary for planner run input
	ModulesResolved []proposal.ModuleRecord // module records for provenance
}

// BuildProjectSummary generates a structured summary of the target directory
// for the planner. It describes the task tree shape, sidecar file presence,
// dependencies (both explicit and implicit prefix ordering), validation status,
// and per-task execution status. It also collects module provenance records.
func BuildProjectSummary(targetDir string) (*ProjectSummary, error) {
	absDir, err := filepath.Abs(targetDir)
	if err != nil {
		return nil, fmt.Errorf("resolve target dir: %w", err)
	}

	// Check if the directory exists.
	info, err := os.Stat(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &ProjectSummary{Text: "Empty directory. No existing Smith task tree."}, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", absDir)
	}

	// Check if a task tree root exists.
	hasTaskMD := fileExists(filepath.Join(absDir, "task.md"))
	hasModuleYAML := fileExists(filepath.Join(absDir, "module.yaml"))
	if !hasTaskMD && !hasModuleYAML {
		text := "Empty directory. No existing Smith task tree.\n"
		text += formatToolInventory(absDir)
		return &ProjectSummary{Text: text}, nil
	}

	// Run full validation to get the graph (which includes implicit prefix deps).
	vr := validate.Validate(absDir)

	// If discovery itself failed, produce a structured summary with a raw
	// file listing so the planner knows what exists on disk for repair.
	if vr.Root == nil {
		var sb strings.Builder
		sb.WriteString("Directory contains task files but is not a valid Smith task tree.\n")
		sb.WriteString("Validation: fail\n")
		for _, e := range vr.Errs {
			fmt.Fprintf(&sb, "  %s\n", e)
		}
		sb.WriteString("\nFiles on disk:\n")
		scanProjectFiles(absDir, &sb)
		sb.WriteString(formatToolInventory(absDir))
		return &ProjectSummary{Text: sb.String()}, nil
	}
	root := vr.Root

	// Build a dependency map from the graph (includes both explicit depends_on
	// and implicit numeric-prefix ordering). This is the real execution graph.
	depMap := make(map[string][]string) // task ID → list of dependency IDs
	if vr.Graph != nil {
		for id, deps := range vr.Graph.SiblingDeps {
			for _, dep := range deps {
				depMap[id] = append(depMap[id], dep.ID)
			}
		}
	}

	// Count tasks and collect module records.
	taskCount := 0
	var modules []proposal.ModuleRecord
	task.WalkTree(root, func(t *task.Task) {
		taskCount++
		if t.ModuleRef != nil && t.SourcePath != "" {
			hash, _ := proposal.HashTaskTree(t.SourcePath)
			modules = append(modules, proposal.ModuleRecord{
				Name:         t.ModuleRef.Source,
				ResolvedFrom: t.SourcePath,
				ContentHash:  hash,
			})
		}
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "Task tree with %d task(s):\n", taskCount)

	// Walk tree and describe each task.
	task.WalkTree(root, func(t *task.Task) {
		id := t.ID
		if id == "" {
			id = "(root)"
		}
		indent := taskIndent(t)

		// Model info.
		model := ""
		if t.Agent != nil && t.Agent.Model != "" {
			model = t.Agent.Model
		} else if t.Parent != nil {
			model = "(inherited)"
		}

		// Execution status — try manifest first, then .metrics.json, then heuristic.
		status := taskStatusWithCached(t, absDir)

		fmt.Fprintf(&sb, "%s%s", indent, id)
		if model != "" {
			fmt.Fprintf(&sb, " - model: %s", model)
		}
		fmt.Fprintf(&sb, ", status: %s\n", status)

		// Dependencies from the execution graph (explicit + implicit).
		if deps := depMap[t.ID]; len(deps) > 0 {
			fmt.Fprintf(&sb, "%s  depends_on: [%s]\n", indent, strings.Join(deps, ", "))
		}

		// Sidecar presence.
		sidecars := listSidecars(t)
		if len(sidecars) > 0 {
			fmt.Fprintf(&sb, "%s  sidecars: %s\n", indent, strings.Join(sidecars, ", "))
		}
	})

	// Validation status.
	if len(vr.Errs) > 0 {
		sb.WriteString("Validation: fail\n")
		for _, e := range vr.Errs {
			fmt.Fprintf(&sb, "  %s\n", e)
		}
	} else {
		sb.WriteString("Validation: pass\n")
	}

	sb.WriteString(formatToolInventory(absDir))

	return &ProjectSummary{
		Text:            sb.String(),
		ModulesResolved: modules,
	}, nil
}

// scanProjectFiles does a basic directory walk and lists Smith-relevant files
// (task.md, module.yaml, agent.md, tools.md, schema.md) to give the planner
// context about what exists on disk when full discovery fails.
func scanProjectFiles(dir string, sb *strings.Builder) {
	relevant := map[string]bool{
		"task.md":     true,
		"module.yaml": true,
		"agent.md":    true,
		"tools.md":    true,
		"schema.md":   true,
	}
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if d.IsDir() {
			if d.Name() == "output" || d.Name() == ".smith" {
				return filepath.SkipDir
			}
			return nil
		}
		if relevant[d.Name()] {
			rel, _ := filepath.Rel(dir, path)
			fmt.Fprintf(sb, "  %s\n", filepath.ToSlash(rel))
		}
		return nil
	})
}

// taskIndent returns indentation based on nesting depth.
func taskIndent(t *task.Task) string {
	depth := 0
	for p := t.Parent; p != nil; p = p.Parent {
		depth++
	}
	return strings.Repeat("  ", depth)
}

// taskStatusWithCached checks the latest run manifest first, then falls back to
// .metrics.json and filesystem heuristics for legacy apps without run history.
func taskStatusWithCached(t *task.Task, appRoot string) string {
	// Try manifest-backed status first (RFC 0006).
	if m, err := run.LatestManifest(appRoot); err == nil {
		for _, mt := range m.Tasks {
			if mt.TaskID == t.ID {
				return mt.Status
			}
		}
	}

	// Fall back to local .metrics.json (pre-v6 runs or planner sub-executions).
	metricsPath := filepath.Join(t.Path, "output", ".metrics.json")
	data, err := os.ReadFile(metricsPath)
	if err == nil {
		var m output.Metrics
		if json.Unmarshal(data, &m) == nil {
			if m.Cached {
				return "cached"
			}
			return m.Status
		}
	}
	return executor.TaskStatus(t.Path, t.Frontmatter.OutputType(), t.HasReturn)
}

// listSidecars returns the names of sidecar files present for a task.
// For module-backed tasks, checks both the source path (module defaults)
// and the runtime path (project-local overrides) so that overrides are
// not hidden from the planner.
func listSidecars(t *task.Task) []string {
	// Collect unique sidecars from both source and runtime paths.
	seen := make(map[string]bool)
	dirs := []string{t.EffectiveSourcePath()}
	if t.SourcePath != "" && t.Path != t.SourcePath {
		dirs = append(dirs, t.Path)
	}

	for _, dir := range dirs {
		for _, name := range []string{"agent.md", "tools.md", "schema.md", "return.md"} {
			if fileExists(filepath.Join(dir, name)) {
				seen[name] = true
			}
		}
		if dirExists(filepath.Join(dir, "context", "static")) {
			seen["context/static/"] = true
		}
	}

	var sidecars []string
	for _, name := range []string{"agent.md", "tools.md", "schema.md", "return.md", "context/static/"} {
		if seen[name] {
			sidecars = append(sidecars, name)
		}
	}
	return sidecars
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// formatToolInventory returns a formatted "Tools:" section listing both
// discovered app/lib tools and built-in tools. If discovery fails, a warning
// is emitted but built-in tools are still listed.
func formatToolInventory(projectRoot string) string {
	type entry struct {
		id      string
		source  string
		kind    string // "shell", "task", or "" for builtins
		desc    string
		summary string // input field summary
	}

	var entries []entry

	// Discover app/lib tools.
	var discoverErr error
	resolved, err := tools.Discover(projectRoot, tools.BuiltinToolIDs, tools.NativeToolIDs())
	if err != nil {
		discoverErr = err
	} else {
		for id, def := range resolved.AppDefs {
			source := resolved.Sources[id]
			// Normalize lib provenance to a compact label. Discovery
			// stores it as "lib:<abs-path>" which leaks host-specific
			// paths into the planner prompt.
			if strings.HasPrefix(source, "lib:") {
				source = "lib"
			}
			entries = append(entries, entry{
				id:      id,
				source:  source,
				kind:    def.ToolYAML.Type,
				desc:    def.ToolYAML.Description,
				summary: inputSummary(def.AuthorSchema),
			})
		}
	}

	// Built-in tools visible to app tasks. proposal.write is excluded because
	// it is a proposal-staging tool that requires proposal_id scope and is not
	// a general-purpose capability available to app runs.
	builtinHandlers := []tools.ToolHandler{
		&tools.ProjectRead{},
		&tools.ProjectList{},
		&tools.ProjectFind{},
	}
	for _, h := range builtinHandlers {
		def := h.Definition()
		entries = append(entries, entry{
			id:      def.ID,
			source:  "builtin",
			desc:    def.Description,
			summary: inputSummary(def.InputSchema),
		})
	}

	// Sort by ID for stable output.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].id < entries[j].id
	})

	var sb strings.Builder
	if discoverErr != nil {
		fmt.Fprintf(&sb, "\nTools (discovery warning: %v):\n", discoverErr)
	} else {
		sb.WriteString("\nTools:\n")
	}
	for _, e := range entries {
		if e.source == "builtin" {
			fmt.Fprintf(&sb, "  %s (builtin) - %q %s\n", e.id, e.desc, e.summary)
		} else {
			fmt.Fprintf(&sb, "  %s (%s, %s) - %q %s\n", e.id, e.source, e.kind, e.desc, e.summary)
		}
	}
	return sb.String()
}

// inputSummary parses a JSON Schema and returns a bracketed summary of
// top-level properties, marking required fields with *.
// Example output: [query*, status]
func inputSummary(schema json.RawMessage) string {
	if len(schema) == 0 {
		return "[]"
	}

	var s struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		return "[]"
	}

	reqSet := make(map[string]bool, len(s.Required))
	for _, r := range s.Required {
		reqSet[r] = true
	}

	// Collect property names in sorted order.
	names := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		names = append(names, name)
	}
	sort.Strings(names)

	var parts []string
	for _, name := range names {
		if reqSet[name] {
			parts = append(parts, name+"*")
		} else {
			parts = append(parts, name)
		}
	}

	return "[" + strings.Join(parts, ", ") + "]"
}
