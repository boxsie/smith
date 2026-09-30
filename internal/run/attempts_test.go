package run

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/runtime"
)

func TestReduceAttemptsTracksMultipleAttemptsForOneTaskInvocation(t *testing.T) {
	now := time.Date(2026, time.September, 3, 16, 0, 0, 0, time.UTC)
	taskInvocationID := "run/i-000001"
	first := testAttemptSpec(taskInvocationID, "task", 1)
	second := testAttemptSpec(taskInvocationID, "task", 2)
	events := []Event{
		testAttemptEvent(now, EventAttemptPending, first, AttemptPending, "attempt_created"),
		testAttemptEvent(now.Add(time.Second), EventAttemptAdmitted, first, AttemptAdmitted, "profile_admitted"),
		testAttemptEvent(now.Add(2*time.Second), EventAttemptStarting, first, AttemptStarting, "launch_requested"),
		testAttemptEvent(now.Add(3*time.Second), EventAttemptRunning, first, AttemptRunning, "runtime_invoked"),
		testAttemptEvent(now.Add(4*time.Second), EventAttemptTerminal, first, AttemptTerminal, runtime.TerminalProtocolFailure),
		testAttemptEvent(now.Add(5*time.Second), EventAttemptPending, second, AttemptPending, "attempt_created"),
		testAttemptEvent(now.Add(6*time.Second), EventAttemptTerminal, second, AttemptTerminal, runtime.TerminalTaskLimit),
	}

	states, err := ReduceAttempts(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0].Spec.TaskInvocationID != taskInvocationID || states[1].Spec.TaskInvocationID != taskInvocationID {
		t.Fatalf("attempt states = %#v", states)
	}
	if states[0].Spec.Ordinal != 1 || states[1].Spec.Ordinal != 2 || states[0].Status != AttemptTerminal || states[1].Status != AttemptTerminal {
		t.Fatalf("attempt identities/statuses = %#v", states)
	}
	if got := states[1].Conditions[len(states[1].Conditions)-1].Reason; got != runtime.TerminalTaskLimit {
		t.Fatalf("terminal reason = %q", got)
	}

	first.Model = "mutated-after-reduce"
	if states[0].Spec.Model != "frontier/test" {
		t.Fatalf("attempt spec changed through caller alias: %#v", states[0].Spec)
	}
}

func TestReduceAttemptsRejectsSpecMutationAndTerminalRegression(t *testing.T) {
	now := time.Date(2026, time.September, 3, 16, 0, 0, 0, time.UTC)
	spec := testAttemptSpec("run/i-000001", "task", 1)
	pending := testAttemptEvent(now, EventAttemptPending, spec, AttemptPending, "attempt_created")
	terminal := testAttemptEvent(now.Add(time.Second), EventAttemptTerminal, spec, AttemptTerminal, runtime.TerminalProtocolFailure)

	mutated := testAttemptEvent(now.Add(time.Second), EventAttemptAdmitted, spec, AttemptAdmitted, "profile_admitted")
	changed := spec
	changed.Model = "other/model"
	mutated.AttemptSpec = &changed
	if _, err := ReduceAttempts([]Event{pending, mutated}); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("spec mutation error = %v", err)
	}

	regressed := testAttemptEvent(now.Add(2*time.Second), EventAttemptRunning, spec, AttemptRunning, "runtime_invoked")
	if _, err := ReduceAttempts([]Event{pending, terminal, regressed}); err == nil || !strings.Contains(err.Error(), "cannot transition") {
		t.Fatalf("terminal regression error = %v", err)
	}
}

func TestReduceAttemptsRecordsGracefulAndHardTerminationStages(t *testing.T) {
	now := time.Date(2026, time.September, 3, 16, 0, 0, 0, time.UTC)
	spec := testAttemptSpec("run/i-000001", "task", 1)
	events := []Event{
		testAttemptEvent(now, EventAttemptPending, spec, AttemptPending, "attempt_created"),
		testAttemptEvent(now.Add(time.Second), EventAttemptAdmitted, spec, AttemptAdmitted, "profile_admitted"),
		testAttemptEvent(now.Add(2*time.Second), EventAttemptStarting, spec, AttemptStarting, "launch_requested"),
		testAttemptEvent(now.Add(3*time.Second), EventAttemptRunning, spec, AttemptRunning, "runtime_invoked"),
		testAttemptEvent(now.Add(4*time.Second), EventAttemptTerminating, spec, AttemptTerminating, "graceful_requested"),
		testAttemptEvent(now.Add(5*time.Second), EventAttemptTerminating, spec, AttemptTerminating, "kill_requested"),
		testAttemptEvent(now.Add(6*time.Second), EventAttemptTerminal, spec, AttemptTerminal, runtime.TerminalDeadlineExceeded),
	}

	states, err := ReduceAttempts(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || len(states[0].Conditions) != len(events) {
		t.Fatalf("attempt states = %#v", states)
	}
	if states[0].Conditions[4].Reason != "graceful_requested" || states[0].Conditions[5].Reason != "kill_requested" {
		t.Fatalf("termination conditions = %#v", states[0].Conditions)
	}
}

func TestAttemptProjectionRebuildsIdenticallyAfterRestart(t *testing.T) {
	runDir := t.TempDir()
	now := time.Date(2026, time.September, 3, 16, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		now = now.Add(time.Second)
		return now
	}
	store, err := NewEventStore(runDir, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(Event{Type: EventRunQueued, RunID: "restart", AppRoot: "/app", TaskIDs: []string{"task"}}); err != nil {
		t.Fatal(err)
	}
	spec := testAttemptSpec("restart/i-000001", "task", 1)
	for _, event := range []Event{
		testAttemptEvent(time.Time{}, EventAttemptPending, spec, AttemptPending, "attempt_created"),
		testAttemptEvent(time.Time{}, EventAttemptAdmitted, spec, AttemptAdmitted, "profile_admitted"),
		testAttemptEvent(time.Time{}, EventAttemptStarting, spec, AttemptStarting, "launch_requested"),
		testAttemptEvent(time.Time{}, EventAttemptRunning, spec, AttemptRunning, "runtime_invoked"),
		testAttemptEvent(time.Time{}, EventAttemptTerminating, spec, AttemptTerminating, "context_done"),
		testAttemptEvent(time.Time{}, EventAttemptTerminal, spec, AttemptTerminal, "cancelled"),
	} {
		event.RunID = "restart"
		if _, err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}

	before, err := ReadAttemptStates(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewEventStore(runDir, clock); err != nil {
		t.Fatalf("reopen event store: %v", err)
	}
	after, err := ReadAttemptStates(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("attempt projection changed across restart\nbefore: %#v\nafter:  %#v", before, after)
	}
	if len(after) != 1 || len(after[0].Conditions) != 6 || after[0].Conditions[5].Reason != "cancelled" || after[0].Conditions[5].TransitionTime.IsZero() {
		t.Fatalf("rebuilt attempt = %#v", after)
	}
}

func TestReduceAttemptsRequiresMonotonicOrdinals(t *testing.T) {
	now := time.Date(2026, time.September, 3, 16, 0, 0, 0, time.UTC)
	first := testAttemptSpec("run/i-000001", "task", 2)
	second := testAttemptSpec("run/i-000001", "task", 1)
	_, err := ReduceAttempts([]Event{
		testAttemptEvent(now, EventAttemptPending, first, AttemptPending, "attempt_created"),
		testAttemptEvent(now.Add(time.Second), EventAttemptPending, second, AttemptPending, "attempt_created"),
	})
	if err == nil || !strings.Contains(err.Error(), "does not increase") {
		t.Fatalf("ordinal error = %v", err)
	}
}

func TestAttemptControllerReusesDurableRetryDecisionAfterRestart(t *testing.T) {
	runDir := t.TempDir()
	now := time.Date(2026, time.September, 3, 17, 0, 0, 0, time.UTC)
	policy, err := runtime.ResolveAttemptPolicy(runtime.AttemptPolicy{
		Restart: runtime.RestartOnFailure, MaxAttempts: 3,
		RetryableReasons: []string{runtime.TerminalHostLoss}, ActiveDeadline: "1m",
		Backoff: runtime.BackoffPolicy{Initial: "5s", Maximum: "10s", Multiplier: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewEventStore(runDir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(Event{Type: EventRunQueued, RunID: "restart", AppRoot: "/app", TaskIDs: []string{"task"}}); err != nil {
		t.Fatal(err)
	}
	spec := testAttemptSpec("restart/i-000001", "task", 1)
	spec.ControllerPolicy = policy
	for _, event := range []Event{
		testAttemptEvent(time.Time{}, EventAttemptPending, spec, AttemptPending, "attempt_created"),
		testAttemptEvent(time.Time{}, EventAttemptAdmitted, spec, AttemptAdmitted, "profile_admitted"),
		testAttemptEvent(time.Time{}, EventAttemptStarting, spec, AttemptStarting, "launch_requested"),
		testAttemptEvent(time.Time{}, EventAttemptRunning, spec, AttemptRunning, "runtime_invoked"),
		testAttemptEvent(time.Time{}, EventAttemptTerminal, spec, AttemptTerminal, runtime.TerminalHostLoss),
	} {
		event.RunID = "restart"
		if _, err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	notBefore := now.Add(5 * time.Second)
	if _, err := store.Append(Event{
		Type: EventAttemptRetry, RunID: "restart", InvocationID: spec.ID,
		AttemptID: spec.ID, AttemptOrdinal: 1,
		AttemptRetry: &AttemptRetry{NextOrdinal: 2, Reason: runtime.TerminalHostLoss, NotBefore: notBefore},
	}); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewEventStore(runDir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	wait, err := ReconcileAttemptController(reopened.AttemptStates(), spec.TaskInvocationID, now)
	if err != nil {
		t.Fatal(err)
	}
	if wait.Action != AttemptControllerWait || wait.Ordinal != 2 || !wait.NotBefore.Equal(notBefore) {
		t.Fatalf("waiting decision = %#v", wait)
	}
	launch, err := ReconcileAttemptController(reopened.AttemptStates(), spec.TaskInvocationID, notBefore)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Action != AttemptControllerLaunchNew || launch.Ordinal != 2 {
		t.Fatalf("launch decision = %#v", launch)
	}
}

func TestAttemptControllerDoesNotDoubleLaunchAmbiguousAttempt(t *testing.T) {
	now := time.Date(2026, time.September, 3, 17, 0, 0, 0, time.UTC)
	spec := testAttemptSpec("run/i-000001", "task", 1)
	states, err := ReduceAttempts([]Event{
		testAttemptEvent(now, EventAttemptPending, spec, AttemptPending, "attempt_created"),
		testAttemptEvent(now, EventAttemptAdmitted, spec, AttemptAdmitted, "profile_admitted"),
		testAttemptEvent(now, EventAttemptStarting, spec, AttemptStarting, "launch_requested"),
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := ReconcileAttemptController(states, spec.TaskInvocationID, now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != AttemptControllerMarkHostLost || decision.Ordinal != 1 {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestAttemptControllerHonorsActiveDeadlineAndAttemptBudget(t *testing.T) {
	now := time.Date(2026, time.September, 3, 17, 0, 0, 0, time.UTC)
	policy, err := runtime.ResolveAttemptPolicy(runtime.AttemptPolicy{
		Restart: runtime.RestartOnFailure, MaxAttempts: 2,
		RetryableReasons: []string{runtime.TerminalLaunchFailure}, ActiveDeadline: "5s",
		Backoff: runtime.BackoffPolicy{Initial: "5s", Maximum: "5s", Multiplier: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := testAttemptSpec("run/i-000001", "task", 1)
	spec.ControllerPolicy = policy
	states, err := ReduceAttempts([]Event{
		testAttemptEvent(now, EventAttemptPending, spec, AttemptPending, "attempt_created"),
		testAttemptEvent(now.Add(time.Second), EventAttemptTerminal, spec, AttemptTerminal, runtime.TerminalLaunchFailure),
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := ReconcileAttemptController(states, spec.TaskInvocationID, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != AttemptControllerFinish || decision.Reason != runtime.TerminalDeadlineExceeded {
		t.Fatalf("deadline decision = %#v", decision)
	}

	first := spec
	firstTerminal := testAttemptEvent(now.Add(time.Second), EventAttemptTerminal, first, AttemptTerminal, runtime.TerminalLaunchFailure)
	second := spec
	second.Ordinal = 2
	second.ID = AttemptID(second.TaskInvocationID, 2)
	retryAt := now.Add(2 * time.Second)
	states, err = ReduceAttempts([]Event{
		testAttemptEvent(now, EventAttemptPending, first, AttemptPending, "attempt_created"),
		firstTerminal,
		{At: retryAt, Type: EventAttemptRetry, RunID: "run", InvocationID: first.ID, AttemptID: first.ID, AttemptOrdinal: 1, AttemptRetry: &AttemptRetry{NextOrdinal: 2, Reason: runtime.TerminalLaunchFailure, NotBefore: retryAt}},
		testAttemptEvent(retryAt, EventAttemptPending, second, AttemptPending, "attempt_created"),
		testAttemptEvent(retryAt.Add(time.Second), EventAttemptTerminal, second, AttemptTerminal, runtime.TerminalLaunchFailure),
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err = ReconcileAttemptController(states, spec.TaskInvocationID, retryAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != AttemptControllerFinish || decision.Reason != runtime.TerminalLaunchFailure {
		t.Fatalf("budget decision = %#v", decision)
	}
}

func testAttemptSpec(taskInvocationID, taskID string, ordinal uint64) AttemptSpec {
	controller, err := runtime.ResolveAttemptPolicy(runtime.AttemptPolicy{})
	if err != nil {
		panic(err)
	}
	return AttemptSpec{
		ID:                AttemptID(taskInvocationID, ordinal),
		Ordinal:           ordinal,
		TaskInvocationID:  taskInvocationID,
		TaskID:            taskID,
		Runtime:           "fake",
		Model:             "frontier/test",
		InputSHA256:       "input-hash",
		ContextSHA256:     "context-hash",
		CapabilityProfile: runtime.CapabilityPolicy{Profile: runtime.CapabilityInspect, Allow: []string{"workspace.inspect"}},
		ContainmentProfile: runtime.ContainmentAdmission{
			RequestedProfile: runtime.ExecutionProfileLocalSubscription,
			Mechanism:        runtime.ContainmentSystemdUser,
			EffectiveLimits:  runtime.LimitPolicy{Timeout: "5m", MaxMemoryBytes: 4 << 30, MaxProcesses: 256},
			Enforced:         true,
		},
		WorkspaceAuthority: runtime.WorkspacePolicy{Root: "/workspace", Access: runtime.WorkspaceReadOnly, Granted: true},
		ControllerPolicy:   controller,
	}
}

func testAttemptEvent(at time.Time, eventType string, spec AttemptSpec, status AttemptStatus, reason string) Event {
	event := Event{
		At:             at,
		Type:           eventType,
		RunID:          "run",
		InvocationID:   spec.ID,
		TaskID:         spec.TaskID,
		AttemptID:      spec.ID,
		AttemptOrdinal: spec.Ordinal,
		AttemptCondition: &AttemptCondition{
			Status:         status,
			Reason:         reason,
			Message:        "test transition",
			TransitionTime: at,
		},
	}
	if eventType == EventAttemptPending {
		copy := spec
		event.AttemptSpec = &copy
	}
	return event
}
