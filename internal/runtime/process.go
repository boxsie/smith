package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/boxsie/smith/internal/shell"
)

// ProcessRequest is deliberately argv-based. Adapters must never interpolate
// invocation content into a shell command string.
type ProcessRequest struct {
	Executable  string
	Args        []string
	Dir         string
	Env         []string
	Stdin       []byte
	Limits      LimitPolicy
	Containment ContainmentAdmission
	// StdoutLine observes complete protocol lines while the process is still
	// running. The raw stream is retained independently in ProcessResult.
	StdoutLine func([]byte) error
	// TerminationStage observes controller-owned graceful and hard-kill
	// requests. It is not called for a process that exits normally.
	TerminationStage func(string)
}

type ProcessResult struct {
	Stdout       []byte
	Stderr       []byte
	ExitCode     int
	Measurements ResourceMeasurements
}

type ProcessRunner interface {
	Run(context.Context, ProcessRequest) (ProcessResult, error)
}

type OSProcessRunner struct{}

const (
	DefaultExternalMemoryBytes int64 = 4 << 30
	DefaultExternalProcesses         = 256
)

var (
	ErrProcessResourceLimit          = errors.New("external runtime resource limit exceeded")
	ErrProcessContainmentUnavailable = errors.New("external runtime containment unavailable")
)

type ProcessResourceError struct {
	Resource string
	Limit    int64
	Err      error
}

func processLimitDefaults(limits LimitPolicy) LimitPolicy {
	if limits.MaxMemoryBytes == 0 {
		limits.MaxMemoryBytes = DefaultExternalMemoryBytes
	}
	if limits.MaxProcesses == 0 {
		limits.MaxProcesses = DefaultExternalProcesses
	}
	return limits
}

func (e *ProcessResourceError) Error() string {
	return fmt.Sprintf("external runtime %s limit exceeded (%d): %v", e.Resource, e.Limit, e.Err)
}

func (e *ProcessResourceError) Unwrap() []error {
	return []error{ErrProcessResourceLimit, e.Err}
}

// ProcessError preserves process status and diagnostics independently of the
// stdout protocol stream.
type ProcessError struct {
	Executable string
	ExitCode   int
	Stderr     string
	Err        error
}

func (e *ProcessError) Error() string {
	return fmt.Sprintf("external runtime process %q exited %d: %s", e.Executable, e.ExitCode, e.Stderr)
}

func (e *ProcessError) Unwrap() error { return e.Err }

func (r OSProcessRunner) Run(ctx context.Context, request ProcessRequest) (ProcessResult, error) {
	if request.Executable == "" {
		return ProcessResult{}, fmt.Errorf("external runtime executable is required")
	}
	if request.Dir == "" || !filepath.IsAbs(request.Dir) {
		return ProcessResult{}, fmt.Errorf("external runtime workspace must be an absolute path")
	}
	if request.Containment.Mechanism == "" {
		profile, limits, err := ResolveExecutionProfile("", request.Limits)
		if err != nil {
			return ProcessResult{}, err
		}
		admission, err := (HostContainmentAdmitter{}).Admit(ctx, ContainmentRequest{Profile: profile, Limits: limits})
		if err != nil {
			return ProcessResult{}, err
		}
		request.Limits = limits
		request.Containment = admission
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	launch, err := prepareProcessLaunch(runCtx, request)
	if err != nil {
		return ProcessResult{}, &ProcessLaunchError{Executable: request.Executable, Err: err}
	}
	cmd := launch.command
	cmd.Stdin = bytes.NewReader(request.Stdin)
	var stdout, stderr bytes.Buffer
	observer := newLineObserver(request.StdoutLine, cancel)
	limitState := &processLimitState{cancel: cancel}
	outputBudget := &sharedByteBudget{limit: int64(request.Containment.EffectiveLimits.MaxOutputBytes), state: limitState}
	cmd.Stdout = &budgetWriter{budget: outputBudget, destination: io.MultiWriter(&stdout, observer)}
	cmd.Stderr = &budgetWriter{budget: outputBudget, destination: &stderr}
	monitor, err := startWorkspaceGrowthMonitor(request.Dir, request.Containment.EffectiveLimits.MaxWorkspaceBytes, limitState)
	if err != nil {
		return ProcessResult{}, err
	}

	grace, err := request.Containment.EffectiveLimits.TerminationGraceDuration()
	if err != nil {
		return ProcessResult{}, err
	}
	stopMeasurements := startProcessMeasurements(launch)
	err = shell.RunCommandWithTermination(runCtx, cmd, shell.TerminationPlan{
		Grace: grace, Graceful: launch.graceful, Kill: launch.kill,
		Observe: request.TerminationStage,
	})
	measurements := stopMeasurements()
	monitor.stop()
	observer.flush()
	result := ProcessResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: 0, Measurements: measurements}
	if limitErr := limitState.err(); limitErr != nil {
		return result, limitErr
	}
	if observeErr := observer.err(); observeErr != nil {
		return result, fmt.Errorf("observe external runtime stdout: %w", observeErr)
	}
	if err == nil {
		return result, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	if cmd.Process == nil {
		return result, &ProcessLaunchError{Executable: request.Executable, Err: err}
	}
	result.ExitCode = -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	processErr := &ProcessError{
		Executable: request.Executable,
		ExitCode:   result.ExitCode,
		Stderr:     stderr.String(),
		Err:        err,
	}
	return result, launch.classify(processErr, stdout.Bytes(), stderr.Bytes())
}

type processLimitState struct {
	mu       sync.Mutex
	cancel   context.CancelFunc
	firstErr error
}

func (s *processLimitState) fail(err error) {
	s.mu.Lock()
	if s.firstErr == nil {
		s.firstErr = err
		s.cancel()
	}
	s.mu.Unlock()
}

func (s *processLimitState) err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstErr
}

type sharedByteBudget struct {
	mu    sync.Mutex
	used  int64
	limit int64
	state *processLimitState
}

func (b *sharedByteBudget) take(requested int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit <= 0 {
		return requested
	}
	remaining := b.limit - b.used
	allowed := requested
	if int64(allowed) > remaining {
		allowed = max(0, int(remaining))
	}
	b.used += int64(allowed)
	if allowed < requested {
		b.state.fail(&ProcessResourceError{
			Resource: "output",
			Limit:    b.limit,
			Err:      fmt.Errorf("combined stdout and stderr exceeded the admitted byte budget"),
		})
	}
	return allowed
}

type budgetWriter struct {
	budget      *sharedByteBudget
	destination io.Writer
}

func (w *budgetWriter) Write(chunk []byte) (int, error) {
	allowed := w.budget.take(len(chunk))
	if allowed > 0 {
		if _, err := w.destination.Write(chunk[:allowed]); err != nil {
			return 0, err
		}
	}
	// Report the whole chunk consumed. Crossing the limit cancels the process;
	// returning a short write would obscure the typed resource failure.
	return len(chunk), nil
}

type workspaceGrowthMonitor struct {
	stopCh chan struct{}
	done   chan struct{}
}

func startWorkspaceGrowthMonitor(root string, limit int64, state *processLimitState) (*workspaceGrowthMonitor, error) {
	monitor := &workspaceGrowthMonitor{stopCh: make(chan struct{}), done: make(chan struct{})}
	if limit <= 0 {
		close(monitor.done)
		return monitor, nil
	}
	baseline, err := directoryBytes(root)
	if err != nil {
		return nil, fmt.Errorf("measure external runtime workspace before launch: %w", err)
	}
	check := func() {
		current, measureErr := directoryBytes(root)
		if measureErr != nil {
			state.fail(errors.Join(ErrProcessContainmentUnavailable, fmt.Errorf("measure runtime workspace growth: %w", measureErr)))
			return
		}
		if current > baseline && current-baseline > limit {
			state.fail(&ProcessResourceError{
				Resource: "workspace",
				Limit:    limit,
				Err:      fmt.Errorf("workspace grew by %d bytes", current-baseline),
			})
		}
	}
	go func() {
		defer close(monitor.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				check()
			case <-monitor.stopCh:
				check()
				return
			}
		}
	}()
	return monitor, nil
}

func (m *workspaceGrowthMonitor) stop() {
	select {
	case <-m.done:
		return
	default:
	}
	close(m.stopCh)
	<-m.done
}

func directoryBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

type processLaunch struct {
	command           *exec.Cmd
	graceful          func() error
	kill              func() error
	classify          func(error, []byte, []byte) error
	startMeasurements func() func() ResourceMeasurements
}

func startProcessMeasurements(launch processLaunch) func() ResourceMeasurements {
	if launch.startMeasurements != nil {
		return launch.startMeasurements()
	}
	return func() ResourceMeasurements {
		return ResourceMeasurements{UnavailableReason: "the selected containment mechanism does not expose process resource measurements"}
	}
}

func directProcessLaunch(request ProcessRequest) processLaunch {
	cmd := exec.Command(request.Executable, request.Args...)
	cmd.Dir = request.Dir
	// A nil/empty environment is deliberately sterile. Adapters explicitly
	// forward only the authentication and configuration they require.
	cmd.Env = append([]string{}, request.Env...)
	return processLaunch{
		command:  cmd,
		classify: func(err error, _, _ []byte) error { return err },
	}
}

type lineObserver struct {
	mu       sync.Mutex
	buffer   []byte
	callback func([]byte) error
	cancel   context.CancelFunc
	firstErr error
}

func newLineObserver(callback func([]byte) error, cancel context.CancelFunc) *lineObserver {
	return &lineObserver{callback: callback, cancel: cancel}
}

func (w *lineObserver) Write(chunk []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.callback == nil || w.firstErr != nil {
		return len(chunk), nil
	}
	w.buffer = append(w.buffer, chunk...)
	for {
		index := bytes.IndexByte(w.buffer, '\n')
		if index < 0 {
			break
		}
		w.observeLocked(w.buffer[:index])
		w.buffer = w.buffer[index+1:]
		if w.firstErr != nil {
			break
		}
	}
	return len(chunk), nil
}

func (w *lineObserver) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buffer) > 0 && w.firstErr == nil {
		w.observeLocked(w.buffer)
	}
	w.buffer = nil
}

func (w *lineObserver) observeLocked(line []byte) {
	if len(bytes.TrimSpace(line)) == 0 || w.callback == nil {
		return
	}
	copyOfLine := append([]byte(nil), line...)
	if err := w.callback(copyOfLine); err != nil {
		w.firstErr = err
		w.cancel()
	}
}

func (w *lineObserver) err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.firstErr
}
