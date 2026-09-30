package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/boxsie/smith/internal/defaults"
	"github.com/boxsie/smith/internal/executor"
	"github.com/boxsie/smith/internal/input"
	"github.com/boxsie/smith/internal/output"
	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/task"
	"github.com/boxsie/smith/internal/tools"
)

// PrepareRunRequest contains already-parsed transport input. Reading stdin and
// rendering results remain responsibilities of the CLI adapter.
type PrepareRunRequest struct {
	AppRoot                     string
	Input                       []input.Entry
	Scope                       map[string]string
	WritableRoots               []string
	AllowUncontainedDevelopment bool
	// ParentInvocationID causally nests the app's task invocations beneath a
	// containing live-patch node invocation. Empty preserves top-level runs.
	ParentInvocationID string
	NoCache            bool
	ClearCache         bool
}

// PreparedRun is a validated run ready for dry-run inspection or execution.
// A PreparedRun is single-use once Execute redirects task output paths.
type PreparedRun struct {
	service                     *Service
	AppRoot                     string
	Validation                  *ValidationResult
	Input                       []input.Entry
	Scope                       map[string]string
	WritableRoots               []string
	AllowUncontainedDevelopment bool
	ExternalProfiles            map[string]runtime.ResolvedProfile
	ParentInvocationID          string
	NoCache                     bool
	mu                          sync.Mutex
	runID                       string
	executed                    bool
}

// RunResult contains the canonical task result, execution state, and non-fatal
// service warnings. Output is markdown text or schema-validated JSON according
// to OutputType; transport adapters may wrap it without changing task meaning.
type RunResult struct {
	RunID      string
	RunDir     string
	AppRoot    string
	Output     string
	OutputType string
	Execution  *executor.Result
	Warnings   []error
}

// RunHandle identifies work that has been durably queued and detached from
// the request which started it.
type RunHandle struct {
	RunID   string `json:"run_id"`
	RunDir  string `json:"run_dir"`
	AppRoot string `json:"app_root"`
}

type runLease interface {
	Release() error
}

func acquireRunLease(runDir string) (runLease, error) {
	return run.AcquireRunLease(runDir)
}

type activeRun struct {
	handle RunHandle
	store  *run.EventStore
	lease  runLease
	cancel context.CancelFunc
	done   chan struct{}

	cancelOnce sync.Once
	ordinal    atomic.Uint64
	resultMu   sync.Mutex
	result     *RunResult
	err        error
}

func (a *activeRun) newInvocationID() string {
	return run.InvocationID(a.handle.RunID, a.ordinal.Add(1))
}

func (a *activeRun) finish(result *RunResult, err error) {
	a.resultMu.Lock()
	a.result = result
	a.err = err
	a.resultMu.Unlock()
	close(a.done)
}

func (a *activeRun) outcome() (*RunResult, error) {
	a.resultMu.Lock()
	defer a.resultMu.Unlock()
	return a.result, a.err
}

// PrepareRun resolves defaults, optionally clears cache, and validates the app.
// It deliberately performs validation before any CLI adapter reads piped stdin.
func (s *Service) PrepareRun(request PrepareRunRequest) (*PreparedRun, error) {
	absRoot, err := filepath.Abs(request.AppRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve app path: %w", err)
	}

	runInput := append([]input.Entry(nil), request.Input...)
	defs, err := defaults.Load(absRoot)
	if err != nil {
		return nil, err
	}
	if len(defs) > 0 {
		runInput = defaults.MergeWithInputs(defs, runInput)
	}

	if request.ClearCache {
		if err := os.RemoveAll(run.CacheRoot(absRoot)); err != nil {
			return nil, fmt.Errorf("clear cache: %w", err)
		}
	}

	validation, err := s.Validate(absRoot)
	if err != nil {
		return nil, err
	}

	scope := withDefaultRootScope(request.Scope, absRoot)
	profiles, err := executor.ResolveExternalProfiles(validation.Validation.Root, scope[tools.ScopeRoot], request.WritableRoots, true)
	if err != nil {
		return nil, err
	}

	return &PreparedRun{
		service:                     s,
		AppRoot:                     absRoot,
		Validation:                  validation,
		Input:                       runInput,
		Scope:                       scope,
		WritableRoots:               append([]string(nil), request.WritableRoots...),
		AllowUncontainedDevelopment: request.AllowUncontainedDevelopment,
		ExternalProfiles:            profiles,
		ParentInvocationID:          request.ParentInvocationID,
		NoCache:                     request.NoCache,
	}, nil
}

// DryRun computes the current execution plan without creating run state.
func (p *PreparedRun) DryRun() *executor.DryRunResult {
	return executor.DryRun(
		p.Validation.Validation.Root,
		p.Validation.Validation.Graph,
		executor.DryRunOpts{
			NoCache:          p.NoCache,
			RunInput:         p.Input,
			Scope:            p.Scope,
			CacheRoot:        run.CacheRoot(p.AppRoot),
			ExternalProfiles: p.ExternalProfiles,
		},
	)
}

// RunID returns the ID that Execute will use, allocating it lazily so dry runs
// do not consume IDs. Adapters may call this before Execute to report progress.
func (p *PreparedRun) RunID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.runID == "" {
		p.runID = p.service.newRunID()
	}
	return p.runID
}

// Start durably queues a prepared app and returns before provider execution.
// runInput permits the CLI to add validated piped stdin after PrepareRun; pass
// nil to use the prepared inputs unchanged.
func (p *PreparedRun) Start(ctx context.Context, runInput []input.Entry) (RunHandle, error) {
	p.mu.Lock()
	if p.executed {
		p.mu.Unlock()
		return RunHandle{}, fmt.Errorf("prepared run %s has already been executed", p.runID)
	}
	p.executed = true
	if p.runID == "" {
		p.runID = p.service.newRunID()
	}
	runID := p.runID
	p.mu.Unlock()

	if runInput == nil {
		runInput = p.Input
	}
	runInput = append([]input.Entry(nil), runInput...)

	runDir := run.RunDir(p.AppRoot, runID)
	runtimeDir := run.RuntimeDir(runDir)
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return RunHandle{}, fmt.Errorf("create run directory: %w", err)
	}

	vr := p.Validation.Validation
	task.RedirectPaths(vr.Root, p.AppRoot, runtimeDir)

	var taskIDs []string
	task.WalkTree(vr.Root, func(t *task.Task) {
		taskIDs = append(taskIDs, t.ID)
	})
	inputStrs := make([]string, len(runInput))
	for i, entry := range runInput {
		inputStrs[i] = entry.Name + "=" + entry.Value
	}
	lease, err := p.service.acquireRunLease(runDir)
	if err != nil {
		return RunHandle{}, fmt.Errorf("acquire run ownership: %w", err)
	}
	store, err := run.NewEventStore(runDir, p.service.clock)
	if err != nil {
		return RunHandle{}, errors.Join(fmt.Errorf("open run event store: %w", err), releaseRunLease(lease))
	}
	if _, err := store.Append(run.Event{
		Type:      run.EventRunQueued,
		RunID:     runID,
		AppRoot:   p.AppRoot,
		OwnerPID:  os.Getpid(),
		NoCache:   p.NoCache,
		RunInputs: inputStrs,
		TaskIDs:   taskIDs,
	}); err != nil {
		return RunHandle{}, errors.Join(fmt.Errorf("queue run: %w", err), releaseRunLease(lease))
	}

	handle := RunHandle{RunID: runID, RunDir: runDir, AppRoot: p.AppRoot}
	executionCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	active := &activeRun{handle: handle, store: store, lease: lease, cancel: cancel, done: make(chan struct{})}
	p.service.runsMu.Lock()
	p.service.runs[runKey(p.AppRoot, runID)] = active
	p.service.runsMu.Unlock()

	go p.execute(executionCtx, active, runInput)
	return handle, nil
}

// Execute preserves the synchronous service/CLI contract by waiting on Start.
func (p *PreparedRun) Execute(ctx context.Context, runInput []input.Entry) (*RunResult, error) {
	handle, err := p.Start(ctx, runInput)
	if err != nil {
		return nil, err
	}
	result, err := p.service.WaitRun(ctx, handle.AppRoot, handle.RunID)
	if err == nil {
		return result, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		_, _ = p.service.CancelRun(handle.AppRoot, handle.RunID)
	}
	return nil, err
}

func (p *PreparedRun) execute(ctx context.Context, active *activeRun, runInput []input.Entry) {
	result, err := p.executeRun(ctx, active, runInput)
	err = errors.Join(err, releaseRunLease(active.lease))
	active.finish(result, err)
}

func releaseRunLease(lease runLease) error {
	if err := lease.Release(); err != nil {
		return fmt.Errorf("release run lease: %w", err)
	}
	return nil
}

func (p *PreparedRun) executeRun(ctx context.Context, active *activeRun, runInput []input.Entry) (*RunResult, error) {
	handle := active.handle
	vr := p.Validation.Validation
	if _, err := active.store.Append(run.Event{Type: run.EventRunStarted, RunID: handle.RunID}); err != nil {
		return nil, fmt.Errorf("start run: %w", err)
	}
	p.service.emit(ctx, Event{Operation: "run", State: "started", AppRoot: handle.AppRoot, RunID: handle.RunID})

	registry := p.service.registryFactory()
	resolvedDefs, err := tools.RegisterResolvedTools(registry, vr.ResolvedTools, tools.RegisterResolvedToolsConfig{
		ProjectRoot:     handle.AppRoot,
		Factory:         p.service.factory,
		ExternalFactory: p.service.externalFactory,
		Scope:           p.Scope,
		SubExecute: func(ctx context.Context, root *task.Task, graph *task.Graph, subCfg tools.SubExecConfig) error {
			subLogger := tools.NewHistoryLogger(run.ToolHistoryDir(handle.RunDir))
			subLogger.RunID = handle.RunID
			_, err := executor.Execute(ctx, root, graph, executor.Config{
				Factory:                     subCfg.Factory,
				ExternalFactory:             subCfg.ExternalFactory,
				Adapter:                     subCfg.Adapter,
				NoCache:                     subCfg.NoCache,
				RunInput:                    subCfg.RunInput,
				Scope:                       subCfg.Scope,
				ResolvedDefs:                subCfg.ResolvedDefs,
				RunID:                       handle.RunID,
				HistoryLogger:               subLogger,
				Events:                      active.store,
				NewInvocationID:             active.newInvocationID,
				ParentInvocationID:          run.CausalParent(ctx),
				AppRoot:                     handle.AppRoot,
				WritableRoots:               p.WritableRoots,
				AllowUncontainedDevelopment: p.AllowUncontainedDevelopment,
			})
			return err
		},
	})
	if err != nil {
		p.failRun(ctx, active, err)
		return nil, err
	}

	historyLogger := tools.NewHistoryLogger(run.ToolHistoryDir(handle.RunDir))
	historyLogger.RunID = handle.RunID
	execution, err := executor.Execute(ctx, vr.Root, vr.Graph, executor.Config{
		Factory:                     p.service.factory,
		ExternalFactory:             p.service.externalFactory,
		NoCache:                     p.NoCache,
		RunInput:                    runInput,
		Adapter:                     registry,
		Scope:                       p.Scope,
		ResolvedDefs:                resolvedDefs,
		HistoryLogger:               historyLogger,
		RunID:                       handle.RunID,
		RunDir:                      handle.RunDir,
		CacheRoot:                   run.CacheRoot(handle.AppRoot),
		Events:                      active.store,
		NewInvocationID:             active.newInvocationID,
		ParentInvocationID:          p.ParentInvocationID,
		ProjectManifest:             true,
		AppRoot:                     handle.AppRoot,
		WritableRoots:               p.WritableRoots,
		AllowUncontainedDevelopment: p.AllowUncontainedDevelopment,
		ExternalProfiles:            p.ExternalProfiles,
	})
	if err != nil {
		p.failRun(ctx, active, err)
		return nil, err
	}
	if !execution.Success {
		result := &RunResult{
			RunID:     handle.RunID,
			RunDir:    handle.RunDir,
			AppRoot:   handle.AppRoot,
			Execution: execution,
			Warnings:  p.service.track(handle.AppRoot),
		}
		if ctx.Err() != nil {
			if _, appendErr := active.store.Append(run.Event{Type: run.EventRunCancelled, RunID: handle.RunID, Error: ctx.Err().Error()}); appendErr != nil {
				return result, appendErr
			}
			p.service.emit(ctx, Event{Operation: "run", State: "cancelled", AppRoot: handle.AppRoot, RunID: handle.RunID, Err: ctx.Err()})
			return result, ctx.Err()
		}
		if _, err := active.store.Append(run.Event{Type: run.EventRunFailed, RunID: handle.RunID}); err != nil {
			return result, err
		}
		p.service.emit(ctx, Event{Operation: "run", State: "failed", AppRoot: handle.AppRoot, RunID: handle.RunID})
		return result, nil
	}

	outputType := vr.Root.Frontmatter.OutputType()
	canonical, err := output.ReadCanonicalOutput(vr.Root.Path, outputType)
	if err != nil {
		err = fmt.Errorf("read canonical task result: %w", err)
		p.failRun(ctx, active, err)
		return nil, err
	}

	result := &RunResult{
		RunID:      handle.RunID,
		RunDir:     handle.RunDir,
		AppRoot:    handle.AppRoot,
		Output:     canonical,
		OutputType: outputType,
		Execution:  execution,
		Warnings:   p.service.track(handle.AppRoot),
	}
	if _, err := active.store.Append(run.Event{Type: run.EventRunCompleted, RunID: handle.RunID}); err != nil {
		return result, err
	}
	p.service.emit(ctx, Event{Operation: "run", State: "completed", AppRoot: handle.AppRoot, RunID: handle.RunID})
	return result, nil
}

func (p *PreparedRun) failRun(ctx context.Context, active *activeRun, cause error) {
	eventType := run.EventRunFailed
	state := "failed"
	if errors.Is(ctx.Err(), context.Canceled) {
		eventType = run.EventRunCancelled
		state = "cancelled"
	}
	_, _ = active.store.Append(run.Event{Type: eventType, RunID: active.handle.RunID, Error: cause.Error()})
	p.service.emit(ctx, Event{Operation: "run", State: state, AppRoot: active.handle.AppRoot, RunID: active.handle.RunID, Err: cause})
}

// WaitRun waits for a run owned by this service instance. Waiting cancellation
// does not itself cancel background work; CancelRun is explicit.
func (s *Service) WaitRun(ctx context.Context, appRoot, runID string) (*RunResult, error) {
	absRoot, err := filepath.Abs(appRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve app path: %w", err)
	}
	s.runsMu.Lock()
	active := s.runs[runKey(absRoot, runID)]
	s.runsMu.Unlock()
	if active == nil {
		return nil, fmt.Errorf("run %s is not owned by this service instance", runID)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-active.done:
		return active.outcome()
	}
}

// CancelRun idempotently requests cancellation. The returned bool is true only
// for the call that first issued the request.
func (s *Service) CancelRun(appRoot, runID string) (bool, error) {
	absRoot, err := filepath.Abs(appRoot)
	if err != nil {
		return false, fmt.Errorf("resolve app path: %w", err)
	}
	s.runsMu.Lock()
	active := s.runs[runKey(absRoot, runID)]
	s.runsMu.Unlock()
	if active == nil {
		_, err := s.Status(absRoot, runID)
		return false, err
	}
	select {
	case <-active.done:
		return false, nil
	default:
	}

	requested := false
	var appendErr error
	active.cancelOnce.Do(func() {
		requested = true
		_, appendErr = active.store.Append(run.Event{Type: run.EventRunCancelRequested, RunID: runID})
		active.cancel()
	})
	return requested, appendErr
}

// ReadRunEvents reads a bounded page of durable run history.
func (s *Service) ReadRunEvents(appRoot, runID string, after uint64, limit int) (run.EventPage, error) {
	absRoot, err := filepath.Abs(appRoot)
	if err != nil {
		return run.EventPage{}, fmt.Errorf("resolve app path: %w", err)
	}
	runDir := run.RunDir(absRoot, runID)
	if _, err := s.reconcileRun(runDir); err != nil {
		return run.EventPage{}, err
	}
	return run.ReadEvents(runDir, after, limit)
}

// ReadRunAttempts reconstructs a bounded conductor view from durable events.
func (s *Service) ReadRunAttempts(appRoot, runID string, after uint64, limit int) (run.AttemptInspectionPage, error) {
	absRoot, err := filepath.Abs(appRoot)
	if err != nil {
		return run.AttemptInspectionPage{}, fmt.Errorf("resolve app path: %w", err)
	}
	runDir := run.RunDir(absRoot, runID)
	if _, err := s.reconcileRun(runDir); err != nil {
		return run.AttemptInspectionPage{}, err
	}
	return run.ReadAttemptInspections(runDir, after, limit)
}

func runKey(appRoot, runID string) string { return appRoot + "\x00" + runID }

func withDefaultRootScope(scope map[string]string, absRoot string) map[string]string {
	out := make(map[string]string, len(scope)+1)
	for key, value := range scope {
		out[key] = value
	}
	if out[tools.ScopeRoot] == "" && absRoot != "" {
		out[tools.ScopeRoot] = absRoot
	}
	return out
}
