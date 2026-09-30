package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
	"gopkg.in/yaml.v3"
)

type retryingPatchRuntime struct {
	mu    sync.Mutex
	calls int
}

type protocolFailingPatchRuntime struct {
	mu    sync.Mutex
	calls int
}

type fixedPatchRuntime struct {
	payload json.RawMessage
}

func TestStableGateRequestIDSurvivesInvocationRetry(t *testing.T) {
	trigger := patchrun.Envelope{ID: "run/e-000004"}
	first := patchrun.Invocation{ID: "run/i-000005", Trigger: trigger}
	recovered := patchrun.Invocation{ID: "run/i-000006", Trigger: trigger}
	if got, want := stableGateRequestID(first), trigger.ID; got != want {
		t.Fatalf("first request id = %q, want %q", got, want)
	}
	if got, want := stableGateRequestID(recovered), trigger.ID; got != want {
		t.Fatalf("recovered request id = %q, want %q", got, want)
	}
}

func (r *fixedPatchRuntime) Admit(_ context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
	return runtime.ContainmentAdmission{RequestedProfile: request.Profile, Mechanism: "test_containment", EffectiveLimits: request.Limits, Enforced: true}, nil
}

func (r *fixedPatchRuntime) Invoke(ctx context.Context, _ runtime.Invocation, sink runtime.InvocationSink) error {
	return sink.Complete(ctx, &runtime.ExternalResult{
		JSON:       r.payload,
		Provenance: runtime.Provenance{Adapter: "fixture", RequestedModel: "frontier"},
	})
}

func TestPatchRuntimeMergeInputPreservesBatonAndOverlaysResult(t *testing.T) {
	body := &fixedPatchRuntime{payload: json.RawMessage(`{"ticket_id":"ticket-42","accepted":true,"review_feedback":"accepted"}`)}
	svc := New(Dependencies{ExternalFactory: &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{"fixture": body}}})
	node := patch.Node{
		ID: "review", Kind: patch.NodeRuntime,
		Runtime: &patch.RuntimeReference{Runtime: "fixture", Model: "frontier", Profile: runtime.CapabilityReason},
		Config:  map[string]any{"prompt": "review", "merge_input": true},
		Outlets: []patch.Port{{ID: "baton", Kind: patch.EnvelopeMessage, Schema: map[string]any{"type": "object"}}},
	}
	emissions, err := svc.PatchNodeRunner().Run(context.Background(), patchrun.Invocation{
		RunID: "run", ID: "invocation-1", PatchRoot: t.TempDir(), Node: node,
		Trigger: patchrun.Envelope{PortID: "baton"},
		Inputs: map[string]json.RawMessage{
			"baton": json.RawMessage(`{"ticket_id":"ticket-42","accepted":false,"check_run":{"schema":"smith.command_checks/1"}}`),
		},
		Report: func(patchrun.NodeEvent) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(emissions) != 1 {
		t.Fatalf("emissions = %#v", emissions)
	}
	var got struct {
		TicketID       string `json:"ticket_id"`
		Accepted       bool   `json:"accepted"`
		ReviewFeedback string `json:"review_feedback"`
		CheckRun       struct {
			Schema string `json:"schema"`
		} `json:"check_run"`
	}
	if err := json.Unmarshal(emissions[0].Envelope.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.TicketID != "ticket-42" || !got.Accepted || got.ReviewFeedback != "accepted" || got.CheckRun.Schema != "smith.command_checks/1" {
		t.Fatalf("merged baton = %#v", got)
	}
}

func TestMergeRuntimeObjectInputRejectsNonObjects(t *testing.T) {
	for _, test := range []struct {
		name   string
		input  json.RawMessage
		output json.RawMessage
	}{
		{name: "array input", input: json.RawMessage(`[]`), output: json.RawMessage(`{"accepted":true}`)},
		{name: "null input", input: json.RawMessage(`null`), output: json.RawMessage(`{"accepted":true}`)},
		{name: "array output", input: json.RawMessage(`{"ticket_id":"ticket-42"}`), output: json.RawMessage(`[]`)},
		{name: "null output", input: json.RawMessage(`{"ticket_id":"ticket-42"}`), output: json.RawMessage(`null`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := mergeRuntimeObjectInput(test.input, test.output); err == nil {
				t.Fatal("expected non-object merge to fail")
			}
		})
	}
}

func (r *retryingPatchRuntime) Admit(_ context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
	return runtime.ContainmentAdmission{RequestedProfile: request.Profile, Mechanism: "test_containment", EffectiveLimits: request.Limits, Enforced: true}, nil
}

func (r *retryingPatchRuntime) Invoke(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	if call == 1 {
		return &runtime.ProcessLaunchError{Executable: "fixture", Err: errors.New("temporary host fault")}
	}
	return sink.Complete(ctx, &runtime.ExternalResult{
		JSON:       json.RawMessage(`{"ok":true}`),
		Provenance: runtime.Provenance{Adapter: "fixture", RequestedModel: invocation.Model},
	})
}

func (r *protocolFailingPatchRuntime) Admit(_ context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
	return runtime.ContainmentAdmission{RequestedProfile: request.Profile, Mechanism: "test_containment", EffectiveLimits: request.Limits, Enforced: true}, nil
}

func (r *protocolFailingPatchRuntime) Invoke(context.Context, runtime.Invocation, runtime.InvocationSink) error {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return runtime.ErrCodexProtocol
}

func TestPatchRuntimeUsesExplicitFiniteAttemptPolicy(t *testing.T) {
	body := &retryingPatchRuntime{}
	svc := New(Dependencies{ExternalFactory: &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{"fixture": body}}})
	node := patch.Node{
		ID: "worker", Kind: patch.NodeRuntime,
		Runtime: &patch.RuntimeReference{Runtime: "fixture", Model: "frontier", Profile: runtime.CapabilityReason},
		Config: map[string]any{
			"prompt": "try safely",
			"attempts": map[string]any{
				"restart": runtime.RestartOnFailure, "max_attempts": 2,
				"retryable_reasons": []any{runtime.TerminalLaunchFailure}, "active_deadline": "1s",
				"backoff": map[string]any{"initial": "1ms", "maximum": "1ms", "multiplier": 2},
			},
		},
		Outlets: []patch.Port{{ID: "output", Kind: patch.EnvelopeMessage, Schema: map[string]any{"type": "object"}}},
	}
	var events []patchrun.NodeEvent
	emissions, err := svc.PatchNodeRunner().Run(context.Background(), patchrun.Invocation{
		RunID: "run", ID: "invocation-1", PatchRoot: t.TempDir(), Node: node,
		Report: func(event patchrun.NodeEvent) error { events = append(events, event); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if body.calls != 2 || len(emissions) != 1 {
		t.Fatalf("calls=%d emissions=%#v", body.calls, emissions)
	}
	var terminalReasons []string
	retries := 0
	for _, event := range events {
		if event.Type == patchrun.EventAttemptTerminal {
			terminalReasons = append(terminalReasons, event.Reason)
		}
		if event.Type == patchrun.EventAttemptRetry {
			retries++
		}
	}
	if retries != 1 || len(terminalReasons) != 2 || terminalReasons[0] != runtime.TerminalLaunchFailure || terminalReasons[1] != runtime.TerminalSuccess {
		t.Fatalf("retry events=%d terminal reasons=%v events=%#v", retries, terminalReasons, events)
	}
}

func TestPatchRuntimeExhaustsBoundedProtocolRetry(t *testing.T) {
	body := &protocolFailingPatchRuntime{}
	svc := New(Dependencies{ExternalFactory: &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{"fixture": body}}})
	node := patch.Node{
		ID: "worker", Kind: patch.NodeRuntime,
		Runtime: &patch.RuntimeReference{Runtime: "fixture", Model: "frontier", Profile: runtime.CapabilityReason},
		Config: map[string]any{
			"prompt": "return structured output",
			"attempts": map[string]any{
				"restart": runtime.RestartOnFailure, "max_attempts": 2,
				"retryable_reasons": []any{runtime.TerminalProtocolFailure}, "active_deadline": "1s",
				"backoff": map[string]any{"initial": "1ms", "maximum": "1ms", "multiplier": 1},
			},
		},
		Outlets: []patch.Port{{ID: "output", Kind: patch.EnvelopeMessage, Schema: map[string]any{"type": "object"}}},
	}
	var events []patchrun.NodeEvent
	_, err := svc.PatchNodeRunner().Run(context.Background(), patchrun.Invocation{
		RunID: "run", ID: "invocation-1", PatchRoot: t.TempDir(), Node: node,
		Report: func(event patchrun.NodeEvent) error { events = append(events, event); return nil },
	})
	if !errors.Is(err, runtime.ErrCodexProtocol) || body.calls != 2 {
		t.Fatalf("protocol exhaustion = err %v calls %d", err, body.calls)
	}
	terminals, retries := 0, 0
	for _, event := range events {
		if event.Type == patchrun.EventAttemptTerminal && event.Reason == runtime.TerminalProtocolFailure {
			terminals++
		}
		if event.Type == patchrun.EventAttemptRetry {
			retries++
		}
	}
	if terminals != 2 || retries != 1 {
		t.Fatalf("protocol events = terminals %d retries %d", terminals, retries)
	}
}

func TestPatchNodeRunnerExecutesSubpatchUnderSchedulerInvocation(t *testing.T) {
	patchRoot := t.TempDir()
	appRoot := filepath.Join(patchRoot, "flow")
	writeSubpatchFile(t, filepath.Join(appRoot, "task.md"), "do the work")
	writeSubpatchFile(t, filepath.Join(appRoot, "agent.md"), "model: custom/live-subpatch\n")
	document := patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{{
		ID: "flow", Kind: patch.NodeSubpatch, Subpatch: &patch.SubpatchReference{Path: "flow"},
		Inlets:  []patch.Port{{ID: "trigger", Kind: patch.EnvelopeBang}},
		Outlets: []patch.Port{{ID: "output", Kind: patch.EnvelopeMessage, Schema: map[string]any{"type": "string"}}},
	}}}
	data, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeSubpatchFile(t, filepath.Join(patchRoot, patch.FileName), string(data))

	provider := &countingSubpatchProvider{}
	svc := New(Dependencies{
		Factory:      &runtime.Factory{Override: func(string) (runtime.Provider, error) { return provider, nil }},
		TrackProject: func(string) error { return nil },
	})
	scheduler, err := (patchrun.Engine{
		Runner: svc.PatchNodeRunner(), NewRunID: func() string { return "live-subpatch-run" },
	}).Start(context.Background(), patchRoot, patchrun.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Bang("flow", "trigger"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := scheduler.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls.Load())
	}
	patchEvents, err := scheduler.Events(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var outerInvocation string
	for _, event := range patchEvents.Events {
		if event.Type == patchrun.EventInvocationStarted {
			outerInvocation = event.InvocationID
		}
	}
	if outerInvocation == "" {
		t.Fatal("patch invocation was not recorded")
	}
	runs, err := svc.ListRuns(appRoot)
	if err != nil || len(runs) != 1 {
		t.Fatalf("subpatch runs = %#v, err = %v", runs, err)
	}
	inner, err := svc.ReadRunEvents(appRoot, runs[0].RunID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	linked := false
	for _, event := range inner.Events {
		if event.Type == run.EventInvocationStarted && event.ParentInvocationID == outerInvocation {
			linked = true
		}
	}
	if !linked {
		t.Fatalf("subpatch history is not linked to %q: %#v", outerInvocation, inner.Events)
	}
	if err := scheduler.Drain(); err != nil {
		t.Fatal(err)
	}
	if state, err := scheduler.Wait(ctx); err != nil || state.Status != patchrun.StatusCompleted {
		t.Fatalf("patch terminal state = %#v, err = %v", state, err)
	}
}

func TestPatchNodeRunnerDrainsRemovedSubpatchFromFrozenTopology(t *testing.T) {
	patchRoot := t.TempDir()
	appRoot := filepath.Join(patchRoot, "flow")
	writeSubpatchFile(t, filepath.Join(appRoot, "task.md"), "do the work")
	writeSubpatchFile(t, filepath.Join(appRoot, "agent.md"), "model: custom/live-subpatch\n")
	document := patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{{
		ID: "flow", Kind: patch.NodeSubpatch, Subpatch: &patch.SubpatchReference{Path: "flow"},
		Inlets:  []patch.Port{{ID: "trigger", Kind: patch.EnvelopeBang}},
		Outlets: []patch.Port{{ID: "output", Kind: patch.EnvelopeMessage, Schema: map[string]any{"type": "string"}}},
	}}}
	data, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeSubpatchFile(t, filepath.Join(patchRoot, patch.FileName), string(data))
	provider := &countingSubpatchProvider{}
	svc := New(Dependencies{
		Factory:      &runtime.Factory{Override: func(string) (runtime.Provider, error) { return provider, nil }},
		TrackProject: func(string) error { return nil },
	})
	scheduler, err := (patchrun.Engine{
		Runner: svc.PatchNodeRunner(), NewRunID: func() string { return "removed-subpatch-run" },
	}).Start(context.Background(), patchRoot, patchrun.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Pause(); err != nil {
		t.Fatal(err)
	}
	before := scheduler.Inspect()
	if _, err := scheduler.Bang("flow", "trigger"); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Operate(patchrun.TopologyChangeRequest{
		ExpectedTopologyRevision: before.TopologyRevision,
		Removal:                  patchrun.RemovalDrain,
		Actor:                    "conductor",
		Source:                   "service-test",
		Operations:               []patch.Operation{{Type: "remove_node", NodeID: "flow"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Resume(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := scheduler.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("removed subpatch provider calls = %d, want 1", provider.calls.Load())
	}
	if err := scheduler.Drain(); err != nil {
		t.Fatal(err)
	}
	if state, err := scheduler.Wait(ctx); err != nil || state.Status != patchrun.StatusCompleted {
		t.Fatalf("removed subpatch terminal state = %#v, err = %v", state, err)
	}
}
