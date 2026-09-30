package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/plan"
	"github.com/boxsie/smith/internal/proposal"
	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/tools"
	"github.com/boxsie/smith/internal/validate"
)

type blockingProvider struct {
	started chan struct{}
	once    sync.Once
}

type serviceExternalRuntime struct{}

type failingReleaseLease struct {
	runLease
	err      error
	releases *int
}

func (l *failingReleaseLease) Release() error {
	(*l.releases)++
	innerErr := l.runLease.Release()
	return errors.Join(innerErr, l.err)
}

func injectReleaseFailure(t *testing.T, svc *Service, injected error) *int {
	t.Helper()
	releases := 0
	svc.acquireRunLease = func(runDir string) (runLease, error) {
		lease, err := run.AcquireRunLease(runDir)
		if err != nil {
			return nil, err
		}
		return &failingReleaseLease{runLease: lease, err: injected, releases: &releases}, nil
	}
	return &releases
}

func (serviceExternalRuntime) Invoke(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
	return sink.Complete(ctx, &runtime.ExternalResult{
		Text: "external result",
		Provenance: runtime.Provenance{
			Adapter:         invocation.Runtime,
			AdapterVersion:  "1",
			ProtocolVersion: "fake-json/1",
			RequestedModel:  invocation.Model,
			CanonicalModel:  "canonical-model",
			SessionID:       "service-session",
			BillingBasis:    runtime.BillingSubscription,
		},
	})
}

func (serviceExternalRuntime) Admit(_ context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
	return runtime.ContainmentAdmission{RequestedProfile: request.Profile, Mechanism: "test_containment", EffectiveLimits: request.Limits, Enforced: true}, nil
}

func (p *blockingProvider) Execute(ctx context.Context, _ *runtime.Request) (*runtime.Response, error) {
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestNewIsInert(t *testing.T) {
	called := false
	mark := func() { called = true }

	_ = New(Dependencies{
		RegistryFactory: func() *tools.Registry { mark(); return tools.NewRegistry() },
		Clock:           func() time.Time { mark(); return time.Time{} },
		NewRunID:        func() string { mark(); return "run" },
		TrackProject:    func(string) error { mark(); return nil },
		Events: EventSinkFunc(func(context.Context, Event) {
			mark()
		}),
		Validate: func(string, *runtime.Factory, *runtime.ExternalFactory) *validate.Result {
			mark()
			return &validate.Result{}
		},
		Plan: func(context.Context, plan.PlanInput) (*plan.PlanResult, error) {
			mark()
			return nil, nil
		},
		Apply: func(proposal.ApplyInput) (*proposal.ApplyResult, error) {
			mark()
			return nil, nil
		},
	})

	if called {
		t.Fatal("constructing the service must not invoke dependencies")
	}
}

func TestValidateUsesInjectedFactory(t *testing.T) {
	root := writeTestApp(t, "custom/model")
	factory := &runtime.Factory{Override: func(model string) (runtime.Provider, error) {
		if model != "custom/model" {
			t.Fatalf("resolved model = %q, want custom/model", model)
		}
		return &runtime.MockProvider{Default: "ok"}, nil
	}}

	result, err := New(Dependencies{Factory: factory}).Validate(root)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if result.AppRoot != root {
		t.Fatalf("AppRoot = %q, want %q", result.AppRoot, root)
	}
	if result.Validation.Root == nil || result.Validation.Graph == nil {
		t.Fatal("successful validation must return the loaded tree and graph")
	}
}

func TestPrepareRunExecuteOwnsLifecycle(t *testing.T) {
	root := writeTestApp(t, "mock/service")
	fixedTime := time.Date(2026, time.September, 1, 20, 0, 0, 0, time.UTC)
	trackErr := errors.New("tracking unavailable")
	var events []Event

	svc := New(Dependencies{
		Clock:        func() time.Time { return fixedTime },
		NewRunID:     func() string { return "20260901-200000.000000000-service1" },
		TrackProject: func(path string) error { return trackErr },
		Events: EventSinkFunc(func(_ context.Context, event Event) {
			events = append(events, event)
		}),
	})

	prepared, err := svc.PrepareRun(PrepareRunRequest{AppRoot: root})
	if err != nil {
		t.Fatalf("PrepareRun() error = %v", err)
	}
	if prepared.Scope[tools.ScopeRoot] != root {
		t.Fatalf("root scope = %q, want %q", prepared.Scope[tools.ScopeRoot], root)
	}
	if got := prepared.RunID(); got != "20260901-200000.000000000-service1" {
		t.Fatalf("RunID() = %q", got)
	}

	result, err := prepared.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Execution.Success {
		t.Fatalf("execution failed: %+v", result.Execution.Tasks)
	}
	if result.OutputType != "markdown" || result.Output != "mock response for service" {
		t.Fatalf("task result = (%q, %q), want markdown mock response", result.OutputType, result.Output)
	}
	if len(result.Warnings) != 1 || !errors.Is(result.Warnings[0], trackErr) {
		t.Fatalf("warnings = %v, want tracking error", result.Warnings)
	}

	manifest, err := run.ReadManifest(run.ManifestPath(result.RunDir))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.Status != "success" || manifest.RunID != result.RunID {
		t.Fatalf("manifest = %+v", manifest)
	}
	if len(events) != 2 || events[0].State != "started" || events[1].State != "completed" {
		t.Fatalf("events = %+v", events)
	}
	for _, event := range events {
		if !event.At.Equal(fixedTime) || event.RunID != result.RunID {
			t.Fatalf("event = %+v", event)
		}
	}
	if _, err := prepared.Execute(context.Background(), nil); err == nil {
		t.Fatal("executing a prepared run twice must fail")
	}
}

func TestExecuteSurfacesLeaseReleaseFailureWithoutChangingTerminalEvent(t *testing.T) {
	root := writeTestApp(t, "mock/release")
	injected := errors.New("injected release failure")
	svc := New(Dependencies{})
	releases := injectReleaseFailure(t, svc, injected)

	prepared, err := svc.PrepareRun(PrepareRunRequest{AppRoot: root, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := prepared.Execute(context.Background(), nil)
	if !errors.Is(err, injected) {
		t.Fatalf("Execute() error = %v, want injected release error", err)
	}
	if result != nil {
		t.Fatalf("Execute() result = %#v, want nil when WaitRun reports an error", result)
	}
	if *releases != 1 {
		t.Fatalf("Release() calls = %d, want 1", *releases)
	}
	manifest, readErr := run.ReadManifest(run.ManifestPath(run.RunDir(root, prepared.RunID())))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if manifest.Status != "success" {
		t.Fatalf("terminal manifest status = %q, want success", manifest.Status)
	}
	lease, acquireErr := run.AcquireRunLease(run.RunDir(root, prepared.RunID()))
	if acquireErr != nil {
		t.Fatalf("owner.lock remains held: %v", acquireErr)
	}
	if releaseErr := lease.Release(); releaseErr != nil {
		t.Fatal(releaseErr)
	}
}

func TestExecuteJoinsExecutionAndLeaseReleaseFailures(t *testing.T) {
	root := writeTestApp(t, "custom/blocking-release")
	releaseErr := errors.New("injected release failure")
	provider := &blockingProvider{started: make(chan struct{})}
	svc := New(Dependencies{Factory: &runtime.Factory{Override: func(string) (runtime.Provider, error) {
		return provider, nil
	}}})
	releases := injectReleaseFailure(t, svc, releaseErr)

	prepared, err := svc.PrepareRun(PrepareRunRequest{AppRoot: root, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := prepared.Start(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not start")
	}
	if requested, err := svc.CancelRun(root, handle.RunID); err != nil || !requested {
		t.Fatalf("CancelRun() = (%v, %v), want newly requested cancellation", requested, err)
	}
	_, err = svc.WaitRun(context.Background(), root, handle.RunID)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, releaseErr) {
		t.Fatalf("WaitRun() error = %v, want cancellation and release errors", err)
	}
	if *releases != 1 {
		t.Fatalf("Release() calls = %d, want 1", *releases)
	}
}

func TestReconcileSurfacesAndJoinsLeaseReleaseFailure(t *testing.T) {
	root := t.TempDir()
	runID := "20260903-120000.000000000-release1"
	runDir := run.RunDir(root, runID)
	store, err := run.NewEventStore(runDir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []run.Event{
		{Type: run.EventRunQueued, RunID: runID, AppRoot: root},
		{Type: run.EventRunStarted, RunID: runID},
		{Type: run.EventRunCompleted, RunID: runID},
	} {
		if _, err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}

	injected := errors.New("injected release failure")
	svc := New(Dependencies{})
	releases := injectReleaseFailure(t, svc, injected)
	manifest, err := svc.reconcileRun(runDir)
	if !errors.Is(err, injected) || manifest.Status != "success" {
		t.Fatalf("reconcileRun() = (%+v, %v), want success manifest and release error", manifest, err)
	}
	if *releases != 1 {
		t.Fatalf("Release() calls = %d, want 1", *releases)
	}

	// Corrupt the event stream to force a deterministic reconciliation error
	// while preserving the release failure.
	if err := os.WriteFile(run.EventsPath(runDir), []byte("not-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = svc.reconcileRun(runDir)
	if !errors.Is(err, injected) {
		t.Fatalf("joined error = %v, want release error", err)
	}
	if err == nil || !strings.Contains(err.Error(), "decode run event") {
		t.Fatalf("joined error = %v, want reconciliation error first", err)
	}
	if *releases != 2 {
		t.Fatalf("Release() calls = %d, want 2 across two reconciliations", *releases)
	}
}

func TestServiceSelectsInjectedExternalRuntime(t *testing.T) {
	root := writeTestApp(t, "frontier/requested")
	if err := os.WriteFile(filepath.Join(root, "agent.md"), []byte("runtime: fake\nmodel: frontier/requested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := New(Dependencies{ExternalFactory: &runtime.ExternalFactory{
		Runtimes: map[string]runtime.ExternalRuntime{"fake": serviceExternalRuntime{}},
	}})
	prepared, err := svc.PrepareRun(PrepareRunRequest{AppRoot: root, NoCache: true})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	result, err := prepared.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Output != "external result" || !result.Execution.Success {
		t.Fatalf("result = %#v", result)
	}
	metrics := result.Execution.Tasks[0].Metrics
	if metrics.Runtime != "fake" || metrics.Model != "canonical-model" || metrics.SessionID != "service-session" || metrics.BillingBasis != "subscription" {
		t.Fatalf("metrics = %#v", metrics)
	}
	page, err := svc.ReadRunEvents(root, result.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range page.Events {
		if event.Type == run.EventRuntimeCompleted && event.SessionID == "service-session" {
			found = true
		}
	}
	if !found {
		t.Fatalf("runtime completion not found in events: %#v", page.Events)
	}
	manifest, err := svc.Status(root, result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tasks[0].RuntimeRecords) != 1 || manifest.Tasks[0].RuntimeRecords[0].ProtocolVersion == nil ||
		*manifest.Tasks[0].RuntimeRecords[0].ProtocolVersion != "fake-json/1" {
		t.Fatalf("runtime history = %#v", manifest.Tasks[0].RuntimeRecords)
	}
}

func TestStartReturnsBeforeExecutionAndCancellationIsDurable(t *testing.T) {
	root := writeTestApp(t, "custom/blocking")
	provider := &blockingProvider{started: make(chan struct{})}
	svc := New(Dependencies{Factory: &runtime.Factory{Override: func(string) (runtime.Provider, error) {
		return provider, nil
	}}})
	prepared, err := svc.PrepareRun(PrepareRunRequest{AppRoot: root, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}

	handle, err := prepared.Start(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	manifest, err := svc.Status(root, handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != "running" {
		t.Fatalf("status = %q, want running", manifest.Status)
	}
	observer := New(Dependencies{})
	manifest, err = observer.Status(root, handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != "running" {
		t.Fatalf("cross-process-style status = %q, want running owner to remain healthy", manifest.Status)
	}

	requested, err := svc.CancelRun(root, handle.RunID)
	if err != nil || !requested {
		t.Fatalf("first cancel = (%v, %v), want (true, nil)", requested, err)
	}
	requested, err = svc.CancelRun(root, handle.RunID)
	if err != nil || requested {
		t.Fatalf("second cancel = (%v, %v), want (false, nil)", requested, err)
	}
	_, err = svc.WaitRun(context.Background(), root, handle.RunID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitRun() error = %v, want context canceled", err)
	}
	manifest, err = svc.Status(root, handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", manifest.Status)
	}

	page, err := svc.ReadRunEvents(root, handle.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var cancelRequested, cancelled bool
	for i, event := range page.Events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("sequence[%d] = %d", i, event.Sequence)
		}
		cancelRequested = cancelRequested || event.Type == run.EventRunCancelRequested
		cancelled = cancelled || event.Type == run.EventRunCancelled
	}
	if !cancelRequested || !cancelled {
		t.Fatalf("cancellation events missing: %+v", page.Events)
	}
}

func TestRestartReconcilesAbandonedRunButPreservesCompletedHistory(t *testing.T) {
	root := t.TempDir()
	clock := func() time.Time { return time.Date(2026, time.September, 2, 8, 30, 0, 0, time.UTC) }

	abandonedID := "20260902-083000.000000000-abandon1"
	abandonedStore, err := run.NewEventStore(run.RunDir(root, abandonedID), clock)
	if err != nil {
		t.Fatal(err)
	}
	controllerPolicy, err := runtime.ResolveAttemptPolicy(runtime.AttemptPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	attemptID := run.AttemptID(abandonedID+"/i-000001", 1)
	attemptSpec := run.AttemptSpec{
		ID: attemptID, Ordinal: 1, TaskInvocationID: abandonedID + "/i-000001",
		Runtime: "fake", Model: "frontier", InputSHA256: "input",
		ContainmentProfile: runtime.ContainmentAdmission{RequestedProfile: runtime.ExecutionProfileLocalSubscription},
		ControllerPolicy:   controllerPolicy,
	}
	condition := func(status run.AttemptStatus, reason string) *run.AttemptCondition {
		return &run.AttemptCondition{Status: status, Reason: reason}
	}
	for _, event := range []run.Event{
		{Type: run.EventRunQueued, RunID: abandonedID, AppRoot: root, TaskIDs: []string{""}},
		{Type: run.EventRunStarted, RunID: abandonedID},
		{Type: run.EventAttemptPending, RunID: abandonedID, InvocationID: attemptID, ParentInvocationID: attemptSpec.TaskInvocationID, AttemptID: attemptID, AttemptOrdinal: 1, AttemptSpec: &attemptSpec, AttemptCondition: condition(run.AttemptPending, "attempt_created")},
		{Type: run.EventAttemptAdmitted, RunID: abandonedID, InvocationID: attemptID, ParentInvocationID: attemptSpec.TaskInvocationID, AttemptID: attemptID, AttemptOrdinal: 1, AttemptCondition: condition(run.AttemptAdmitted, "profile_admitted")},
		{Type: run.EventAttemptStarting, RunID: abandonedID, InvocationID: attemptID, ParentInvocationID: attemptSpec.TaskInvocationID, AttemptID: attemptID, AttemptOrdinal: 1, AttemptCondition: condition(run.AttemptStarting, "launch_requested")},
		{Type: run.EventAttemptRunning, RunID: abandonedID, InvocationID: attemptID, ParentInvocationID: attemptSpec.TaskInvocationID, AttemptID: attemptID, AttemptOrdinal: 1, AttemptCondition: condition(run.AttemptRunning, "runtime_invoked")},
	} {
		if _, err := abandonedStore.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	torn, err := os.OpenFile(run.EventsPath(run.RunDir(root, abandonedID)), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := torn.WriteString(`{"version":1,"sequence":7`); err != nil {
		t.Fatal(err)
	}
	if err := torn.Close(); err != nil {
		t.Fatal(err)
	}

	completedID := "20260902-082900.000000000-complete1"
	completedStore, err := run.NewEventStore(run.RunDir(root, completedID), clock)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []run.Event{
		{Type: run.EventRunQueued, RunID: completedID, AppRoot: root, TaskIDs: []string{""}},
		{Type: run.EventRunStarted, RunID: completedID},
		{Type: run.EventRunCompleted, RunID: completedID},
	} {
		if _, err := completedStore.Append(event); err != nil {
			t.Fatal(err)
		}
	}

	restarted := New(Dependencies{Clock: clock})
	abandoned, err := restarted.Status(root, abandonedID)
	if err != nil {
		t.Fatal(err)
	}
	if abandoned.Status != "interrupted" {
		t.Fatalf("abandoned status = %q", abandoned.Status)
	}
	completed, err := restarted.Status(root, completedID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "success" {
		t.Fatalf("completed status = %q", completed.Status)
	}
	if err := os.Remove(run.ManifestPath(run.RunDir(root, completedID))); err != nil {
		t.Fatal(err)
	}
	listed, err := restarted.ListRuns(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].RunID != abandonedID || listed[1].RunID != completedID {
		t.Fatalf("event-backed runs after projection loss = %+v", listed)
	}
	page, err := restarted.ReadRunEvents(root, abandonedID, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 6 || page.Events[4].Type != run.EventAttemptTerminal ||
		page.Events[4].AttemptCondition.Reason != runtime.TerminalHostLoss || page.Events[5].Type != run.EventRunInterrupted {
		t.Fatalf("recovery events = %+v", page.Events)
	}
	attempts, err := restarted.ReadRunAttempts(root, abandonedID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts.Attempts) != 1 || attempts.Attempts[0].TerminalReason == nil || *attempts.Attempts[0].TerminalReason != runtime.TerminalHostLoss ||
		attempts.Attempts[0].Process != nil || attempts.Attempts[0].Measurements.PeakMemoryBytes != nil || attempts.Attempts[0].Measurements.UnavailableReason == "" {
		t.Fatalf("recovered attempt inspection = %#v", attempts)
	}
}

func TestSuccessfulRunRecordsProviderUsageAndArtifact(t *testing.T) {
	root := writeTestApp(t, "mock/events")
	svc := New(Dependencies{})
	prepared, err := svc.PrepareRun(PrepareRunRequest{AppRoot: root, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := prepared.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	page, err := svc.ReadRunEvents(root, result.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if page.Events[0].Type != run.EventRunQueued || page.Events[len(page.Events)-1].Type != run.EventRunCompleted {
		t.Fatalf("terminal event shape = %+v", page.Events)
	}
	var invocationID string
	var providerUsage, artifact bool
	for _, event := range page.Events {
		if event.Type == run.EventInvocationQueued {
			invocationID = event.InvocationID
		}
		providerUsage = providerUsage || event.Type == run.EventProviderCompleted
		artifact = artifact || event.Type == run.EventArtifactPublished
	}
	if invocationID == "" || !providerUsage || !artifact {
		t.Fatalf("missing durable detail: invocation=%q provider=%v artifact=%v", invocationID, providerUsage, artifact)
	}
}

func TestParallelServiceRunHasUniqueOrderedInvocationIDs(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"task.md":               "Run the children.",
		"agent.md":              "model: mock/parallel\n",
		"subtasks/01-a/task.md": "Child A.",
		"subtasks/01-b/task.md": "Child B.",
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(Dependencies{})
	prepared, err := svc.PrepareRun(PrepareRunRequest{AppRoot: root, NoCache: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := prepared.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	page, err := svc.ReadRunEvents(root, result.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for i, event := range page.Events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("event[%d] sequence = %d", i, event.Sequence)
		}
		if event.Type == run.EventInvocationQueued {
			if seen[event.InvocationID] {
				t.Fatalf("duplicate invocation ID %q", event.InvocationID)
			}
			seen[event.InvocationID] = true
		}
	}
	if len(seen) != 3 {
		t.Fatalf("invocation IDs = %v, want three task invocations", seen)
	}
}

func TestExecuteReturnsValidatedJSONTaskResult(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "task.md"), []byte("---\noutput:\n  type: json\n---\nReturn structured JSON.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "schema.md"), []byte(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"],"additionalProperties":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agent.md"), []byte("model: mock/json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	factory := &runtime.Factory{Override: func(string) (runtime.Provider, error) {
		return &runtime.MockProvider{Default: `{"answer":42}`}, nil
	}}
	prepared, err := New(Dependencies{Factory: factory}).PrepareRun(PrepareRunRequest{AppRoot: root})
	if err != nil {
		t.Fatalf("PrepareRun() error = %v", err)
	}
	result, err := prepared.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.OutputType != "json" || result.Output != "{\n  \"answer\": 42\n}\n" {
		t.Fatalf("task result = (%q, %q)", result.OutputType, result.Output)
	}
}

func TestPlanPassesDependenciesAndReturnsTrackingWarning(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target")
	trackErr := errors.New("index is read-only")
	factory := &runtime.Factory{}
	var got plan.PlanInput

	svc := New(Dependencies{
		Factory: factory,
		Plan: func(_ context.Context, input plan.PlanInput) (*plan.PlanResult, error) {
			got = input
			return &plan.PlanResult{
				ProposalDir: filepath.Join(target, ".smith", "proposals", "test"),
				Proposal:    &proposal.Proposal{Summary: "planned"},
			}, nil
		},
		TrackProject: func(string) error { return trackErr },
	})

	result, err := svc.Plan(context.Background(), PlanRequest{
		TargetDir:     target,
		Goal:          "build it",
		ModelOverride: "mock/planner",
		MaxCostUSD:    2.5,
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got.TargetDir != target || got.Goal != "build it" || got.Factory != factory {
		t.Fatalf("plan input = %+v", got)
	}
	if len(result.Warnings) != 1 || !errors.Is(result.Warnings[0], trackErr) {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

func TestApplyResolvesProposalTargetWithoutCobra(t *testing.T) {
	root := t.TempDir()
	proposalDir := filepath.Join(root, ".smith", "proposals", "test")
	if err := os.MkdirAll(proposalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proposalDir, "manifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	var got proposal.ApplyInput
	svc := New(Dependencies{Apply: func(input proposal.ApplyInput) (*proposal.ApplyResult, error) {
		got = input
		return &proposal.ApplyResult{}, nil
	}})
	result, err := svc.Apply(context.Background(), ApplyRequest{ProposalDir: proposalDir, DryRun: true})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.TargetDir != root || got.TargetDir != root || !got.DryRun {
		t.Fatalf("result = %+v, input = %+v", result, got)
	}
}

func TestStatusAndListRunsUseServiceBoundary(t *testing.T) {
	root := t.TempDir()
	runID := "20260901-200000.000000000-history1"
	runDir := run.RunDir(root, runID)
	manifest := run.NewManifest(runID, root, false, nil, []string{""})
	if err := run.WriteManifest(run.ManifestPath(runDir), manifest); err != nil {
		t.Fatal(err)
	}

	svc := New(Dependencies{})
	got, err := svc.Status(root, runID)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.RunID != runID {
		t.Fatalf("RunID = %q, want %q", got.RunID, runID)
	}
	all, err := svc.ListRuns(root)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(all) != 1 || all[0].RunID != runID {
		t.Fatalf("runs = %+v", all)
	}
}

func TestWithDefaultRootScope(t *testing.T) {
	original := map[string]string{tools.ScopeProposalID: "abc123"}
	got := withDefaultRootScope(original, "/tmp/app")
	if got[tools.ScopeRoot] != "/tmp/app" || got[tools.ScopeProposalID] != "abc123" {
		t.Fatalf("scope = %v", got)
	}
	if _, ok := original[tools.ScopeRoot]; ok {
		t.Fatal("original scope map should not be mutated")
	}

	explicit := withDefaultRootScope(map[string]string{tools.ScopeRoot: "/custom"}, "/tmp/app")
	if explicit[tools.ScopeRoot] != "/custom" {
		t.Fatalf("explicit root = %q", explicit[tools.ScopeRoot])
	}
}

func writeTestApp(t *testing.T, model string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "task.md"), []byte("Return a service result.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agent.md"), []byte("model: "+model+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
