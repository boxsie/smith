package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/boxsie/smith/internal/input"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/task"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// SubExecConfig holds the configuration for a sub-execution of a task tree.
type SubExecConfig struct {
	Factory         *runtime.Factory
	ExternalFactory *runtime.ExternalFactory
	Adapter         Adapter
	NoCache         bool
	RunInput        []input.Entry
	Scope           map[string]string
	ResolvedDefs    map[string]runtime.ToolDef
	RunID           string // parent run ID for tool-history attribution
}

// SubExecuteFunc executes a task tree. Injected by the CLI to break the
// tools→executor import cycle.
type SubExecuteFunc func(ctx context.Context, root *task.Task, graph *task.Graph, cfg SubExecConfig) error

// TaskToolConfig holds the configuration for creating a TaskToolHandler.
type TaskToolConfig struct {
	SubExecute      SubExecuteFunc
	Factory         *runtime.Factory
	ExternalFactory *runtime.ExternalFactory
	Scope           map[string]string
	ProjectRoot     string
	Adapter         Adapter                    // pre-built adapter for sub-executions (includes built-ins + app tools)
	ResolvedDefs    map[string]runtime.ToolDef // pre-built resolved defs for sub-executions
}

// TaskToolHandler implements ToolHandler for task-backed app-defined tools.
type TaskToolHandler struct {
	def      *AppToolDef
	cfg      TaskToolConfig
	schema   *jsonschema.Schema
	cacheDir string
}

// NewTaskToolHandler creates a handler for the given task tool definition.
func NewTaskToolHandler(def *AppToolDef, cfg TaskToolConfig) (*TaskToolHandler, error) {
	schema, err := compileJSONSchema(def.AuthorSchema)
	if err != nil {
		return nil, fmt.Errorf("compile input schema for %q: %w", def.ID, err)
	}

	cacheDir := filepath.Join(cfg.ProjectRoot, ".smith", "cache", "tools", def.ID)

	return &TaskToolHandler{
		def:      def,
		cfg:      cfg,
		schema:   schema,
		cacheDir: cacheDir,
	}, nil
}

func (h *TaskToolHandler) RequiredScope() []string { return []string{ScopeRoot} }

func (h *TaskToolHandler) Definition() runtime.ToolDef { return h.def.ToToolDef() }

func (h *TaskToolHandler) Execute(ctx context.Context, inputJSON json.RawMessage, scope map[string]string) (json.RawMessage, error) {
	// 1. Validate input.
	if err := validateJSON(h.schema, inputJSON); err != nil {
		return nil, fmt.Errorf("input validation: %w", err)
	}

	// 2. Check depth limit.
	depth := Depth(ctx)
	if depth >= MaxToolDepth {
		return nil, fmt.Errorf("tool invocation depth limit exceeded")
	}

	// 3. Outer cache check.
	if h.def.ToolYAML.Cache == "auto" {
		key := h.outerCacheKey(inputJSON)
		if cached, ok := cacheLookup(h.cacheDir, key); ok {
			return cached, nil
		}
	}

	// 4. Create invocation directory.
	invocationID := fmt.Sprintf("%d", time.Now().UnixNano())
	invocationDir := filepath.Join(h.cfg.ProjectRoot, ".smith", "tool-runs", h.def.ID, invocationID)
	if err := os.MkdirAll(invocationDir, 0o755); err != nil {
		return nil, fmt.Errorf("create invocation dir: %w", err)
	}

	// 5. Write input.json.
	inputPath := filepath.Join(invocationDir, "input.json")
	if err := os.WriteFile(inputPath, inputJSON, 0o644); err != nil {
		return nil, fmt.Errorf("write input.json: %w", err)
	}

	// 6. Discover the source task tree.
	root, err := task.DiscoverTree(h.def.ResolvedSource)
	if err != nil {
		return nil, fmt.Errorf("discover source task tree: %w", err)
	}

	// 7. Redirect output paths to invocation directory.
	task.RedirectPaths(root, h.def.ResolvedSource, invocationDir)

	// 8. Resolve agent inheritance and build graph.
	if err := task.ResolveAgentInheritance(root); err != nil {
		return nil, fmt.Errorf("resolve agent inheritance: %w", err)
	}
	graph, err := task.BuildGraph(root)
	if err != nil {
		return nil, fmt.Errorf("build graph: %w", err)
	}

	// 9. Use call-time scope (from the adapter's WithScope), falling back to config scope.
	effectiveScope := scope
	if effectiveScope == nil {
		effectiveScope = h.cfg.Scope
	}

	// 10. Execute with depth+1 and _json run input.
	// The adapter and resolved defs are pre-built by the caller (run.go / plan.go)
	// to include both built-in and app-defined tools from the project.
	subCtx := WithDepth(ctx, depth+1)
	subCfg := SubExecConfig{
		Factory:         h.cfg.Factory,
		ExternalFactory: h.cfg.ExternalFactory,
		Adapter:         h.cfg.Adapter,
		Scope:           effectiveScope,
		RunInput:        []input.Entry{{Name: "_json", Value: string(inputJSON)}},
		ResolvedDefs:    h.cfg.ResolvedDefs,
	}
	if err := h.cfg.SubExecute(subCtx, root, graph, subCfg); err != nil {
		return nil, fmt.Errorf("task execution failed: %w", err)
	}

	// 11. Read canonical output.
	output, outputType, err := readCanonicalOutput(invocationDir)
	if err != nil {
		return nil, fmt.Errorf("read output: %w", err)
	}

	// 12. Output mapping.
	var result json.RawMessage
	switch outputType {
	case "json":
		result = output
	case "markdown":
		wrapped, err := json.Marshal(map[string]string{"content": string(output)})
		if err != nil {
			return nil, fmt.Errorf("wrap markdown output: %w", err)
		}
		result = wrapped
	default:
		return nil, fmt.Errorf("unknown output type: %q", outputType)
	}

	// 13. Optional output schema validation.
	if h.def.OutputSchema != nil {
		outSchema, err := compileJSONSchema(h.def.OutputSchema)
		if err != nil {
			return nil, fmt.Errorf("compile output schema: %w", err)
		}
		if err := validateJSON(outSchema, result); err != nil {
			return nil, fmt.Errorf("output validation: %w", err)
		}
	}

	// 14. Cache store.
	if h.def.ToolYAML.Cache == "auto" {
		key := h.outerCacheKey(inputJSON)
		_ = cacheStore(h.cacheDir, key, result)
	}

	return result, nil
}

func (h *TaskToolHandler) outerCacheKey(inputJSON json.RawMessage) string {
	hash := sha256.New()

	// Hash tool definition files.
	for _, name := range []string{"tool.yaml", "input.schema.json"} {
		data, err := os.ReadFile(filepath.Join(h.def.Dir, name))
		if err == nil {
			hash.Write(data)
		}
	}
	if h.def.OutputSchema != nil {
		hash.Write(h.def.OutputSchema)
	}

	// Hash referenced task tree content.
	hashDir(hash, h.def.ResolvedSource)

	// Hash canonical input.
	hash.Write(inputJSON)

	return fmt.Sprintf("%x", hash.Sum(nil))
}

// hashDir walks a directory and hashes all file contents for cache keying.
func hashDir(hash io.Writer, dir string) {
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		// Include relative path so renames invalidate.
		rel, _ := filepath.Rel(dir, path)
		hash.Write([]byte(rel))
		hash.Write(data)
		return nil
	})
}

// readCanonicalOutput reads the task tree's canonical output from the invocation dir.
// Returns (content, type, error) where type is "json" or "markdown".
func readCanonicalOutput(invocationDir string) (json.RawMessage, string, error) {
	outputDir := filepath.Join(invocationDir, "output")

	// Try JSON first.
	jsonPath := filepath.Join(outputDir, "result.json")
	if data, err := os.ReadFile(jsonPath); err == nil {
		if !json.Valid(data) {
			return nil, "", fmt.Errorf("result.json is not valid JSON")
		}
		return json.RawMessage(data), "json", nil
	}

	// Fall back to markdown.
	mdPath := filepath.Join(outputDir, "result.md")
	if data, err := os.ReadFile(mdPath); err == nil {
		return json.RawMessage(data), "markdown", nil
	}

	return nil, "", fmt.Errorf("no output found (expected output/result.json or output/result.md)")
}
