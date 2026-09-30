package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/boxsie/smith/internal/executor"
	"github.com/boxsie/smith/internal/input"
	"github.com/boxsie/smith/internal/lib"
	"github.com/boxsie/smith/internal/proposal"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/task"
	"github.com/boxsie/smith/internal/tools"
	"gopkg.in/yaml.v3"
)

// ErrInvalidTarget indicates the target path is not usable (existing file,
// permission denied, etc.). The CLI maps this to exit code 2.
type ErrInvalidTarget struct {
	Err error
}

func (e *ErrInvalidTarget) Error() string { return e.Err.Error() }
func (e *ErrInvalidTarget) Unwrap() error { return e.Err }

// PlanInput holds all inputs for the plan operation.
type PlanInput struct {
	TargetDir     string           // target project directory
	Goal          string           // user's goal string
	ModelOverride string           // "" = use planner default
	MaxCostUSD    float64          // 0 = no ceiling
	Factory       *runtime.Factory // nil = DefaultFactory
	PlannerPath   string           // "" = resolve from lib path
	TerminalStage string           // "" = read output from root; set = read from child stage
}

// PlanResult holds the outcome of a plan operation.
type PlanResult struct {
	Proposal    *proposal.Proposal
	ProposalDir string
}

// Plan executes the full planner lifecycle: resolve planner, build project
// summary, execute planner, read output, and assemble proposal.
func Plan(ctx context.Context, inp PlanInput) (*PlanResult, error) {
	factory := inp.Factory
	if factory == nil {
		factory = runtime.DefaultFactory()
	}

	// 1. Validate and resolve target directory.
	absTarget, err := filepath.Abs(inp.TargetDir)
	if err != nil {
		return nil, &ErrInvalidTarget{Err: fmt.Errorf("resolve target path: %w", err)}
	}
	// Check if the path exists as a non-directory (e.g. a file).
	if info, err := os.Stat(absTarget); err == nil && !info.IsDir() {
		return nil, &ErrInvalidTarget{Err: fmt.Errorf("target path is a file, not a directory: %s", absTarget)}
	}
	if err := os.MkdirAll(absTarget, 0o755); err != nil {
		return nil, &ErrInvalidTarget{Err: fmt.Errorf("create target directory: %w", err)}
	}

	// 2. Generate proposal ID early — needed for scope.
	proposalID := proposal.GenerateID()

	// 3. Create proposal directory — proposal.write needs it during execution.
	proposalDir := proposal.ProposalDir(absTarget, proposalID)
	if err := os.MkdirAll(proposalDir, 0o755); err != nil {
		return nil, fmt.Errorf("create proposal directory: %w", err)
	}

	// 4. Resolve planner.
	plannerPath := inp.PlannerPath
	if plannerPath == "" {
		resolved, err := lib.ResolveModule("planner", absTarget)
		if err != nil {
			return nil, fmt.Errorf("resolve planner: %w", err)
		}
		plannerPath = resolved
	}

	// 5. Compute planner content hash early — needed for cache dir and provenance.
	plannerHash, err := proposal.HashTaskTree(plannerPath)
	if err != nil {
		return nil, fmt.Errorf("hash planner tree: %w", err)
	}

	// 5b. Detect pipeline marker.
	if inp.TerminalStage == "" {
		ts, err := readPipelineMarker(plannerPath)
		if err != nil {
			return nil, fmt.Errorf("pipeline marker: %w", err)
		}
		if ts != "" {
			// Validate the terminal stage exists.
			tsTask := filepath.Join(plannerPath, ts, "task.md")
			if _, err := os.Stat(tsTask); err != nil {
				return nil, fmt.Errorf("pipeline.md declares terminal_stage %q but %s does not exist", ts, tsTask)
			}
			inp.TerminalStage = ts
		}
	}

	// 6. Build project summary.
	projectSummary, err := BuildProjectSummary(absTarget)
	if err != nil {
		return nil, fmt.Errorf("build project summary: %w", err)
	}

	// 6. Discover planner task tree.
	root, err := task.DiscoverTree(plannerPath)
	if err != nil {
		return nil, fmt.Errorf("discover planner tree: %w", err)
	}

	// 8. Redirect all task Paths so outputs don't go to the lib path.
	// Each task keeps its SourcePath pointing at the lib tree (for provenance/caching)
	// but writes outputs to a mirror structure under the work dir.
	//
	// For pipeline planners (TerminalStage set), use a stable project-local cache
	// dir under <target>/.smith/cache/planner/<hash>/ so cached stage outputs
	// survive across runs but stay isolated per project and planner version.
	// For legacy planners, use a temp dir (cleaned up after execution).
	hashDir := strings.TrimPrefix(plannerHash, "sha256:")
	var workDir string
	var cleanupWorkDir func()
	if inp.TerminalStage != "" {
		cacheDir, err := plannerCacheDir(absTarget, hashDir)
		if err != nil {
			return nil, fmt.Errorf("planner cache dir: %w", err)
		}
		workDir = cacheDir
		cleanupWorkDir = func() {} // keep cache across runs
	} else {
		td, err := os.MkdirTemp("", "smith-plan-*")
		if err != nil {
			return nil, fmt.Errorf("create temp dir: %w", err)
		}
		workDir = td
		cleanupWorkDir = func() { os.RemoveAll(td) }
	}
	defer cleanupWorkDir()
	task.RedirectPaths(root, plannerPath, workDir)

	// 8. Apply planner model routing and invocation overrides before agent inheritance.
	if _, err := configurePlannerModels(root, factory, inp.ModelOverride); err != nil {
		return nil, fmt.Errorf("configure planner models: %w", err)
	}
	if inp.MaxCostUSD > 0 {
		if root.Agent == nil {
			root.Agent = &task.AgentConfig{}
		}
		root.Agent.MaxCostUSD = &inp.MaxCostUSD
	}

	// 9. Resolve agent inheritance and build graph.
	if err := task.ResolveAgentInheritance(root); err != nil {
		return nil, fmt.Errorf("resolve planner agents: %w", err)
	}
	graph, err := task.BuildGraph(root)
	if err != nil {
		return nil, fmt.Errorf("build planner graph: %w", err)
	}

	// 10. Build run input.
	runInput := []input.Entry{
		{Name: "goal", Value: inp.Goal},
		{Name: "project_summary", Value: projectSummary.Text},
	}

	// 11. Build scope and tool registry (conditional on pipeline mode).
	scope := map[string]string{
		"root": absTarget,
	}
	registry := tools.NewRegistry()
	registry.Register("project.list", &tools.ProjectList{})
	registry.Register("project.read", &tools.ProjectRead{})
	registry.Register("project.find", &tools.ProjectFind{})

	noCache := true
	if inp.TerminalStage != "" {
		// Pipeline mode: stages are pure functions, no tools, caching enabled.
		noCache = false
	} else {
		// Legacy mode: proposal.write tool available, scoped to proposal.
		scope["proposal_id"] = proposalID
		registry.Register("proposal.write", &tools.ProposalWrite{})
	}

	// Discover app tools from the target project (not the planner module).
	// In pipeline mode, discovery errors are non-fatal — the planner can still
	// run with built-in tools only, and a broken tools/ directory shouldn't
	// prevent smith plan from generating a repair proposal. We preserve the
	// error so orchestrateToolCreation can reject create_tools when uniqueness
	// cannot be verified.
	// In legacy mode, discovery errors are hard failures because the planner
	// may depend on app tools being registered in the tool registry.
	var discoverErr error
	appTools, err := tools.DiscoverAndExtract(absTarget, tools.BuiltinToolIDs, tools.NativeToolIDs())
	if err != nil {
		if inp.TerminalStage == "" {
			return nil, fmt.Errorf("discover target project tools: %w", err)
		}
		discoverErr = err
		appTools = &tools.ResolvedTools{
			AppDefs: make(map[string]*tools.AppToolDef),
			Sources: make(map[string]string),
		}
	}
	resolvedDefs, err := tools.RegisterResolvedTools(registry, appTools, tools.RegisterResolvedToolsConfig{
		ProjectRoot: absTarget,
		Factory:     factory,
		Scope:       scope,
		SubExecute: func(ctx context.Context, root *task.Task, graph *task.Graph, subCfg tools.SubExecConfig) error {
			execCfg := executor.Config{
				Factory:      subCfg.Factory,
				Adapter:      subCfg.Adapter,
				NoCache:      subCfg.NoCache,
				RunInput:     subCfg.RunInput,
				Scope:        subCfg.Scope,
				ResolvedDefs: subCfg.ResolvedDefs,
			}
			_, err := executor.Execute(ctx, root, graph, execCfg)
			return err
		},
	})
	if err != nil {
		return nil, err
	}

	// 12. Execute planner.
	cfg := executor.Config{
		Factory:      factory,
		NoCache:      noCache,
		RunInput:     runInput,
		Adapter:      registry,
		Scope:        scope,
		ResolvedDefs: resolvedDefs,
	}

	result, err := executor.Execute(ctx, root, graph, cfg)
	if err != nil {
		return nil, fmt.Errorf("execute planner: %w", err)
	}
	if !result.Success {
		for _, tr := range result.Tasks {
			if tr.Err != nil {
				return nil, fmt.Errorf("planner failed: %w", tr.Err)
			}
		}
		return nil, fmt.Errorf("planner execution failed")
	}

	// 14. Read planner output (result.json from temp dir).
	var outputPath string
	if inp.TerminalStage != "" {
		outputPath = filepath.Join(workDir, inp.TerminalStage, "output", "result.json")
	} else {
		outputPath = filepath.Join(workDir, "output", "result.json")
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read planner output: %w", err)
	}
	var plannerOutput proposal.PlannerOutput
	if err := json.Unmarshal(data, &plannerOutput); err != nil {
		return nil, fmt.Errorf("parse planner output: %w", err)
	}

	// 14b. Post-pipeline tool orchestration: create tools from planner specs.
	if len(plannerOutput.CreateTools) > 0 {
		toolOps, err := orchestrateToolCreation(ctx, plannerOutput.CreateTools, appTools, discoverErr, factory, absTarget)
		if err != nil {
			return nil, fmt.Errorf("tool creation: %w", err)
		}
		plannerOutput.Operations = append(plannerOutput.Operations, toolOps...)
	}

	// 15. Validate ALL operation paths for traversal before any staging or replay.
	// op.Path comes from LLM output — reject escaping paths before they touch disk.
	if err := validateOperationPaths(absTarget, plannerOutput.Operations); err != nil {
		return nil, fmt.Errorf("validate operation paths: %w", err)
	}

	// 16. Stage content-carrying operations to proposal files/ directory.
	if err := stageContentFiles(proposalDir, plannerOutput.Operations); err != nil {
		return nil, fmt.Errorf("stage content files: %w", err)
	}

	// 17. Pre-Author validation gate for content-carrying operations.
	var preValidation *proposal.ValidationResult
	if hasContentOps(plannerOutput.Operations) {
		ops := buildOpsForValidation(plannerOutput)
		validation, err := proposal.PreValidate(absTarget, proposalDir, ops)
		if err != nil {
			return nil, fmt.Errorf("pre-validate: %w", err)
		}
		if validation.Status == "fail" {
			return nil, fmt.Errorf("pre-validation failed: %v", validation.Errors)
		}
		preValidation = validation
	}

	// 17. Compute provenance.
	observedState, err := proposal.HashProjectState(absTarget)
	if err != nil {
		return nil, fmt.Errorf("hash project state: %w", err)
	}

	// Extract metrics from the root task result.
	var tokensIn, tokensOut int
	var costUSD float64
	var durationMS int64
	for _, tr := range result.Tasks {
		tokensIn += tr.Metrics.TokensIn
		tokensOut += tr.Metrics.TokensOut
		costUSD += tr.Metrics.CostUSD
		durationMS += tr.Metrics.DurationMS
	}

	provenance := proposal.ProvenanceInput{
		Goal:            inp.Goal,
		Model:           root.EffectiveAgent.Model,
		Temperature:     *root.EffectiveAgent.Temperature,
		ResolvedFrom:    plannerPath,
		ContentHash:     plannerHash,
		ObservedState:   observedState,
		ModulesResolved: projectSummary.ModulesResolved,
		TokensIn:        tokensIn,
		TokensOut:       tokensOut,
		CostUSD:         costUSD,
		DurationMS:      durationMS,
	}

	// Build per-stage metrics for staged planners.
	if inp.TerminalStage != "" {
		for _, tr := range result.Tasks {
			provenance.StageInputs = append(provenance.StageInputs, proposal.StageInput{
				TaskID:     tr.TaskID,
				Model:      tr.Metrics.Model,
				Cached:     tr.Metrics.Cached,
				TokensIn:   tr.Metrics.TokensIn,
				TokensOut:  tr.Metrics.TokensOut,
				CostUSD:    tr.Metrics.CostUSD,
				DurationMS: tr.Metrics.DurationMS,
			})
		}
	}

	// 18. Assemble proposal.
	prop, err := proposal.Author(proposal.AuthorInput{
		ProposalDir:   proposalDir,
		PlannerOutput: plannerOutput,
		Provenance:    provenance,
		TargetDir:     absTarget,
		PreValidation: preValidation,
	})
	if err != nil {
		return nil, fmt.Errorf("assemble proposal: %w", err)
	}

	return &PlanResult{
		Proposal:    prop,
		ProposalDir: proposalDir,
	}, nil
}

// redirectPaths is now task.RedirectPaths — shared with task tool handler.

// plannerCacheDir returns a stable project-local directory for planner stage outputs.
// Located at <absTarget>/.smith/cache/planner/<hashDir>/ so cached outputs survive
// across runs but stay isolated per project and planner version.
func plannerCacheDir(absTarget, hashDir string) (string, error) {
	dir := filepath.Join(absTarget, ".smith", "cache", "planner", hashDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create planner cache dir: %w", err)
	}
	return dir, nil
}

// validateOperationPaths checks every operation path for traversal and .smith/
// targeting before any staging or replay occurs. This covers all operations
// (writes and deletes) regardless of whether they carry content.
func validateOperationPaths(targetDir string, ops []proposal.PlannerOperation) error {
	for _, op := range ops {
		if op.Path == "" {
			return fmt.Errorf("%s operation has empty path", op.Op)
		}
		if _, err := tools.SafeResolve(targetDir, op.Path); err != nil {
			return fmt.Errorf("%s path %q: %w", op.Op, op.Path, err)
		}
		if tools.HasSmithComponent(op.Path) {
			return fmt.Errorf("%s path %q targets .smith/ directory", op.Op, op.Path)
		}
	}
	return nil
}

// readPipelineMarker reads the pipeline.md sidecar from a planner directory.
// Returns the terminal stage path and nil error if present and valid.
// Returns "" and nil if the file does not exist (legacy planner).
// Returns an error if the file exists but is malformed or has empty terminal_stage.
func readPipelineMarker(plannerPath string) (string, error) {
	data, err := os.ReadFile(filepath.Join(plannerPath, "pipeline.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // No marker — legacy planner
		}
		return "", fmt.Errorf("read pipeline.md: %w", err)
	}
	var marker struct {
		TerminalStage string `yaml:"terminal_stage"`
	}
	if err := yaml.Unmarshal(data, &marker); err != nil {
		return "", fmt.Errorf("parse pipeline.md: %w", err)
	}
	if marker.TerminalStage == "" {
		return "", fmt.Errorf("pipeline.md exists but terminal_stage is empty")
	}
	return marker.TerminalStage, nil
}

// hasContentOps returns true if any operation carries non-empty Content.
func hasContentOps(ops []proposal.PlannerOperation) bool {
	for _, op := range ops {
		if op.Content != "" {
			return true
		}
	}
	return false
}

// buildOpsForValidation converts PlannerOperations to proposal.Operations
// for use with PreValidate. This mirrors the conversion in Author().
func buildOpsForValidation(output proposal.PlannerOutput) []proposal.Operation {
	var ops []proposal.Operation
	for _, pop := range output.Operations {
		op := proposal.Operation{
			Op:   pop.Op,
			Path: pop.Path,
		}
		if pop.Op == "write" {
			op.Source = filepath.ToSlash(filepath.Join("files", pop.Path))
		}
		ops = append(ops, op)
	}
	return ops
}

// stageContentFiles writes content-carrying write operations to the proposal
// files/ directory. Operations with empty Content are skipped (they rely on
// proposal.write tool staging). Delete operations with Content produce an error.
// All paths are validated for traversal before any writes occur.
func stageContentFiles(proposalDir string, ops []proposal.PlannerOperation) error {
	filesDir := proposal.FilesDir(proposalDir)

	// Ensure files/ directory exists for path validation.
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return fmt.Errorf("create files dir: %w", err)
	}

	// Validate all paths before writing anything. op.Path comes from LLM
	// output and must not escape the files/ directory.
	for _, op := range ops {
		if op.Content == "" || op.Op != "write" {
			continue
		}
		if _, err := tools.SafeResolve(filesDir, op.Path); err != nil {
			return fmt.Errorf("unsafe operation path %q: %w", op.Path, err)
		}
		if tools.HasSmithComponent(op.Path) {
			return fmt.Errorf("operation path %q targets .smith/ directory", op.Path)
		}
	}

	for _, op := range ops {
		if op.Content == "" {
			continue
		}
		if op.Op == "delete" {
			return fmt.Errorf("delete operation for %q must not carry content", op.Path)
		}
		if op.Op != "write" {
			continue
		}

		targetPath := filepath.Join(filesDir, op.Path)
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("create dirs for %q: %w", op.Path, err)
		}

		content := normalizeStagedMarkdownFrontmatter(op.Path, op.Content)

		// Atomic write: write to temp file, then rename.
		tmpFile, err := os.CreateTemp(filepath.Dir(targetPath), ".smith-stage-*")
		if err != nil {
			return fmt.Errorf("create temp file for %q: %w", op.Path, err)
		}
		if _, err := tmpFile.WriteString(content); err != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return fmt.Errorf("write content for %q: %w", op.Path, err)
		}
		tmpFile.Close()
		if err := os.Rename(tmpFile.Name(), targetPath); err != nil {
			os.Remove(tmpFile.Name())
			return fmt.Errorf("rename staged file for %q: %w", op.Path, err)
		}
	}
	return nil
}

// normalizeStagedMarkdownFrontmatter hardens planner-authored task.md/return.md
// content before validation. The planner already receives prompt guidance for
// YAML safety, but we still normalize the specific failure mode we have seen in
// live runs: unquoted constraint bullets that contain ": ".
func normalizeStagedMarkdownFrontmatter(path, content string) string {
	base := filepath.Base(path)
	if base != "task.md" && base != "return.md" {
		return content
	}

	frontmatter, body, ok := splitStagedFrontmatter(content)
	if !ok {
		return content
	}

	normalized, changed := quoteUnsafeConstraintBullets(frontmatter)
	if !changed {
		return content
	}

	return "---\n" + normalized + "\n---\n" + body
}

func splitStagedFrontmatter(content string) (frontmatter, body string, ok bool) {
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return "", "", false
	}

	openLen := 4
	if strings.HasPrefix(content, "---\r\n") {
		openLen = 5
	}

	rest := content[openLen:]
	idx := -1
	searchFrom := 0
	for {
		next := strings.Index(rest[searchFrom:], "---")
		if next == -1 {
			return "", "", false
		}
		idx = searchFrom + next
		if idx == 0 || rest[idx-1] == '\n' {
			break
		}
		searchFrom = idx + 3
	}

	frontmatter = strings.TrimRight(rest[:idx], "\r\n")
	if strings.TrimSpace(frontmatter) == "" {
		return "", "", false
	}

	body = rest[idx+3:]
	body = strings.TrimPrefix(body, "\r\n")
	body = strings.TrimPrefix(body, "\n")
	return frontmatter, body, true
}

func quoteUnsafeConstraintBullets(frontmatter string) (string, bool) {
	lines := strings.Split(frontmatter, "\n")
	inConstraints := false
	constraintsIndent := 0
	changed := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		indent := leadingSpaceCount(line)

		if inConstraints && trimmed != "" && !strings.HasPrefix(trimmed, "#") && indent <= constraintsIndent {
			inConstraints = false
		}

		if !inConstraints {
			if trimmed == "constraints:" {
				inConstraints = true
				constraintsIndent = indent
			}
			continue
		}

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		trimmedLeft := strings.TrimLeft(line, " ")
		if !strings.HasPrefix(trimmedLeft, "- ") {
			continue
		}

		item := strings.TrimPrefix(trimmedLeft, "- ")
		if !shouldQuoteConstraintBullet(item) {
			continue
		}

		prefixLen := len(line) - len(trimmedLeft)
		lines[i] = line[:prefixLen] + "- " + strconv.Quote(item)
		changed = true
	}

	return strings.Join(lines, "\n"), changed
}

func shouldQuoteConstraintBullet(item string) bool {
	if item == "" {
		return false
	}
	if strings.HasPrefix(item, "\"") || strings.HasPrefix(item, "'") {
		return false
	}
	return strings.Contains(item, ": ")
}

func leadingSpaceCount(s string) int {
	count := 0
	for count < len(s) && s[count] == ' ' {
		count++
	}
	return count
}

// orchestrateToolCreation validates tool specs against the discovered inventory,
// executes the tool-create module for each spec, validates path scoping, and
// returns the merged operations. If discoverErr is non-nil, the inventory is
// incomplete and uniqueness cannot be verified, so create_tools is rejected.
func orchestrateToolCreation(
	ctx context.Context,
	specs []proposal.ToolSpec,
	appTools *tools.ResolvedTools,
	discoverErr error,
	factory *runtime.Factory,
	projectRoot string,
) ([]proposal.PlannerOperation, error) {
	// If tool discovery failed, we can't verify that create_tools IDs don't
	// collide with existing app/lib tools. Reject rather than risk overwriting.
	if discoverErr != nil {
		return nil, fmt.Errorf("cannot create tools: tool discovery failed (uniqueness check unavailable): %w", discoverErr)
	}

	// Pre-merge ID validation.
	for _, spec := range specs {
		for _, builtinID := range tools.BuiltinToolIDs {
			if spec.ID == builtinID {
				return nil, fmt.Errorf("plan creates tool %q but it already exists as builtin", spec.ID)
			}
		}
		if appTools != nil {
			if _, exists := appTools.AppDefs[spec.ID]; exists {
				return nil, fmt.Errorf("plan creates tool %q but it already exists as %s", spec.ID, appTools.Sources[spec.ID])
			}
		}
	}

	// Resolve the tool-create module.
	toolCreatePath, err := lib.ResolveModule("tool-create", projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve tool-create module: %w", err)
	}

	var allOps []proposal.PlannerOperation
	for _, spec := range specs {
		ops, err := executeToolCreate(ctx, spec, toolCreatePath, factory, projectRoot)
		if err != nil {
			return nil, fmt.Errorf("create tool %q: %w", spec.ID, err)
		}
		allOps = append(allOps, ops...)
	}

	return allOps, nil
}

// executeToolCreate runs the tool-create module for a single tool spec and
// returns the resulting file operations.
func executeToolCreate(
	ctx context.Context,
	spec proposal.ToolSpec,
	toolCreatePath string,
	factory *runtime.Factory,
	projectRoot string,
) ([]proposal.PlannerOperation, error) {
	// Serialize spec as JSON for run input.
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("marshal tool spec: %w", err)
	}

	// Discover task tree.
	root, err := task.DiscoverTree(toolCreatePath)
	if err != nil {
		return nil, fmt.Errorf("discover tool-create tree: %w", err)
	}

	// Create invocation directory.
	invocationDir, err := os.MkdirTemp("", "smith-tool-create-*")
	if err != nil {
		return nil, fmt.Errorf("create invocation dir: %w", err)
	}
	defer os.RemoveAll(invocationDir)

	// Redirect paths to invocation dir.
	task.RedirectPaths(root, toolCreatePath, invocationDir)

	// Resolve agents and build graph.
	if err := task.ResolveAgentInheritance(root); err != nil {
		return nil, fmt.Errorf("resolve agents: %w", err)
	}
	graph, err := task.BuildGraph(root)
	if err != nil {
		return nil, fmt.Errorf("build graph: %w", err)
	}

	// Execute with _json run input.
	registry := tools.NewRegistry()
	cfg := executor.Config{
		Factory:  factory,
		RunInput: []input.Entry{{Name: "_json", Value: string(specJSON)}},
		Adapter:  registry,
		Scope:    map[string]string{"root": projectRoot},
	}

	result, err := executor.Execute(ctx, root, graph, cfg)
	if err != nil {
		return nil, fmt.Errorf("execute: %w", err)
	}
	if !result.Success {
		return nil, toolCreateExecutionError(result, root.ID)
	}

	// Read output — tool-create uses return.md so output is at root level.
	outputPath := filepath.Join(invocationDir, "output", "result.json")
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read output: %w", err)
	}

	var output struct {
		Operations []proposal.PlannerOperation `json:"operations"`
	}
	if err := json.Unmarshal(data, &output); err != nil {
		return nil, fmt.Errorf("parse output: %w", err)
	}

	// Validate path scoping: every operation must be under tools/<id>/.
	prefix := "tools/" + spec.ID + "/"
	for _, op := range output.Operations {
		if !filepath.IsLocal(op.Path) {
			return nil, fmt.Errorf("operation path %q is not local", op.Path)
		}
		if len(op.Path) < len(prefix) || op.Path[:len(prefix)] != prefix {
			return nil, fmt.Errorf("operation path %q is outside scope %q", op.Path, prefix)
		}
	}

	return output.Operations, nil
}

func toolCreateExecutionError(result *executor.Result, rootTaskID string) error {
	if result == nil {
		return fmt.Errorf("tool-create execution failed")
	}

	var specific []string
	var fallback []string
	for _, tr := range result.Tasks {
		if tr.Err == nil {
			continue
		}
		msg := tr.Err.Error()
		if tr.TaskID != "" {
			msg = fmt.Sprintf("%s: %s", tr.TaskID, msg)
		}
		if tr.TaskID != rootTaskID && !strings.Contains(tr.Err.Error(), "child dependency failed") {
			specific = append(specific, msg)
			continue
		}
		fallback = append(fallback, msg)
	}

	if len(specific) > 0 {
		return fmt.Errorf("failed tasks: %s", strings.Join(specific, "; "))
	}
	if len(fallback) > 0 {
		return fmt.Errorf("failed tasks: %s", strings.Join(fallback, "; "))
	}
	return fmt.Errorf("tool-create execution failed")
}
