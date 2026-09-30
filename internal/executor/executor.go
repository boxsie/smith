package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/boxsie/smith/internal/input"
	"github.com/boxsie/smith/internal/output"
	"github.com/boxsie/smith/internal/prompt"
	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/task"
	"github.com/boxsie/smith/internal/tools"
)

// Config holds executor dependencies.
type Config struct {
	Factory                     *runtime.Factory
	ExternalFactory             *runtime.ExternalFactory
	Adapter                     tools.Adapter                      // nil if no MCP tools configured
	NoCache                     bool                               // skip cache checks for all tasks (--no-cache flag)
	RunInput                    []input.Entry                      // run input entries for the root task
	Scope                       map[string]string                  // tool scope parameters (--scope flags)
	RuntimeContext              prompt.RuntimeContext              // built-in calendar/time context for LLM prompts and cache keys
	ResolvedDefs                map[string]runtime.ToolDef         // rich tool definitions (nil → bare ID fallback for legacy callers)
	HistoryLogger               *tools.HistoryLogger               // optional; logs every tool invocation when non-nil
	RunID                       string                             // run ID for this execution (empty for sub-executions and tests)
	RunDir                      string                             // path to run directory (empty for sub-executions and tests)
	CacheRoot                   string                             // shared task cache root (empty disables shared cache)
	Events                      run.EventAppender                  // durable run history; nil preserves legacy manifest writes
	NewInvocationID             func() string                      // run-local stable invocation ID allocator
	ParentInvocationID          string                             // causal parent for nested task-tool execution
	ProjectManifest             bool                               // project invocation events into the top-level manifest
	AppRoot                     string                             // authored app root; owns Smith session state
	WritableRoots               []string                           // conductor-granted roots for external work profiles
	ExternalProfiles            map[string]runtime.ResolvedProfile // resolved before external work starts
	AllowUncontainedDevelopment bool                               // deliberate authority for the named uncontained development profile
	AttemptClock                runtime.AttemptClock               // injectable deterministic clock for retry/deadline control

	// phaseMetrics stores task-phase PhaseMetrics by TaskID for two-phase tasks.
	// Used internally during Execute to pass metrics from task-phase to return-phase.
	phaseMetrics *sync.Map
}

// TaskResult records the outcome of a single task.
type TaskResult struct {
	TaskID        string
	Status        string // "success", "failed", "cached", "skipped"
	Metrics       output.Metrics
	RuntimeResult *runtime.ExternalResult
	Err           error
}

// Result holds the outcome of a full execution run.
type Result struct {
	Tasks   []TaskResult
	Success bool
}

func taskMetrics(result TaskResult) run.TaskMetrics {
	metrics := run.TaskMetrics{
		DurationMS:     result.Metrics.DurationMS,
		Model:          result.Metrics.Model,
		RequestedModel: result.Metrics.RequestedModel,
		Runtime:        result.Metrics.Runtime,
		SessionID:      result.Metrics.SessionID,
		BillingBasis:   result.Metrics.BillingBasis,
		TokensIn:       result.Metrics.TokensIn,
		TokensOut:      result.Metrics.TokensOut,
		CostUSD:        result.Metrics.CostUSD,
		Cached:         result.Metrics.Cached,
	}
	if result.Metrics.TaskPhase != nil {
		if result.Metrics.TaskPhase.Cached {
			metrics.TaskPhaseStatus = "cached"
		} else {
			metrics.TaskPhaseStatus = "success"
		}
	}
	if result.Metrics.ReturnPhase != nil {
		if result.Metrics.ReturnPhase.Cached {
			metrics.ReturnPhaseStatus = "cached"
		} else {
			metrics.ReturnPhaseStatus = "success"
		}
	}
	return metrics
}

func (cfg *Config) eventObserver(invocationID, taskID, phase string) func(run.Event) error {
	if cfg.Events == nil {
		return nil
	}
	return func(event run.Event) error {
		event.RunID = cfg.RunID
		event.TaskID = taskID
		event.Phase = phase
		if event.InvocationID == "" {
			event.InvocationID = invocationID
			event.ParentInvocationID = cfg.ParentInvocationID
		} else if event.ParentInvocationID == "" {
			event.ParentInvocationID = invocationID
		}
		_, err := cfg.Events.Append(event)
		return err
	}
}

func recordArtifact(cfg *Config, invocationID, taskID, phase, path string) error {
	observer := cfg.eventObserver(invocationID, taskID, phase)
	if observer == nil {
		return nil
	}
	digest, err := fileSHA256(path)
	if err != nil {
		return fmt.Errorf("hash published artifact: %w", err)
	}
	return observer(run.Event{Type: run.EventArtifactPublished, Artifact: path, ArtifactSHA256: digest})
}

func canonicalArtifact(taskPath, outputType string) string {
	name := "result.md"
	if outputType == "json" {
		name = "result.json"
	}
	return filepath.Join(taskPath, "output", name)
}

// resolveToolDefs builds ToolDef slices and an allowed-set from a task's Tools list.
// When cfg.ResolvedDefs is non-nil, every tool ID must be present (validation bug otherwise).
// When nil, falls back to bare ToolDef{ID: tid} for legacy callers.
func resolveToolDefs(taskTools []string, resolved map[string]runtime.ToolDef) ([]runtime.ToolDef, map[string]bool, error) {
	var defs []runtime.ToolDef
	allowed := make(map[string]bool)
	for _, tid := range taskTools {
		if resolved == nil {
			defs = append(defs, runtime.ToolDef{ID: tid})
		} else if def, ok := resolved[tid]; ok {
			defs = append(defs, def)
		} else {
			return nil, nil, fmt.Errorf("tool %q not found in resolved definitions (validation bug)", tid)
		}
		allowed[tid] = true
	}
	return defs, allowed, nil
}

// Execute runs all tasks in topological order per the graph's execution plan.
// Tasks within a level run concurrently; levels run sequentially.
func Execute(ctx context.Context, root *task.Task, graph *task.Graph, cfg Config) (*Result, error) {
	if cfg.Factory == nil {
		cfg.Factory = runtime.DefaultFactory()
	}
	if cfg.ExternalFactory == nil {
		cfg.ExternalFactory = runtime.DefaultExternalFactory()
	}
	if cfg.RuntimeContext.IsZero() {
		cfg.RuntimeContext = prompt.NewRuntimeContext(time.Now())
	}
	if cfg.AttemptClock == nil {
		cfg.AttemptClock = runtime.RealAttemptClock{}
	}
	if cfg.AppRoot == "" {
		cfg.AppRoot = cfg.Scope[tools.ScopeRoot]
	}
	if cfg.ExternalProfiles == nil {
		workspaceRoot := cfg.Scope[tools.ScopeRoot]
		if workspaceRoot == "" {
			workspaceRoot = root.Path
		}
		profiles, err := ResolveExternalProfiles(root, workspaceRoot, cfg.WritableRoots, true)
		if err != nil {
			return nil, fmt.Errorf("preflight: %w", err)
		}
		cfg.ExternalProfiles = profiles
	}
	cfg.phaseMetrics = &sync.Map{}

	invocationIDs := make(map[string]string)
	if cfg.Events != nil {
		var localOrdinal uint64
		for _, level := range graph.Plan {
			for _, node := range level {
				localOrdinal++
				invocationID := run.InvocationID(cfg.RunID, localOrdinal)
				if cfg.NewInvocationID != nil {
					invocationID = cfg.NewInvocationID()
				}
				invocationIDs[node.NodeID] = invocationID
				if _, err := cfg.Events.Append(run.Event{
					Type:               run.EventInvocationQueued,
					RunID:              cfg.RunID,
					InvocationID:       invocationID,
					ParentInvocationID: cfg.ParentInvocationID,
					TaskID:             node.Task.ID,
					Phase:              node.Phase,
					TaskHasReturn:      node.Task.HasReturn,
					ManifestTask:       cfg.ProjectManifest,
				}); err != nil {
					return nil, fmt.Errorf("record queued invocation: %w", err)
				}
			}
		}
	}

	// Index tasks for fast lookup.
	index := make(map[string]*task.Task)
	task.WalkTree(root, func(t *task.Task) {
		index[t.ID] = t
	})

	// Preflight: verify all models can be resolved before executing anything.
	// This prevents partial runs where early tasks succeed before a bad model
	// is discovered later in the graph.
	models := make(map[string]bool)
	runtimes := make(map[string]bool)
	for _, t := range index {
		if t.EffectiveAgent.Model == "shell" {
			continue
		}
		if t.EffectiveAgent.Runtime == "" || t.EffectiveAgent.Runtime == runtime.ProviderRuntime {
			models[t.EffectiveAgent.Model] = true
		} else {
			runtimes[t.EffectiveAgent.Runtime] = true
		}
	}
	for runtimeName := range runtimes {
		if _, err := cfg.ExternalFactory.Resolve(runtimeName); err != nil {
			return nil, fmt.Errorf("preflight: %w", err)
		}
	}
	for model := range models {
		if model == "shell" {
			continue
		}
		if _, err := cfg.Factory.Resolve(model); err != nil {
			return nil, fmt.Errorf("preflight: %w", err)
		}
	}

	// Build set of two-phase task IDs for manifest updates.
	twoPhaseIDs := make(map[string]bool)
	for _, t := range index {
		if t.HasReturn {
			twoPhaseIDs[t.ID] = true
		}
	}

	failed := make(map[string]bool)
	var allResults []TaskResult

	// Track result index by TaskID for two-phase result aggregation.
	resultIndex := make(map[string]int)

	for _, level := range graph.Plan {
		if cfg.Events != nil {
			for _, node := range level {
				if nodeBlocked(node, graph, failed) {
					continue
				}
				if _, err := cfg.Events.Append(run.Event{
					Type:               run.EventInvocationStarted,
					RunID:              cfg.RunID,
					InvocationID:       invocationIDs[node.NodeID],
					ParentInvocationID: cfg.ParentInvocationID,
					TaskID:             node.Task.ID,
					Phase:              node.Phase,
					TaskHasReturn:      node.Task.HasReturn,
					ManifestTask:       cfg.ProjectManifest,
				}); err != nil {
					return nil, fmt.Errorf("record started invocation: %w", err)
				}
			}
		}
		// Mark tasks in this level as "running" in the manifest before execution starts.
		if cfg.RunDir != "" && cfg.Events == nil {
			manifestPath := run.ManifestPath(cfg.RunDir)
			for _, node := range level {
				// Only mark task-phase nodes (or single-phase nodes) as running;
				// return-phase nodes are part of the same task lifecycle.
				if node.Phase != "return" {
					_ = run.UpdateTaskStatus(manifestPath, node.Task.ID, "running", run.TaskMetrics{})
				}
			}
		}

		var levelResults []nodeResult

		if len(level) == 1 {
			// Single node — no goroutine overhead.
			node := level[0]
			tr := executeOrSkip(ctx, node, graph, &cfg, failed, invocationIDs[node.NodeID])
			levelResults = append(levelResults, tr)
		} else {
			// Parallel execution within level.
			results := make([]nodeResult, len(level))
			var wg sync.WaitGroup
			for i, node := range level {
				wg.Add(1)
				go func(idx int, node *task.GraphNode) {
					defer wg.Done()
					results[idx] = executeOrSkip(ctx, node, graph, &cfg, failed, invocationIDs[node.NodeID])
				}(i, node)
			}
			wg.Wait()
			levelResults = results
		}

		// Collect failures and skips for dependent skipping.
		// Skipped tasks must propagate: if A fails and B is skipped,
		// C (which depends on B) must also be skipped.
		for _, nr := range levelResults {
			if nr.Result.Status == "failed" || nr.Result.Status == "skipped" {
				failed[nr.NodeID] = true
			}
			// Result aggregation: return-phase result replaces task-phase result,
			// but a skipped return-phase must NOT overwrite a failed/successful
			// task-phase — the earlier result is more informative.
			if idx, ok := resultIndex[nr.Result.TaskID]; ok {
				if nr.Result.Status != "skipped" {
					allResults[idx] = nr.Result
				} else if allResults[idx].Status == "success" || allResults[idx].Status == "cached" {
					// Task-phase succeeded but return was skipped (child failure).
					// Mark the task as failed since it didn't complete.
					allResults[idx] = TaskResult{
						TaskID: nr.Result.TaskID,
						Status: "failed",
						Err:    fmt.Errorf("return phase skipped: child dependency failed"),
					}
				}
				// If previous was already "failed", keep it as-is.
			} else {
				resultIndex[nr.Result.TaskID] = len(allResults)
				allResults = append(allResults, nr.Result)
			}

			// Update manifest with per-task status (best-effort).
			metrics := taskMetrics(nr.Result)
			isReturnPhase := strings.HasSuffix(nr.NodeID, ":return")
			isTaskPhaseOfTwoPhase := !isReturnPhase && twoPhaseIDs[nr.Result.TaskID]
			if cfg.Events != nil {
				eventType := run.EventInvocationCompleted
				switch nr.Result.Status {
				case "failed":
					eventType = run.EventInvocationFailed
				case "skipped":
					eventType = run.EventInvocationSkipped
				}
				event := run.Event{
					Type:               eventType,
					RunID:              cfg.RunID,
					InvocationID:       nr.InvocationID,
					ParentInvocationID: cfg.ParentInvocationID,
					TaskID:             nr.Result.TaskID,
					Phase:              nr.Phase,
					TaskFinal:          !isTaskPhaseOfTwoPhase || nr.Result.Status == "failed" || nr.Result.Status == "skipped",
					TaskHasReturn:      twoPhaseIDs[nr.Result.TaskID],
					ManifestTask:       cfg.ProjectManifest,
					Metrics:            &metrics,
				}
				if nr.Result.Err != nil {
					event.Error = nr.Result.Err.Error()
				}
				if _, err := cfg.Events.Append(event); err != nil {
					return nil, fmt.Errorf("record completed invocation: %w", err)
				}
			}

			if cfg.RunDir != "" && cfg.Events == nil {
				manifestPath := run.ManifestPath(cfg.RunDir)
				tr := nr.Result
				// Phase attribution from metrics (successful two-phase tasks).
				if tr.Metrics.TaskPhase != nil {
					if tr.Metrics.TaskPhase.Cached {
						metrics.TaskPhaseStatus = "cached"
					} else {
						metrics.TaskPhaseStatus = "success"
					}
				}
				if tr.Metrics.ReturnPhase != nil {
					if tr.Metrics.ReturnPhase.Cached {
						metrics.ReturnPhaseStatus = "cached"
					} else {
						metrics.ReturnPhaseStatus = "success"
					}
				}
				// Phase attribution for failures: infer from graph node phase.
				if tr.Status == "failed" {
					if isReturnPhase {
						metrics.ReturnPhaseStatus = "failed"
					} else if isTaskPhaseOfTwoPhase {
						metrics.TaskPhaseStatus = "failed"
					}
				}
				if tr.Status == "skipped" && isReturnPhase {
					metrics.ReturnPhaseStatus = "skipped"
				}

				// For task-phase results of two-phase tasks, keep overall status as
				// "running" — the task isn't done until the return phase completes.
				manifestStatus := tr.Status
				if isTaskPhaseOfTwoPhase && (tr.Status == "success" || tr.Status == "cached") {
					manifestStatus = "running"
				}
				_ = run.UpdateTaskStatus(manifestPath, tr.TaskID, manifestStatus, metrics)
			}
		}
	}

	// Finalize manifest (best-effort).
	success := true
	for _, tr := range allResults {
		if tr.Status == "failed" {
			success = false
			break
		}
	}

	if cfg.RunDir != "" && cfg.Events == nil {
		_ = run.FinalizeRun(run.ManifestPath(cfg.RunDir), success)
	}

	return &Result{Tasks: allResults, Success: success}, nil
}

// taskPhaseInfo stores metrics and timing from the task-phase for use by the return-phase.
type taskPhaseInfo struct {
	StartedAt time.Time
	Metrics   output.PhaseMetrics
}

// nodeResult pairs a TaskResult with the graph node ID that produced it.
type nodeResult struct {
	NodeID       string
	Phase        string
	InvocationID string
	Result       TaskResult
}

// executeOrSkip checks if a node should be skipped (due to a failed dependency)
// and either skips or executes it.
func executeOrSkip(ctx context.Context, node *task.GraphNode, graph *task.Graph, cfg *Config, failed map[string]bool, invocationID string) nodeResult {
	// Check if any node-level dependency failed.
	if nodeBlocked(node, graph, failed) {
		// If a return-phase is being skipped, clean up the .running marker
		// that the task-phase left behind. Otherwise the task appears
		// permanently "running" on disk.
		if node.Phase == "return" {
			_ = output.RemoveRunning(node.Task.Path)
		}
		return nodeResult{
			NodeID:       node.NodeID,
			Phase:        node.Phase,
			InvocationID: invocationID,
			Result: TaskResult{
				TaskID: node.Task.ID,
				Status: "skipped",
			},
		}
	}

	// Route based on phase.
	var tr TaskResult
	switch {
	case node.Phase == "return":
		tr = executeReturnPhase(ctx, node.Task, graph, cfg, invocationID)
	case node.Task.HasReturn:
		tr = executeTaskPhase(ctx, node.Task, graph, cfg, invocationID)
	default:
		tr = executeTask(ctx, node.Task, graph, cfg, invocationID)
	}
	return nodeResult{NodeID: node.NodeID, Phase: node.Phase, InvocationID: invocationID, Result: tr}
}

func nodeBlocked(node *task.GraphNode, graph *task.Graph, failed map[string]bool) bool {
	for _, dep := range graph.NodeDeps[node.NodeID] {
		if failed[dep.NodeID] {
			return true
		}
	}
	return false
}

// executeTask runs a single task through the full lifecycle.
func executeTask(ctx context.Context, t *task.Task, graph *task.Graph, cfg *Config, invocationID string) TaskResult {
	startedAt := time.Now()
	outputType := t.Frontmatter.OutputType()

	// Step 1: Clean stale .running marker from previous crashed runs.
	_ = output.RemoveRunning(t.Path)

	// Step 2: Build cache input and compute key.
	cacheInput, err := buildCacheInput(t, graph, cfg.RunInput, cfg.RuntimeContext)
	if err != nil {
		return failResult(t.Path, t.ID, startedAt, err)
	}
	cacheKey := ComputeCacheKey(cacheInput)

	// Step 3: Check cache (skip if cache: never or global NoCache).
	if t.Frontmatter.CachePolicy() != "never" && !cfg.NoCache {
		if cfg.CacheRoot != "" && CheckSharedCache(cfg.CacheRoot, cacheKey) == CacheHit {
			if err := HydrateFromCache(t.Path, cfg.CacheRoot, cacheKey, outputType, t, startedAt); err == nil {
				if err := recordArtifact(cfg, invocationID, t.ID, "task", canonicalArtifact(t.Path, outputType)); err != nil {
					return failResult(t.Path, t.ID, startedAt, err)
				}
				return cachedResult(t, startedAt)
			}
		} else if CheckCache(t.Path, cacheKey, outputType) == CacheHit {
			if err := recordArtifact(cfg, invocationID, t.ID, "task", canonicalArtifact(t.Path, outputType)); err != nil {
				return failResult(t.Path, t.ID, startedAt, err)
			}
			return cachedResult(t, startedAt)
		}
	}

	// Step 4: Write .running marker.
	marker := output.RunningMarker{
		TaskID:    t.ID,
		StartedAt: startedAt.UTC().Format(time.RFC3339),
	}
	if err := output.WriteRunning(t.Path, marker); err != nil {
		return failResult(t.Path, t.ID, startedAt, fmt.Errorf("write .running: %w", err))
	}

	// Step 5: Execute — shell or LLM path.
	var responseContent string
	var tokensIn, tokensOut int
	var costUSD float64
	model := t.EffectiveAgent.Model
	var requestedModel, runtimeName string
	var sessionID, billingBasis string
	var runtimeResult *runtime.ExternalResult
	var runtimeRecord *runtime.Record

	if t.EffectiveAgent.Model == "shell" {
		// Shell path: execute task body as /bin/sh -e script.
		stdout, _, err := runShellScript(ctx, t, graph, cfg)
		if err != nil {
			// Write result.md with whatever stdout was captured before failure,
			// so diagnostics aren't lost (parallel to WriteJSONOutput always
			// writing result.md even when extraction/validation fails).
			if stdout != "" {
				_ = output.WriteResultMD(t.Path, stdout)
			}
			return failResult(t.Path, t.ID, startedAt, err)
		}
		responseContent = stdout
	} else {
		// Model path: either Smith's Provider loop or a complete external agent.
		assemblyInput, err := buildAssemblyInput(t, graph, cfg.RunInput, cfg.RuntimeContext)
		if err != nil {
			return failResult(t.Path, t.ID, startedAt, fmt.Errorf("assemble prompt: %w", err))
		}
		promptText, err := prompt.AssemblePrompt(assemblyInput)
		if err != nil {
			return failResult(t.Path, t.ID, startedAt, fmt.Errorf("assemble prompt: %w", err))
		}

		var schema json.RawMessage
		if t.Schema != nil {
			schema = t.Schema.Raw
		}
		execution, err := executeModel(ctx, t, cfg, promptText, "task", outputType, schema, invocationID)
		if err != nil {
			return failResult(t.Path, t.ID, startedAt, err)
		}
		responseContent = execution.content
		model = execution.model
		requestedModel = execution.requestedModel
		runtimeName = execution.runtime
		sessionID = execution.sessionID
		billingBasis = execution.billingBasis
		tokensIn = execution.tokensIn
		tokensOut = execution.tokensOut
		costUSD = execution.costUSD
		runtimeResult = execution.runtimeResult
		runtimeRecord = execution.runtimeRecord
	}

	// Step 6: Write outputs.
	if outputType == "json" {
		if t.Schema == nil {
			return failResult(t.Path, t.ID, startedAt, fmt.Errorf("json task missing schema"))
		}
		if err := output.WriteJSONOutput(t.Path, responseContent, t.Schema.Raw); err != nil {
			return failResult(t.Path, t.ID, startedAt, err)
		}
	} else {
		if err := output.WriteResultMD(t.Path, responseContent); err != nil {
			return failResult(t.Path, t.ID, startedAt, err)
		}
	}
	if err := recordArtifact(cfg, invocationID, t.ID, "task", canonicalArtifact(t.Path, outputType)); err != nil {
		return failResult(t.Path, t.ID, startedAt, err)
	}

	// Step 7: Finalize success (.running removal is the commit point).
	completedAt := time.Now()
	if err := output.WriteHash(t.Path, cacheKey); err != nil {
		return failResult(t.Path, t.ID, startedAt, fmt.Errorf("write .hash: %w", err))
	}

	// Store to shared cache (best-effort).
	_ = StoreToCache(t.Path, cfg.CacheRoot, cacheKey, outputType)

	metrics := output.Metrics{
		Task:           t.ID,
		Status:         "success",
		Cached:         false,
		Model:          model,
		RequestedModel: requestedModel,
		Runtime:        runtimeName,
		SessionID:      sessionID,
		BillingBasis:   billingBasis,
		StartedAt:      startedAt.UTC().Format(time.RFC3339),
		CompletedAt:    completedAt.UTC().Format(time.RFC3339),
		DurationMS:     completedAt.Sub(startedAt).Milliseconds(),
		TokensIn:       tokensIn,
		TokensOut:      tokensOut,
		CostUSD:        costUSD,
		RuntimeRecords: runtimeRecords(runtimeRecord),
	}
	if err := output.WriteMetrics(t.Path, metrics); err != nil {
		return failResult(t.Path, t.ID, startedAt, fmt.Errorf("write .metrics.json: %w", err))
	}

	if err := output.RemoveRunning(t.Path); err != nil {
		return failResult(t.Path, t.ID, startedAt, fmt.Errorf("remove .running: %w", err))
	}

	return TaskResult{
		TaskID:        t.ID,
		Status:        "success",
		Metrics:       metrics,
		RuntimeResult: runtimeResult,
	}
}

func failResult(taskPath, taskID string, startedAt time.Time, err error) TaskResult {
	completedAt := time.Now()
	m := output.Metrics{
		Task:        taskID,
		Status:      "failed",
		StartedAt:   startedAt.UTC().Format(time.RFC3339),
		CompletedAt: completedAt.UTC().Format(time.RFC3339),
		DurationMS:  completedAt.Sub(startedAt).Milliseconds(),
	}
	// Best-effort metrics write for failed tasks (RFC: "SHOULD be written for every task attempt").
	_ = output.WriteMetrics(taskPath, m)

	return TaskResult{
		TaskID:  taskID,
		Status:  "failed",
		Err:     err,
		Metrics: m,
	}
}

func cachedResult(t *task.Task, startedAt time.Time) TaskResult {
	completedAt := time.Now()
	var requestedModel, runtimeName string
	if t.EffectiveAgent.Runtime != "" && t.EffectiveAgent.Runtime != runtime.ProviderRuntime {
		requestedModel = t.EffectiveAgent.Model
		runtimeName = t.EffectiveAgent.Runtime
	}
	metrics := output.Metrics{
		Task:           t.ID,
		Status:         "success",
		Cached:         true,
		Model:          t.EffectiveAgent.Model,
		RequestedModel: requestedModel,
		Runtime:        runtimeName,
		StartedAt:      startedAt.UTC().Format(time.RFC3339),
		CompletedAt:    completedAt.UTC().Format(time.RFC3339),
		DurationMS:     completedAt.Sub(startedAt).Milliseconds(),
	}
	// Best-effort metrics write for cache hits.
	_ = output.WriteMetrics(t.Path, metrics)

	return TaskResult{
		TaskID:  t.ID,
		Status:  "cached",
		Metrics: metrics,
	}
}

// executeTaskPhase runs the task-phase of a two-phase task.
// Writes output to output/task/result.md. Does NOT remove .running — that
// spans the entire lifecycle and is removed by the return phase.
func executeTaskPhase(ctx context.Context, t *task.Task, graph *task.Graph, cfg *Config, invocationID string) TaskResult {
	startedAt := time.Now()

	// Step 1: Clean stale .running marker.
	_ = output.RemoveRunning(t.Path)

	// Step 2: Build task-phase cache input (schema excluded for two-phase tasks).
	cacheInput, err := buildCacheInput(t, graph, cfg.RunInput, cfg.RuntimeContext)
	if err != nil {
		return failResult(t.Path, t.ID, startedAt, err)
	}
	cacheKey := ComputeCacheKey(cacheInput)

	// Step 3: Check task-phase cache.
	if t.Frontmatter.CachePolicy() != "never" && !cfg.NoCache {
		taskPhaseHit := false
		if cfg.CacheRoot != "" && CheckSharedTaskPhaseCache(cfg.CacheRoot, cacheKey) == CacheHit {
			if err := HydrateTaskPhaseFromCache(t.Path, cfg.CacheRoot, cacheKey); err == nil {
				taskPhaseHit = true
			}
		} else if CheckTaskPhaseCache(t.Path, cacheKey) == CacheHit {
			taskPhaseHit = true
		}
		if taskPhaseHit {
			// Task-phase cached — output/task/result.md exists (hydrated or pre-existing).
			cfg.phaseMetrics.Store(t.ID, &taskPhaseInfo{
				StartedAt: startedAt,
				Metrics: output.PhaseMetrics{
					Cached:     true,
					DurationMS: time.Since(startedAt).Milliseconds(),
				},
			})
			if err := recordArtifact(cfg, invocationID, t.ID, "task", filepath.Join(t.Path, "output", "task", "result.md")); err != nil {
				return failResult(t.Path, t.ID, startedAt, err)
			}
			return TaskResult{
				TaskID: t.ID,
				Status: "cached",
			}
		}
	}

	// Step 4: Write .running marker (spans entire lifecycle).
	marker := output.RunningMarker{
		TaskID:    t.ID,
		StartedAt: startedAt.UTC().Format(time.RFC3339),
	}
	if err := output.WriteRunning(t.Path, marker); err != nil {
		return failResult(t.Path, t.ID, startedAt, fmt.Errorf("write .running: %w", err))
	}

	// Step 5: Execute through either the Provider loop or an external runtime.
	assemblyInput, err := buildAssemblyInput(t, graph, cfg.RunInput, cfg.RuntimeContext)
	if err != nil {
		return failResult(t.Path, t.ID, startedAt, fmt.Errorf("assemble prompt: %w", err))
	}
	promptText, err := prompt.AssemblePrompt(assemblyInput)
	if err != nil {
		return failResult(t.Path, t.ID, startedAt, fmt.Errorf("assemble prompt: %w", err))
	}

	execution, err := executeModel(ctx, t, cfg, promptText, "task", "markdown", nil, invocationID)
	if err != nil {
		return failResult(t.Path, t.ID, startedAt, err)
	}

	// Step 6: Write task-phase output (always markdown).
	if err := output.WriteTaskPhaseResult(t.Path, execution.content); err != nil {
		return failResult(t.Path, t.ID, startedAt, err)
	}
	if err := recordArtifact(cfg, invocationID, t.ID, "task", filepath.Join(t.Path, "output", "task", "result.md")); err != nil {
		return failResult(t.Path, t.ID, startedAt, err)
	}

	// Step 7: Write task-phase hash.
	if err := output.WriteTaskPhaseHash(t.Path, cacheKey); err != nil {
		return failResult(t.Path, t.ID, startedAt, fmt.Errorf("write task-phase .hash: %w", err))
	}

	// Store task-phase to shared cache (best-effort).
	_ = StoreTaskPhaseToCache(t.Path, cfg.CacheRoot, cacheKey)

	// Store task-phase metrics for the return phase.
	completedAt := time.Now()
	cfg.phaseMetrics.Store(t.ID, &taskPhaseInfo{
		StartedAt: startedAt,
		Metrics: output.PhaseMetrics{
			Cached:        false,
			Runtime:       execution.runtime,
			SessionID:     execution.sessionID,
			BillingBasis:  execution.billingBasis,
			DurationMS:    completedAt.Sub(startedAt).Milliseconds(),
			TokensIn:      execution.tokensIn,
			TokensOut:     execution.tokensOut,
			CostUSD:       execution.costUSD,
			RuntimeRecord: execution.runtimeRecord,
		},
	})

	// Do NOT remove .running or write canonical output — children run next,
	// then the return phase completes the task.
	return TaskResult{
		TaskID:        t.ID,
		Status:        "success",
		RuntimeResult: execution.runtimeResult,
	}
}

// executeReturnPhase runs the return-phase of a two-phase task.
// Reads task-phase output and children's canonical outputs, writes canonical output.
func executeReturnPhase(ctx context.Context, t *task.Task, graph *task.Graph, cfg *Config, invocationID string) TaskResult {
	startedAt := time.Now()
	outputType := t.Frontmatter.OutputType()

	// Step 1: Build return-phase cache input.
	rci, err := buildReturnCacheInput(t, graph, cfg.RunInput, cfg.RuntimeContext)
	if err != nil {
		return failResult(t.Path, t.ID, startedAt, err)
	}
	returnCacheKey := ComputeReturnCacheKey(rci)

	// Step 2: Check return-phase cache.
	if t.Frontmatter.CachePolicy() != "never" && !cfg.NoCache {
		returnHit := false
		if cfg.CacheRoot != "" && CheckSharedCache(cfg.CacheRoot, returnCacheKey) == CacheHit {
			if err := HydrateFromCache(t.Path, cfg.CacheRoot, returnCacheKey, outputType, t, startedAt); err == nil {
				returnHit = true
			}
		} else if CheckCache(t.Path, returnCacheKey, outputType) == CacheHit {
			returnHit = true
		}
		if returnHit {
			_ = output.RemoveRunning(t.Path)
			completedAt := time.Now()

			// Build proper two-phase metrics for the cache hit.
			returnPM := &output.PhaseMetrics{Cached: true, DurationMS: completedAt.Sub(startedAt).Milliseconds()}
			var taskPM *output.PhaseMetrics
			lifecycleStart := startedAt
			if v, ok := cfg.phaseMetrics.Load(t.ID); ok {
				info := v.(*taskPhaseInfo)
				taskPM = &info.Metrics
				lifecycleStart = info.StartedAt
			}
			allCached := taskPM != nil && taskPM.Cached
			totalDuration := completedAt.Sub(lifecycleStart).Milliseconds()

			metrics := output.Metrics{
				Task:        t.ID,
				Status:      "success",
				Cached:      allCached,
				Model:       t.EffectiveAgent.Model,
				StartedAt:   lifecycleStart.UTC().Format(time.RFC3339),
				CompletedAt: completedAt.UTC().Format(time.RFC3339),
				DurationMS:  totalDuration,
				TaskPhase:   taskPM,
				ReturnPhase: returnPM,
			}
			_ = output.WriteMetrics(t.Path, metrics)
			if err := recordArtifact(cfg, invocationID, t.ID, "return", canonicalArtifact(t.Path, outputType)); err != nil {
				return failResult(t.Path, t.ID, startedAt, err)
			}

			return TaskResult{
				TaskID:  t.ID,
				Status:  "cached",
				Metrics: metrics,
			}
		}
	}

	// Step 3: Ensure .running is present (may not be if task-phase was cached).
	if _, err := os.Stat(filepath.Join(t.Path, "output", ".running")); os.IsNotExist(err) {
		marker := output.RunningMarker{
			TaskID:    t.ID,
			StartedAt: startedAt.UTC().Format(time.RFC3339),
		}
		if err := output.WriteRunning(t.Path, marker); err != nil {
			return failResult(t.Path, t.ID, startedAt, fmt.Errorf("write .running: %w", err))
		}
	}

	// Step 4: Assemble return-phase prompt.
	rai, err := buildReturnAssemblyInput(t, graph, cfg.RunInput, cfg.RuntimeContext)
	if err != nil {
		return failResult(t.Path, t.ID, startedAt, fmt.Errorf("assemble return prompt: %w", err))
	}
	promptText, err := prompt.AssembleReturnPrompt(rai)
	if err != nil {
		return failResult(t.Path, t.ID, startedAt, fmt.Errorf("assemble return prompt: %w", err))
	}

	// Step 5: Execute through either the Provider loop or an external runtime.
	var schema json.RawMessage
	if t.Schema != nil {
		schema = t.Schema.Raw
	}
	execution, err := executeModel(ctx, t, cfg, promptText, "return", outputType, schema, invocationID)
	if err != nil {
		return failResult(t.Path, t.ID, startedAt, err)
	}

	// Step 6: Write canonical output.
	if outputType == "json" {
		if t.Schema == nil {
			return failResult(t.Path, t.ID, startedAt, fmt.Errorf("json task missing schema"))
		}
		if err := output.WriteJSONOutput(t.Path, execution.content, t.Schema.Raw); err != nil {
			return failResult(t.Path, t.ID, startedAt, err)
		}
	} else {
		if err := output.WriteResultMD(t.Path, execution.content); err != nil {
			return failResult(t.Path, t.ID, startedAt, err)
		}
	}
	if err := recordArtifact(cfg, invocationID, t.ID, "return", canonicalArtifact(t.Path, outputType)); err != nil {
		return failResult(t.Path, t.ID, startedAt, err)
	}

	// Step 7: Finalize — write hash, metrics, remove .running.
	completedAt := time.Now()
	if err := output.WriteHash(t.Path, returnCacheKey); err != nil {
		return failResult(t.Path, t.ID, startedAt, fmt.Errorf("write .hash: %w", err))
	}

	// Store to shared cache (best-effort).
	_ = StoreToCache(t.Path, cfg.CacheRoot, returnCacheKey, outputType)

	// Build phase-level metrics.
	returnPM := &output.PhaseMetrics{
		Cached:        false,
		Runtime:       execution.runtime,
		SessionID:     execution.sessionID,
		BillingBasis:  execution.billingBasis,
		DurationMS:    completedAt.Sub(startedAt).Milliseconds(),
		TokensIn:      execution.tokensIn,
		TokensOut:     execution.tokensOut,
		CostUSD:       execution.costUSD,
		RuntimeRecord: execution.runtimeRecord,
	}

	var taskPM *output.PhaseMetrics
	lifecycleStart := startedAt
	if v, ok := cfg.phaseMetrics.Load(t.ID); ok {
		info := v.(*taskPhaseInfo)
		taskPM = &info.Metrics
		lifecycleStart = info.StartedAt
	}

	// Top-level totals.
	totalTokensIn := execution.tokensIn
	totalTokensOut := execution.tokensOut
	totalCost := execution.costUSD
	allCached := false
	if taskPM != nil {
		totalTokensIn += taskPM.TokensIn
		totalTokensOut += taskPM.TokensOut
		totalCost += taskPM.CostUSD
		allCached = taskPM.Cached && returnPM.Cached
	}

	// Top-level duration covers the full lifecycle from task-phase start to
	// return-phase completion, including child execution time.
	totalDuration := completedAt.Sub(lifecycleStart).Milliseconds()

	metrics := output.Metrics{
		Task:           t.ID,
		Status:         "success",
		Cached:         allCached,
		Model:          execution.model,
		RequestedModel: execution.requestedModel,
		Runtime:        execution.runtime,
		SessionID:      execution.sessionID,
		BillingBasis:   execution.billingBasis,
		StartedAt:      lifecycleStart.UTC().Format(time.RFC3339),
		CompletedAt:    completedAt.UTC().Format(time.RFC3339),
		DurationMS:     totalDuration,
		TokensIn:       totalTokensIn,
		TokensOut:      totalTokensOut,
		CostUSD:        totalCost,
		TaskPhase:      taskPM,
		ReturnPhase:    returnPM,
		RuntimeRecords: phaseRuntimeRecords(taskPM, returnPM),
	}
	if err := output.WriteMetrics(t.Path, metrics); err != nil {
		return failResult(t.Path, t.ID, startedAt, fmt.Errorf("write .metrics.json: %w", err))
	}

	if err := output.RemoveRunning(t.Path); err != nil {
		return failResult(t.Path, t.ID, startedAt, fmt.Errorf("remove .running: %w", err))
	}

	return TaskResult{
		TaskID:        t.ID,
		Status:        "success",
		Metrics:       metrics,
		RuntimeResult: execution.runtimeResult,
	}
}

func runtimeRecords(record *runtime.Record) []runtime.Record {
	if record == nil {
		return nil
	}
	return []runtime.Record{*record}
}

func phaseRuntimeRecords(phases ...*output.PhaseMetrics) []runtime.Record {
	var records []runtime.Record
	for _, phase := range phases {
		if phase != nil && phase.RuntimeRecord != nil {
			records = append(records, *phase.RuntimeRecord)
		}
	}
	return records
}

// buildCacheInput gathers all data that contributes to the cache key.
// runInput is included for root tasks only (t.Parent == nil).
func buildCacheInput(t *task.Task, graph *task.Graph, runInput []input.Entry, runtimeCtx prompt.RuntimeContext) (CacheInput, error) {
	var schema json.RawMessage
	if t.Schema != nil {
		schema = t.Schema.Raw
	}

	// For shell tasks, only the model name matters for caching — persona,
	// temperature, max_tokens, max_cost_usd, and tools.md are all ignored
	// per RFC 0002. Normalize to prevent false cache misses.
	effectiveAgent := t.EffectiveAgent
	var tools []string
	if effectiveAgent.Model == "shell" {
		effectiveAgent = task.AgentConfig{Model: "shell"}
	} else {
		tools = t.Tools
	}

	// For two-phase tasks, the task-phase cache key excludes schema.md.
	// Schema applies only to canonical output (return phase for two-phase tasks).
	if t.HasReturn {
		schema = nil
	}

	ci := CacheInput{
		TaskMD:         NormalizeTaskMD(t.Frontmatter, t.Body),
		EffectiveAgent: effectiveAgent,
		Tools:          tools,
		Schema:         schema,
		RuntimeContext: runtimeCtx,
		StaticContext:  t.StaticContext,
	}

	// For shell tasks, include the source path in the cache key. Shell behavior
	// can depend on SMITH_SOURCE_PATH and SMITH_CONTEXT_DIR, so changing the
	// module reference must invalidate the cache even if the body is identical.
	// When SourcePath is empty (e.g. dry-run before RedirectPaths), use t.Path
	// since that IS the source path in a non-redirected context.
	if effectiveAgent.Model == "shell" {
		if t.SourcePath != "" {
			ci.SourcePath = t.SourcePath
		} else {
			ci.SourcePath = t.Path
		}
	}

	// Run input (root task only).
	if t.Parent == nil {
		ci.RunInput = runInput
	}

	// Parent output: task-phase output if parent has return.md, canonical otherwise.
	if t.Parent != nil {
		var content string
		var err error
		var parentType string
		if t.Parent.HasReturn {
			parentType = "markdown" // task-phase output is always markdown
			content, err = output.ReadTaskPhaseOutput(t.Parent.Path)
		} else {
			parentType = t.Parent.Frontmatter.OutputType()
			content, err = output.ReadCanonicalOutput(t.Parent.Path, parentType)
		}
		if err != nil && !os.IsNotExist(err) {
			return CacheInput{}, fmt.Errorf("read parent output: %w", err)
		}
		if err == nil {
			ci.ParentOutput = &prompt.OutputData{
				TaskID:  t.Parent.ID,
				Type:    parentType,
				Content: content,
			}
		}
	}

	// Sibling dependency outputs.
	for _, dep := range graph.SiblingDeps[t.ID] {
		depType := dep.Frontmatter.OutputType()
		content, err := output.ReadCanonicalOutput(dep.Path, depType)
		if err != nil && !os.IsNotExist(err) {
			return CacheInput{}, fmt.Errorf("read sibling output %q: %w", dep.ID, err)
		}
		if err == nil {
			ci.SiblingOutputs = append(ci.SiblingOutputs, prompt.OutputData{
				TaskID:  dep.ID,
				Type:    depType,
				Content: content,
			})
		}
	}

	return ci, nil
}

// buildAssemblyInput gathers prompt assembly data from the task and its dependencies.
// runInput is injected only for root tasks (t.Parent == nil).
func buildAssemblyInput(t *task.Task, graph *task.Graph, runInput []input.Entry, runtimeCtx prompt.RuntimeContext) (prompt.AssemblyInput, error) {
	ai := prompt.AssemblyInput{
		TaskBody:       t.Body,
		Constraints:    t.Frontmatter.Constraints,
		RuntimeContext: runtimeCtx,
		StaticContext:  t.StaticContext,
	}

	// Run input (root task only).
	if t.Parent == nil {
		ai.RunInput = runInput
	}

	// Parent output: task-phase output if parent has return.md, canonical otherwise.
	if t.Parent != nil {
		var content string
		var err error
		var parentType string
		if t.Parent.HasReturn {
			parentType = "markdown"
			content, err = output.ReadTaskPhaseOutput(t.Parent.Path)
		} else {
			parentType = t.Parent.Frontmatter.OutputType()
			content, err = output.ReadCanonicalOutput(t.Parent.Path, parentType)
		}
		if err != nil {
			if !os.IsNotExist(err) {
				return prompt.AssemblyInput{}, fmt.Errorf("read parent output: %w", err)
			}
			// Parent output not yet available (root task has no output yet).
		} else {
			ai.ParentOutput = &prompt.OutputData{
				TaskID:  t.Parent.ID,
				Type:    parentType,
				Content: content,
			}
		}
	}

	// Sibling dependency outputs.
	for _, dep := range graph.SiblingDeps[t.ID] {
		depType := dep.Frontmatter.OutputType()
		content, err := output.ReadCanonicalOutput(dep.Path, depType)
		if err != nil {
			return prompt.AssemblyInput{}, fmt.Errorf("read dependency output %q: %w", dep.ID, err)
		}
		ai.SiblingOutputs = append(ai.SiblingOutputs, prompt.OutputData{
			TaskID:  dep.ID,
			Type:    depType,
			Content: content,
		})
	}

	return ai, nil
}

// buildReturnAssemblyInput gathers return-phase prompt assembly data.
func buildReturnAssemblyInput(t *task.Task, graph *task.Graph, runInput []input.Entry, runtimeCtx prompt.RuntimeContext) (prompt.ReturnAssemblyInput, error) {
	rai := prompt.ReturnAssemblyInput{
		ReturnBody:        t.ReturnBody,
		ReturnConstraints: t.ReturnConstraints,
		RuntimeContext:    runtimeCtx,
		StaticContext:     t.StaticContext,
	}

	// Run input (root task only).
	if t.Parent == nil {
		rai.RunInput = runInput
	}

	// Parent output: task-phase if parent has return.md, canonical otherwise.
	if t.Parent != nil {
		var content string
		var err error
		var parentType string
		if t.Parent.HasReturn {
			parentType = "markdown"
			content, err = output.ReadTaskPhaseOutput(t.Parent.Path)
		} else {
			parentType = t.Parent.Frontmatter.OutputType()
			content, err = output.ReadCanonicalOutput(t.Parent.Path, parentType)
		}
		if err != nil {
			if !os.IsNotExist(err) {
				return prompt.ReturnAssemblyInput{}, fmt.Errorf("read parent output: %w", err)
			}
		} else {
			rai.ParentOutput = &prompt.OutputData{
				TaskID:  t.Parent.ID,
				Type:    parentType,
				Content: content,
			}
		}
	}

	// Sibling dependency outputs.
	for _, dep := range graph.SiblingDeps[t.ID] {
		depType := dep.Frontmatter.OutputType()
		content, err := output.ReadCanonicalOutput(dep.Path, depType)
		if err != nil {
			return prompt.ReturnAssemblyInput{}, fmt.Errorf("read dependency output %q: %w", dep.ID, err)
		}
		rai.SiblingOutputs = append(rai.SiblingOutputs, prompt.OutputData{
			TaskID:  dep.ID,
			Type:    depType,
			Content: content,
		})
	}

	// This task's task-phase output.
	tpContent, err := output.ReadTaskPhaseOutput(t.Path)
	if err != nil {
		return prompt.ReturnAssemblyInput{}, fmt.Errorf("read task-phase output: %w", err)
	}
	rai.TaskPhaseOutput = &prompt.OutputData{
		TaskID:  t.ID,
		Type:    "markdown",
		Content: tpContent,
	}

	// All children's canonical outputs, ordered by t.Children.
	for _, child := range t.Children {
		childType := child.Frontmatter.OutputType()
		content, err := output.ReadCanonicalOutput(child.Path, childType)
		if err != nil {
			return prompt.ReturnAssemblyInput{}, fmt.Errorf("read child output %q: %w", child.ID, err)
		}
		rai.ChildOutputs = append(rai.ChildOutputs, prompt.OutputData{
			TaskID:  child.ID,
			Type:    childType,
			Content: content,
		})
	}

	return rai, nil
}

// buildReturnCacheInput gathers all data that contributes to the return-phase cache key.
func buildReturnCacheInput(t *task.Task, graph *task.Graph, runInput []input.Entry, runtimeCtx prompt.RuntimeContext) (ReturnCacheInput, error) {
	var schema json.RawMessage
	if t.Schema != nil {
		schema = t.Schema.Raw
	}

	rci := ReturnCacheInput{
		ReturnMD:       NormalizeReturnMD(t.ReturnConstraints, t.ReturnBody),
		EffectiveAgent: t.EffectiveAgent,
		Tools:          t.Tools,
		Schema:         schema,
		RuntimeContext: runtimeCtx,
		StaticContext:  t.StaticContext,
	}

	if t.EffectiveAgent.Model == "shell" {
		if t.SourcePath != "" {
			rci.SourcePath = t.SourcePath
		} else {
			rci.SourcePath = t.Path
		}
	}

	// Run input (root task only).
	if t.Parent == nil {
		rci.RunInput = runInput
	}

	// Parent output: task-phase if parent has return.md, canonical otherwise.
	if t.Parent != nil {
		var content string
		var err error
		var parentType string
		if t.Parent.HasReturn {
			parentType = "markdown"
			content, err = output.ReadTaskPhaseOutput(t.Parent.Path)
		} else {
			parentType = t.Parent.Frontmatter.OutputType()
			content, err = output.ReadCanonicalOutput(t.Parent.Path, parentType)
		}
		if err != nil && !os.IsNotExist(err) {
			return ReturnCacheInput{}, fmt.Errorf("read parent output: %w", err)
		}
		if err == nil {
			rci.ParentOutput = &prompt.OutputData{
				TaskID:  t.Parent.ID,
				Type:    parentType,
				Content: content,
			}
		}
	}

	// Sibling dependency outputs.
	for _, dep := range graph.SiblingDeps[t.ID] {
		depType := dep.Frontmatter.OutputType()
		content, err := output.ReadCanonicalOutput(dep.Path, depType)
		if err != nil && !os.IsNotExist(err) {
			return ReturnCacheInput{}, fmt.Errorf("read sibling output %q: %w", dep.ID, err)
		}
		if err == nil {
			rci.SiblingOutputs = append(rci.SiblingOutputs, prompt.OutputData{
				TaskID:  dep.ID,
				Type:    depType,
				Content: content,
			})
		}
	}

	// This task's task-phase output.
	tpContent, err := output.ReadTaskPhaseOutput(t.Path)
	if err != nil && !os.IsNotExist(err) {
		return ReturnCacheInput{}, fmt.Errorf("read task-phase output: %w", err)
	}
	if err == nil {
		rci.TaskPhaseOutput = &prompt.OutputData{
			TaskID:  t.ID,
			Type:    "markdown",
			Content: tpContent,
		}
	}

	// All children's canonical outputs.
	for _, child := range t.Children {
		childType := child.Frontmatter.OutputType()
		content, err := output.ReadCanonicalOutput(child.Path, childType)
		if err != nil && !os.IsNotExist(err) {
			return ReturnCacheInput{}, fmt.Errorf("read child output %q: %w", child.ID, err)
		}
		if err == nil {
			rci.ChildOutputs = append(rci.ChildOutputs, prompt.OutputData{
				TaskID:  child.ID,
				Type:    childType,
				Content: content,
			})
		}
	}

	return rci, nil
}

// --- Dry Run ---

// DryRunResult holds the execution plan without actually executing.
type DryRunResult struct {
	Tasks []DryRunTask
}

// DryRunTask describes one task in the dry-run report.
type DryRunTask struct {
	TaskID            string
	Model             string
	Level             int
	Cached            bool // current on-disk cache status (may change if upstream reruns)
	DepsOn            []string
	Scope             map[string]string // nil if no scope set
	HasReturn         bool              // true if task has return.md
	TaskPhaseCached   bool              // two-phase only: task-phase cache status
	ReturnPhaseCached bool              // two-phase only: return-phase cache status
	ExecutionProfile  *runtime.ResolvedProfile
}

// DryRunOpts holds options that affect dry-run cache status reporting.
type DryRunOpts struct {
	NoCache          bool                  // report all tasks as not cached
	RunInput         []input.Entry         // run input entries for cache key computation
	Scope            map[string]string     // scope parameters to report
	RuntimeContext   prompt.RuntimeContext // prompt/calendar context for cache key computation
	CacheRoot        string                // shared task cache root (checked before local .hash)
	ExternalProfiles map[string]runtime.ResolvedProfile
}

// DryRun validates and reports the execution plan without calling providers.
func DryRun(root *task.Task, graph *task.Graph, opts ...DryRunOpts) *DryRunResult {
	var o DryRunOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.RuntimeContext.IsZero() {
		o.RuntimeContext = prompt.NewRuntimeContext(time.Now())
	}

	var tasks []DryRunTask
	seen := make(map[string]bool) // deduplicate: one entry per task ID

	for levelIdx, level := range graph.Plan {
		for _, node := range level {
			t := node.Task
			// Emit one entry per task, skip return-phase duplicates.
			if seen[t.ID] {
				continue
			}
			seen[t.ID] = true

			outputType := t.Frontmatter.OutputType()
			taskPhaseCached := false
			returnPhaseCached := false
			cached := false

			if !o.NoCache && t.Frontmatter.CachePolicy() != "never" {
				cacheInput, err := buildCacheInput(t, graph, o.RunInput, o.RuntimeContext)
				if err == nil {
					cacheKey := ComputeCacheKey(cacheInput)
					if t.HasReturn {
						// Check shared cache first, fall back to local.
						taskPhaseCached = CheckSharedTaskPhaseCache(o.CacheRoot, cacheKey) == CacheHit ||
							CheckTaskPhaseCache(t.Path, cacheKey) == CacheHit
						rci, rErr := buildReturnCacheInput(t, graph, o.RunInput, o.RuntimeContext)
						if rErr == nil {
							rKey := ComputeReturnCacheKey(rci)
							returnPhaseCached = CheckSharedCache(o.CacheRoot, rKey) == CacheHit ||
								CheckCache(t.Path, rKey, outputType) == CacheHit
						}
						cached = taskPhaseCached && returnPhaseCached
					} else {
						cached = CheckSharedCache(o.CacheRoot, cacheKey) == CacheHit ||
							CheckCache(t.Path, cacheKey, outputType) == CacheHit
					}
				}
			}

			var deps []string
			for _, d := range graph.Deps[t.ID] {
				deps = append(deps, d.ID)
			}

			dt := DryRunTask{
				TaskID:            t.ID,
				Model:             t.EffectiveAgent.Model,
				Level:             levelIdx,
				Cached:            cached,
				DepsOn:            deps,
				HasReturn:         t.HasReturn,
				TaskPhaseCached:   taskPhaseCached,
				ReturnPhaseCached: returnPhaseCached,
			}
			if profile, ok := o.ExternalProfiles[t.ID]; ok {
				value := profile
				dt.ExecutionProfile = &value
			}
			if len(t.Tools) > 0 && len(o.Scope) > 0 {
				dt.Scope = o.Scope
			}
			tasks = append(tasks, dt)
		}
	}

	return &DryRunResult{Tasks: tasks}
}

// --- Status helpers (for output/) ---

// Deprecated: TaskStatus uses filesystem heuristics. Prefer reading the run manifest
// via run.LatestManifest(). Kept for planner fallback on legacy apps.
//
// TaskStatus determines the persisted status of a task by inspecting output/.
// For two-phase tasks (hasReturn=true), inspects both output/task/ and output/.
func TaskStatus(taskPath, outputType string, hasReturn bool) string {
	outDir := filepath.Join(taskPath, "output")

	if _, err := os.Stat(filepath.Join(outDir, ".running")); err == nil {
		return "running"
	}
	if _, err := os.Stat(filepath.Join(outDir, ".hash")); err == nil {
		var canonical string
		if outputType == "json" {
			canonical = "result.json"
		} else {
			canonical = "result.md"
		}
		if _, err := os.Stat(filepath.Join(outDir, canonical)); err == nil {
			return "success"
		}
	}

	// For two-phase tasks: task-phase done but no canonical output.
	// At this point .running is absent (checked above) and canonical .hash is absent.
	// If task-phase artifacts exist, the return phase was skipped (child failure) → failed.
	if hasReturn {
		taskPhaseHash := filepath.Join(outDir, "task", ".hash")
		taskPhaseResult := filepath.Join(outDir, "task", "result.md")
		if _, err := os.Stat(taskPhaseHash); err == nil {
			if _, err := os.Stat(taskPhaseResult); err == nil {
				return "failed"
			}
		}
	}

	if _, err := os.Stat(filepath.Join(outDir, "result.md")); err == nil {
		return "failed"
	}
	return "pending"
}
