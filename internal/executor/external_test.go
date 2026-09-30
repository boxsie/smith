package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
)

type fakeExternalRuntime struct {
	admit  func(context.Context, runtime.ContainmentRequest) (runtime.ContainmentAdmission, error)
	invoke func(context.Context, runtime.Invocation, runtime.InvocationSink) error
}

type externalProcessRunnerFunc func(context.Context, runtime.ProcessRequest) (runtime.ProcessResult, error)

func (f externalProcessRunnerFunc) Run(ctx context.Context, request runtime.ProcessRequest) (runtime.ProcessResult, error) {
	return f(ctx, request)
}

func (f *fakeExternalRuntime) Invoke(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
	return f.invoke(ctx, invocation, sink)
}

func (f *fakeExternalRuntime) Admit(ctx context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
	if f.admit != nil {
		return f.admit(ctx, request)
	}
	return testContainmentAdmitter().Admit(ctx, request)
}

type recordingEvents struct {
	mu     sync.Mutex
	events []run.Event
}

type advancingAttemptClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func (c *advancingAttemptClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *advancingAttemptClock) Wait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.now = c.now.Add(delay)
	c.waits = append(c.waits, delay)
	c.mu.Unlock()
	return nil
}

func (c *advancingAttemptClock) advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func (r *recordingEvents) Append(event run.Event) (run.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return event, nil
}

func (r *recordingEvents) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	types := make([]string, len(r.events))
	for index, event := range r.events {
		types[index] = event.Type
	}
	return types
}

func externalFactory(external runtime.ExternalRuntime) *runtime.ExternalFactory {
	return &runtime.ExternalFactory{Override: func(string) (runtime.ExternalRuntime, error) {
		return external, nil
	}}
}

func testContainmentAdmitter() runtime.ContainmentAdmitter {
	return runtime.ContainmentAdmitterFunc(func(_ context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
		return runtime.ContainmentAdmission{RequestedProfile: request.Profile, Mechanism: "test_containment", EffectiveLimits: request.Limits, Enforced: true}, nil
	})
}

func TestExecuteExternalRuntimeReturnsCanonicalJSONAndStreamsEvents(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":                 "---\noutput:\n  type: json\n---\nCount the input.",
		"agent.md":                "runtime: fake\nmodel: fable/test\nprofile: inspect\npersona: careful analyst\n",
		"schema.md":               `{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}`,
		"context/static/rules.md": "be exact",
	})
	root, graph := loadAndBuild(t, dir)
	events := &recordingEvents{}
	inputTokens, outputTokens := 12, 3
	artifactPath := filepath.Join(dir, ".smith", "agent.log")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, []byte("agent trace"), 0o644); err != nil {
		t.Fatal(err)
	}
	var got runtime.Invocation
	external := &fakeExternalRuntime{invoke: func(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
		got = invocation
		if err := sink.Emit(ctx, runtime.RuntimeEvent{Type: "turn.completed", Data: json.RawMessage(`{"turn":1}`)}); err != nil {
			return err
		}
		return sink.Complete(ctx, &runtime.ExternalResult{
			JSON:      json.RawMessage(`{"count":2}`),
			Artifacts: []runtime.Artifact{{Path: artifactPath}},
			Usage:     runtime.Usage{InputTokens: &inputTokens, OutputTokens: &outputTokens},
			Provenance: runtime.Provenance{
				Adapter:         "fake",
				AdapterVersion:  "1",
				ProtocolVersion: "fake-json/1",
				RequestedModel:  "fable/test",
				CanonicalModel:  "fable-5.1",
				SessionID:       "session-1",
				TerminalReason:  "completed",
				BillingBasis:    runtime.BillingSubscription,
			},
		})
	}}

	result, err := Execute(context.Background(), root, graph, Config{
		ExternalFactory: externalFactory(external),
		Events:          events,
		RunID:           "external-success",
		Scope:           map[string]string{"root": dir},
		NoCache:         true,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success || len(result.Tasks) != 1 {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "output", "result.json"))
	if err != nil {
		t.Fatalf("read canonical result: %v", err)
	}
	var canonical map[string]int
	if err := json.Unmarshal(data, &canonical); err != nil || canonical["count"] != 2 {
		t.Fatalf("canonical output = %q (error %v)", data, err)
	}
	if strings.Contains(string(data), "session-1") {
		t.Fatal("protocol provenance leaked into canonical task output")
	}
	if got.Runtime != "fake" || got.Model != "fable/test" || got.Profile != "inspect" {
		t.Fatalf("runtime selection = %#v", got)
	}
	if got.Persona != "careful analyst" || got.Session.Mode != runtime.SessionFresh {
		t.Fatalf("persona/session = %#v", got)
	}
	if got.Workspace.Root != dir || got.Workspace.Access != runtime.WorkspaceReadOnly {
		t.Fatalf("workspace = %#v", got.Workspace)
	}
	if got.Output.Type != "json" || len(got.Output.Schema) == 0 {
		t.Fatalf("output contract = %#v", got.Output)
	}
	if len(got.Context) != 1 || got.Context[0].Name != "rules.md" || got.Context[0].SHA256 == "" {
		t.Fatalf("context = %#v", got.Context)
	}
	if result.Tasks[0].Metrics.Model != "fable-5.1" || result.Tasks[0].Metrics.RequestedModel != "fable/test" || result.Tasks[0].Metrics.Runtime != "fake" {
		t.Fatalf("metrics = %#v", result.Tasks[0].Metrics)
	}
	if result.Tasks[0].RuntimeResult == nil || result.Tasks[0].RuntimeResult.Provenance.SessionID != "session-1" || len(result.Tasks[0].RuntimeResult.Artifacts) != 1 {
		t.Fatalf("runtime result = %#v", result.Tasks[0].RuntimeResult)
	}
	if result.Tasks[0].Metrics.CostUSD != 0 || len(result.Tasks[0].Metrics.RuntimeRecords) != 1 {
		t.Fatalf("external metrics = %#v", result.Tasks[0].Metrics)
	}
	record := result.Tasks[0].Metrics.RuntimeRecords[0]
	if record.AdapterVersion == nil || *record.AdapterVersion != "1" || record.ProtocolVersion == nil || *record.ProtocolVersion != "fake-json/1" ||
		record.CanonicalModel == nil || *record.CanonicalModel != "fable-5.1" || record.Profile.Name != runtime.CapabilityInspect ||
		record.Profile.Session.Mode != runtime.SessionFresh || record.OutputValidation.SchemaSHA256 == nil || !record.OutputValidation.Valid ||
		len(record.Artifacts) != 1 || record.Artifacts[0].SHA256 == "" {
		t.Fatalf("runtime record = %#v", record)
	}
	wantEvents := []string{
		run.EventInvocationQueued, run.EventInvocationStarted,
		run.EventAttemptPending, run.EventAttemptAdmitted, run.EventAttemptStarting,
		run.EventRuntimeStarted, run.EventAttemptRunning, run.EventRuntimeEmitted,
		run.EventArtifactPublished, run.EventRuntimeCompleted, run.EventAttemptTerminal,
		run.EventArtifactPublished, run.EventInvocationCompleted,
	}
	if gotTypes := events.types(); strings.Join(gotTypes, ",") != strings.Join(wantEvents, ",") {
		t.Fatalf("event types = %v, want %v", gotTypes, wantEvents)
	}
	var recordedProfile runtime.ResolvedProfile
	var completedRecord *runtime.Record
	var attemptSpec *run.AttemptSpec
	var terminalCondition *run.AttemptCondition
	canonicalArtifactHasHash := false
	for _, event := range events.events {
		if event.Type == run.EventAttemptPending {
			attemptSpec = event.AttemptSpec
		}
		if event.Type == run.EventAttemptTerminal {
			terminalCondition = event.AttemptCondition
		}
		if event.Type == run.EventRuntimeStarted {
			if err := json.Unmarshal(event.RuntimeProfile, &recordedProfile); err != nil {
				t.Fatalf("decode recorded profile: %v", err)
			}
		}
		if event.Type == run.EventRuntimeCompleted {
			completedRecord = event.RuntimeRecord
		}
		if event.Type == run.EventArtifactPublished && event.Artifact == canonicalArtifact(dir, "json") {
			canonicalArtifactHasHash = event.ArtifactSHA256 != ""
		}
	}
	if recordedProfile.Name != runtime.CapabilityInspect || recordedProfile.Workspace.Access != runtime.WorkspaceReadOnly {
		t.Fatalf("recorded profile = %#v", recordedProfile)
	}
	if completedRecord == nil || completedRecord.Billing.Basis != runtime.BillingSubscription || !canonicalArtifactHasHash {
		t.Fatalf("completed record = %#v, canonical artifact hash = %v", completedRecord, canonicalArtifactHasHash)
	}
	if attemptSpec == nil || attemptSpec.ID != "external-success/i-000001/attempt-001" || attemptSpec.Ordinal != 1 ||
		attemptSpec.TaskInvocationID != "external-success/i-000001" || attemptSpec.Runtime != "fake" || attemptSpec.Model != "fable/test" ||
		attemptSpec.InputSHA256 == "" || attemptSpec.ContextSHA256 == "" || attemptSpec.CapabilityProfile.Profile != runtime.CapabilityInspect ||
		attemptSpec.WorkspaceAuthority.Access != runtime.WorkspaceReadOnly || attemptSpec.ContainmentProfile.EffectiveLimits.MaxMemoryBytes == 0 ||
		attemptSpec.ContainmentProfile.Mechanism == "" {
		t.Fatalf("attempt spec = %#v", attemptSpec)
	}
	if terminalCondition == nil || terminalCondition.Status != run.AttemptTerminal || terminalCondition.Reason != runtime.TerminalSuccess {
		t.Fatalf("terminal attempt condition = %#v", terminalCondition)
	}
}

func TestExecuteExternalCancellationRecordsTerminatingAndTerminalAttempt(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Cancel me.",
		"agent.md": "runtime: fake\nmodel: frontier/test\nprofile: reason\n",
	})
	root, graph := loadAndBuild(t, dir)
	events := &recordingEvents{}
	external := &fakeExternalRuntime{invoke: func(context.Context, runtime.Invocation, runtime.InvocationSink) error {
		return context.Canceled
	}}

	result, err := Execute(context.Background(), root, graph, Config{
		ExternalFactory: externalFactory(external),
		Events:          events,
		RunID:           "external-cancelled",
		NoCache:         true,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Success || len(result.Tasks) != 1 || !errors.Is(result.Tasks[0].Err, context.Canceled) {
		t.Fatalf("result = %#v", result)
	}
	wantTail := []string{run.EventAttemptTerminating, run.EventRuntimeFailed, run.EventAttemptTerminal, run.EventInvocationFailed}
	gotTypes := events.types()
	if len(gotTypes) < len(wantTail) || !slices.Equal(gotTypes[len(gotTypes)-len(wantTail):], wantTail) {
		t.Fatalf("event tail = %v, want %v", gotTypes, wantTail)
	}
	terminal := events.events[len(events.events)-2]
	if terminal.AttemptCondition == nil || terminal.AttemptCondition.Reason != "cancelled" {
		t.Fatalf("terminal attempt = %#v", terminal)
	}
}

func TestExecuteExternalRejectsBeforeInvokeWhenContainmentCannotBeAdmitted(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Do not launch me.",
		"agent.md": "runtime: fake\nmodel: frontier/test\nprofile: reason\n",
	})
	root, graph := loadAndBuild(t, dir)
	events := &recordingEvents{}
	invoked := false
	external := &fakeExternalRuntime{
		admit: func(_ context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
			return runtime.ContainmentAdmission{}, &runtime.ContainmentAdmissionError{
				Profile: request.Profile, Reason: "mechanism_unavailable", Message: "fixture host has no enforceable boundary",
			}
		},
		invoke: func(context.Context, runtime.Invocation, runtime.InvocationSink) error {
			invoked = true
			return nil
		},
	}

	result, err := Execute(context.Background(), root, graph, Config{
		ExternalFactory: externalFactory(external), Events: events, RunID: "admission-rejected", NoCache: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if invoked {
		t.Fatal("external runtime launched after containment admission failed")
	}
	if result.Success || len(result.Tasks) != 1 {
		t.Fatalf("result = %#v", result)
	}
	var admissionErr *runtime.ContainmentAdmissionError
	if !errors.As(result.Tasks[0].Err, &admissionErr) || admissionErr.Reason != "mechanism_unavailable" {
		t.Fatalf("task error = %v", result.Tasks[0].Err)
	}
	gotTypes := events.types()
	if slices.Contains(gotTypes, run.EventAttemptAdmitted) || slices.Contains(gotTypes, run.EventAttemptStarting) || slices.Contains(gotTypes, run.EventAttemptRunning) {
		t.Fatalf("rejected attempt crossed launch boundary: %v", gotTypes)
	}
	var terminal *run.AttemptCondition
	for _, event := range events.events {
		if event.Type == run.EventAttemptTerminal {
			terminal = event.AttemptCondition
		}
	}
	if terminal == nil || terminal.Reason != runtime.TerminalTaskLimit {
		t.Fatalf("terminal condition = %#v", terminal)
	}
}

func TestExecuteWorkProfileRequiresConductorGrant(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":   "---\noutput:\n  type: json\n---\nEdit the workspace.",
		"agent.md":  "runtime: fake\nmodel: frontier/test\nprofile: work\n",
		"schema.md": `{"type":"object","properties":{"changed":{"type":"boolean"}},"required":["changed"]}`,
	})
	root, graph := loadAndBuild(t, dir)
	called := false
	external := &fakeExternalRuntime{invoke: func(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
		called = true
		if invocation.Workspace.Access != runtime.WorkspaceWritable || !invocation.Workspace.Granted || invocation.Workspace.GrantRoot != dir {
			t.Fatalf("workspace = %#v", invocation.Workspace)
		}
		return sink.Complete(ctx, &runtime.ExternalResult{JSON: json.RawMessage(`{"changed":true}`), Provenance: runtime.Provenance{Adapter: "fake", RequestedModel: invocation.Model}})
	}}

	_, err := Execute(context.Background(), root, graph, Config{ExternalFactory: externalFactory(external), Scope: map[string]string{"root": dir}, AppRoot: dir, NoCache: true})
	if err == nil || !strings.Contains(err.Error(), "conductor-granted") {
		t.Fatalf("missing grant error = %v", err)
	}
	if called {
		t.Fatal("work runtime started before authority was granted")
	}

	result, err := Execute(context.Background(), root, graph, Config{ExternalFactory: externalFactory(external), Scope: map[string]string{"root": dir}, AppRoot: dir, WritableRoots: []string{dir}, NoCache: true})
	if err != nil || !result.Success || !called {
		t.Fatalf("granted work result = %#v, error %v", result, err)
	}
}

func TestExecuteStickyProfileRetainsOnlyItsNodeSession(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":   "---\noutput:\n  type: json\n---\nContinue the conversation.",
		"agent.md":  "runtime: fake\nmodel: frontier/test\nprofile: reason\nsession:\n  mode: sticky\n",
		"schema.md": `{"type":"object","properties":{"turn":{"type":"integer"}},"required":["turn"]}`,
	})
	root, graph := loadAndBuild(t, dir)
	var seen []string
	external := &fakeExternalRuntime{invoke: func(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
		seen = append(seen, invocation.Session.ID)
		turn := len(seen)
		return sink.Complete(ctx, &runtime.ExternalResult{
			JSON:       json.RawMessage(fmt.Sprintf(`{"turn":%d}`, turn)),
			Provenance: runtime.Provenance{Adapter: "fake", RequestedModel: invocation.Model, SessionID: fmt.Sprintf("sticky-session-%d", turn)},
		})
	}}
	cfg := Config{ExternalFactory: externalFactory(external), Scope: map[string]string{"root": dir}, AppRoot: dir, NoCache: true}
	for index := 0; index < 3; index++ {
		result, err := Execute(context.Background(), root, graph, cfg)
		if err != nil || !result.Success {
			t.Fatalf("run %d = %#v, error %v", index, result, err)
		}
	}
	if len(seen) != 3 || seen[0] != "" || seen[1] != "sticky-session-1" || seen[2] != "sticky-session-2" {
		t.Fatalf("sticky session ids = %v", seen)
	}

	freshDir := setupTree(t, map[string]string{
		"task.md":   "---\noutput:\n  type: json\n---\nStart fresh.",
		"agent.md":  "runtime: fake\nmodel: frontier/test\nprofile: reason\n",
		"schema.md": `{"type":"object","properties":{"turn":{"type":"integer"}},"required":["turn"]}`,
	})
	freshRoot, freshGraph := loadAndBuild(t, freshDir)
	seen = nil
	freshCfg := Config{ExternalFactory: externalFactory(external), Scope: map[string]string{"root": freshDir}, AppRoot: freshDir, NoCache: true}
	for index := 0; index < 2; index++ {
		result, err := Execute(context.Background(), freshRoot, freshGraph, freshCfg)
		if err != nil || !result.Success {
			t.Fatalf("fresh run %d = %#v, error %v", index, result, err)
		}
	}
	if seen[0] != "" || seen[1] != "" {
		t.Fatalf("fresh sessions retained ids: %v", seen)
	}
}

func TestExecuteExternalProfileEnforcesRuntimeLimits(t *testing.T) {
	tests := []struct {
		name   string
		limits string
		invoke func(context.Context, runtime.Invocation, runtime.InvocationSink) error
		want   string
	}{
		{
			name:   "events",
			limits: "  max_events: 1\n",
			invoke: func(ctx context.Context, _ runtime.Invocation, sink runtime.InvocationSink) error {
				if err := sink.Emit(ctx, runtime.RuntimeEvent{Type: "one"}); err != nil {
					return err
				}
				return sink.Emit(ctx, runtime.RuntimeEvent{Type: "two"})
			},
			want: "max_events",
		},
		{
			name:   "output",
			limits: "  max_output_bytes: 5\n",
			invoke: func(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
				return sink.Complete(ctx, &runtime.ExternalResult{JSON: json.RawMessage(`{"answer":42}`), Provenance: runtime.Provenance{Adapter: "fake", RequestedModel: invocation.Model}})
			},
			want: "max_output_bytes",
		},
		{
			name:   "turns",
			limits: "  max_turns: 1\n",
			invoke: func(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
				turns := 2
				return sink.Complete(ctx, &runtime.ExternalResult{JSON: json.RawMessage(`{"answer":42}`), Usage: runtime.Usage{Turns: &turns}, Provenance: runtime.Provenance{Adapter: "fake", RequestedModel: invocation.Model}})
			},
			want: "max_turns",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := setupTree(t, map[string]string{
				"task.md":   "---\noutput:\n  type: json\n---\nReturn the answer.",
				"agent.md":  "runtime: fake\nmodel: frontier/test\nprofile: reason\nlimits:\n" + test.limits,
				"schema.md": `{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`,
			})
			root, graph := loadAndBuild(t, dir)
			result, err := Execute(context.Background(), root, graph, Config{ExternalFactory: externalFactory(&fakeExternalRuntime{invoke: test.invoke}), Scope: map[string]string{"root": dir}, AppRoot: dir, NoCache: true})
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if result.Success || len(result.Tasks) != 1 || result.Tasks[0].Err == nil || !strings.Contains(result.Tasks[0].Err.Error(), test.want) {
				t.Fatalf("result = %#v, want %q limit failure", result, test.want)
			}
		})
	}
}

func TestExecuteClaudeRuntimeFromTaskSelection(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":   "---\noutput:\n  type: json\n---\nReturn the answer.",
		"agent.md":  "runtime: claude\nmodel: claude-fable-5-1\nprofile: reason\npersona: exact calculator\n",
		"schema.md": `{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`,
	})
	root, graph := loadAndBuild(t, dir)
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"smith-claude-session","tools":["StructuredOutput"],"mcp_servers":[],"model":"claude-fable-5-1","permissionMode":"plan","slash_commands":[],"apiKeySource":"none","claude_code_version":"2.1.258"}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"smith-claude-session","duration_ms":250,"num_turns":1,"terminal_reason":"completed","usage":{"input_tokens":3,"cache_creation_input_tokens":2000,"cache_read_input_tokens":0,"output_tokens":5},"modelUsage":{"claude-fable-5-1":{"canonicalModel":"claude-fable-5-1","costBasis":"list"}},"permission_denials":[],"result":"{\"answer\":42}","structured_output":{"answer":42}}`,
	}, "\n")
	claude := &runtime.ClaudeRuntime{Executable: "claude-fixture", Environment: []string{"HOME=/fixture"}, Admitter: testContainmentAdmitter()}
	claude.Runner = externalProcessRunnerFunc(func(_ context.Context, request runtime.ProcessRequest) (runtime.ProcessResult, error) {
		if request.Executable != "claude-fixture" || string(request.Stdin) == "" {
			t.Fatalf("process request = %#v", request)
		}
		for _, line := range strings.Split(stream, "\n") {
			if err := request.StdoutLine([]byte(line)); err != nil {
				return runtime.ProcessResult{}, err
			}
		}
		return runtime.ProcessResult{Stdout: []byte(stream)}, nil
	})

	result, err := Execute(context.Background(), root, graph, Config{
		ExternalFactory: &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{runtime.ClaudeRuntimeName: claude}},
		NoCache:         true,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success || len(result.Tasks) != 1 {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "output", "result.json"))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var canonical map[string]int
	if err := json.Unmarshal(data, &canonical); err != nil || canonical["answer"] != 42 {
		t.Fatalf("canonical output = %q (error %v)", data, err)
	}
	runtimeResult := result.Tasks[0].RuntimeResult
	if runtimeResult == nil || runtimeResult.Provenance.Adapter != "claude" ||
		runtimeResult.Provenance.SessionID != "smith-claude-session" ||
		runtimeResult.Provenance.BillingBasis != runtime.BillingReportedListEstimate {
		t.Fatalf("runtime result = %#v", runtimeResult)
	}
	record := onlyRuntimeRecord(t, result)
	if record.CLIVersion == nil || *record.CLIVersion != "2.1.258" || record.ProtocolVersion == nil || *record.ProtocolVersion != "claude-stream-json/1" ||
		record.CanonicalModel == nil || *record.CanonicalModel != "claude-fable-5-1" || record.Usage.CachedInputTokens == nil || *record.Usage.CachedInputTokens != 2000 ||
		record.Billing.Basis != runtime.BillingReportedListEstimate || result.Tasks[0].Metrics.CostUSD != 0 {
		t.Fatalf("claude record = %#v", record)
	}
}

func TestExecuteCodexRuntimeFromTaskSelection(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":   "---\noutput:\n  type: json\n---\nReturn the answer.",
		"agent.md":  "runtime: codex\nmodel: gpt-5.6-sol\nprofile: reason\npersona: exact calculator\n",
		"schema.md": `{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`,
	})
	root, graph := loadAndBuild(t, dir)
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"smith-codex-session"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"{\"result_json\":\"{\\\"answer\\\":42}\"}"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":80,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0}}`,
	}, "\n")
	codex := &runtime.CodexRuntime{Executable: "codex-fixture", Environment: []string{"HOME=/fixture"}, Admitter: testContainmentAdmitter()}
	codex.Runner = externalProcessRunnerFunc(func(_ context.Context, request runtime.ProcessRequest) (runtime.ProcessResult, error) {
		if request.Executable != "codex-fixture" || string(request.Stdin) == "" {
			t.Fatalf("process request = %#v", request)
		}
		for _, line := range strings.Split(stream, "\n") {
			if err := request.StdoutLine([]byte(line)); err != nil {
				return runtime.ProcessResult{}, err
			}
		}
		return runtime.ProcessResult{Stdout: []byte(stream)}, nil
	})

	result, err := Execute(context.Background(), root, graph, Config{
		ExternalFactory: &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{runtime.CodexRuntimeName: codex}},
		NoCache:         true,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success || len(result.Tasks) != 1 {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "output", "result.json"))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var canonical map[string]int
	if err := json.Unmarshal(data, &canonical); err != nil || canonical["answer"] != 42 {
		t.Fatalf("canonical output = %q (error %v)", data, err)
	}
	runtimeResult := result.Tasks[0].RuntimeResult
	if runtimeResult == nil || runtimeResult.Provenance.Adapter != "codex" ||
		runtimeResult.Provenance.SessionID != "smith-codex-session" ||
		runtimeResult.Provenance.BillingBasis != runtime.BillingSubscription {
		t.Fatalf("runtime result = %#v", runtimeResult)
	}
	record := onlyRuntimeRecord(t, result)
	if record.CLIVersion != nil || record.CanonicalModel != nil || record.ProtocolVersion == nil || *record.ProtocolVersion != "codex-exec-jsonl/1" ||
		record.Usage.CachedInputTokens == nil || *record.Usage.CachedInputTokens != 80 || record.Billing.Basis != runtime.BillingSubscription || record.Billing.AmountUSD != nil ||
		result.Tasks[0].Metrics.CostUSD != 0 {
		t.Fatalf("codex record = %#v", record)
	}
}

func TestExecuteGrokRuntimeFromTaskSelection(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":   "---\noutput:\n  type: json\n---\nReturn the answer.",
		"agent.md":  "runtime: grok\nmodel: grok-4.5\nprofile: reason\npersona: exact calculator\n",
		"schema.md": `{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`,
	})
	root, graph := loadAndBuild(t, dir)
	stream := strings.Join([]string{
		`{"type":"available_commands","tools":[],"commands":["compact"]}`,
		`{"type":"text","data":"{\"answer\":42}"}`,
		`{"type":"end","stopReason":"end_turn","sessionId":"smith-grok-session","requestId":"request-1","usage":{"input_tokens":100,"cache_read_input_tokens":20,"output_tokens":5,"total_tokens":125},"num_turns":1,"total_cost_usd":0.01,"modelUsage":{"grok-4.5-build":{"inputTokens":100,"outputTokens":5,"cacheReadInputTokens":20,"modelCalls":1,"costUSD":0.01}},"structuredOutput":{"answer":42}}`,
	}, "\n")
	grok := &runtime.GrokRuntime{Executable: "grok-fixture", Environment: []string{"HOME=/fixture"}, Admitter: testContainmentAdmitter()}
	grok.Runner = externalProcessRunnerFunc(func(_ context.Context, request runtime.ProcessRequest) (runtime.ProcessResult, error) {
		if request.Executable != "grok-fixture" || !slices.Contains(request.Args, "--prompt-file") {
			t.Fatalf("process request = %#v", request)
		}
		for _, line := range strings.Split(stream, "\n") {
			if err := request.StdoutLine([]byte(line)); err != nil {
				return runtime.ProcessResult{}, err
			}
		}
		return runtime.ProcessResult{Stdout: []byte(stream)}, nil
	})

	result, err := Execute(context.Background(), root, graph, Config{
		ExternalFactory: &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{runtime.GrokRuntimeName: grok}},
		NoCache:         true,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success || len(result.Tasks) != 1 {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "output", "result.json"))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var canonical map[string]int
	if err := json.Unmarshal(data, &canonical); err != nil || canonical["answer"] != 42 {
		t.Fatalf("canonical output = %q (error %v)", data, err)
	}
	runtimeResult := result.Tasks[0].RuntimeResult
	if runtimeResult == nil || runtimeResult.Provenance.Adapter != "grok" ||
		runtimeResult.Provenance.SessionID != "smith-grok-session" ||
		runtimeResult.Provenance.BillingBasis != runtime.BillingReportedListEstimate {
		t.Fatalf("runtime result = %#v", runtimeResult)
	}
	record := onlyRuntimeRecord(t, result)
	if record.CLIVersion != nil || record.ProtocolVersion == nil || *record.ProtocolVersion != "grok-streaming-json/1" ||
		record.CanonicalModel == nil || *record.CanonicalModel != "grok-4.5-build" || record.Billing.AmountUSD == nil || *record.Billing.AmountUSD != 0.01 ||
		record.Billing.Basis != runtime.BillingReportedListEstimate || result.Tasks[0].Metrics.CostUSD != 0 {
		t.Fatalf("grok record = %#v", record)
	}
}

func onlyRuntimeRecord(t *testing.T, result *Result) runtime.Record {
	t.Helper()
	if len(result.Tasks) != 1 || len(result.Tasks[0].Metrics.RuntimeRecords) != 1 {
		t.Fatalf("runtime records = %#v", result.Tasks)
	}
	return result.Tasks[0].Metrics.RuntimeRecords[0]
}

func TestExecuteExternalRuntimeFailureMatrix(t *testing.T) {
	tests := []struct {
		name       string
		result     *runtime.ExternalResult
		err        error
		want       string
		wantAsExit bool
	}{
		{name: "schema failure", result: &runtime.ExternalResult{JSON: json.RawMessage(`{"count":"two"}`)}, want: "JSON schema validation failed"},
		{name: "malformed output", result: &runtime.ExternalResult{JSON: json.RawMessage(`{"count"`)}, want: "malformed external runtime result"},
		{name: "nonzero exit", err: &runtime.ProcessError{Executable: "fake", ExitCode: 9, Stderr: "denied"}, want: "exited 9", wantAsExit: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := setupTree(t, map[string]string{
				"task.md":   "---\noutput:\n  type: json\n---\nCount.",
				"agent.md":  "runtime: fake\nmodel: frontier/test\n",
				"schema.md": `{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}`,
			})
			root, graph := loadAndBuild(t, dir)
			external := &fakeExternalRuntime{invoke: func(ctx context.Context, _ runtime.Invocation, sink runtime.InvocationSink) error {
				if test.err != nil {
					return test.err
				}
				return sink.Complete(ctx, test.result)
			}}
			result, err := Execute(context.Background(), root, graph, Config{ExternalFactory: externalFactory(external), NoCache: true})
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if result.Success || len(result.Tasks) != 1 || result.Tasks[0].Err == nil {
				t.Fatalf("result = %#v", result)
			}
			if !strings.Contains(result.Tasks[0].Err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", result.Tasks[0].Err, test.want)
			}
			if test.wantAsExit {
				var processErr *runtime.ProcessError
				if !errors.As(result.Tasks[0].Err, &processErr) {
					t.Fatalf("error = %v, want ProcessError", result.Tasks[0].Err)
				}
			}
		})
	}
}

func TestExecuteExternalRuntimeHonorsDeadlineAndCancellation(t *testing.T) {
	tests := []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		want    error
	}{
		{name: "timeout", context: func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 20*time.Millisecond)
		}, want: context.DeadlineExceeded},
		{name: "cancellation", context: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, want: context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := setupTree(t, map[string]string{"task.md": "wait", "agent.md": "runtime: fake\nmodel: frontier/test\n"})
			root, graph := loadAndBuild(t, dir)
			started := make(chan struct{})
			external := &fakeExternalRuntime{invoke: func(ctx context.Context, invocation runtime.Invocation, _ runtime.InvocationSink) error {
				if errors.Is(test.want, context.DeadlineExceeded) && invocation.Timeout <= 0 {
					t.Errorf("invocation timeout = %v, want positive deadline", invocation.Timeout)
				}
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}}
			ctx, cancel := test.context()
			if errors.Is(test.want, context.Canceled) {
				go func() { <-started; cancel() }()
			} else {
				defer cancel()
			}
			result, err := Execute(ctx, root, graph, Config{ExternalFactory: externalFactory(external), NoCache: true})
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if result.Success || !errors.Is(result.Tasks[0].Err, test.want) {
				t.Fatalf("task error = %v, want %v", result.Tasks[0].Err, test.want)
			}
		})
	}
}

func TestExecuteExternalRetriesOnlyDeclaredReasonWithinActiveDeadline(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Return done.",
		"agent.md": "runtime: fake\nmodel: frontier/test\nlimits:\n  timeout: 6s\nattempts:\n  restart: on_failure\n  max_attempts: 3\n  retryable_reasons: [launch_failure]\n  active_deadline: 10s\n  backoff:\n    initial: 2s\n    maximum: 4s\n    multiplier: 2\n",
	})
	root, graph := loadAndBuild(t, dir)
	events := &recordingEvents{}
	clock := &advancingAttemptClock{now: time.Date(2026, time.September, 3, 17, 0, 0, 0, time.UTC)}
	invocations := 0
	var timeouts []time.Duration
	external := &fakeExternalRuntime{invoke: func(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
		invocations++
		timeouts = append(timeouts, invocation.Timeout)
		if invocations == 1 {
			clock.advance(4 * time.Second)
			return &runtime.ProcessLaunchError{Executable: "fixture", Err: errors.New("temporarily unavailable")}
		}
		return sink.Complete(ctx, &runtime.ExternalResult{Text: "done", Provenance: runtime.Provenance{Adapter: "fake", RequestedModel: invocation.Model}})
	}}
	result, err := Execute(context.Background(), root, graph, Config{
		ExternalFactory: externalFactory(external), Events: events, RunID: "retry", NoCache: true, AttemptClock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || invocations != 2 || !slices.Equal(timeouts, []time.Duration{6 * time.Second, 4 * time.Second}) {
		t.Fatalf("result=%#v invocations=%d timeouts=%v", result, invocations, timeouts)
	}
	if !slices.Equal(clock.waits, []time.Duration{2 * time.Second}) {
		t.Fatalf("retry waits = %v", clock.waits)
	}
	var retries int
	var terminalReasons []string
	for _, event := range events.events {
		if event.Type == run.EventAttemptRetry && event.AttemptRetry != nil && event.AttemptRetry.NextOrdinal == 2 {
			retries++
		}
		if event.Type == run.EventAttemptTerminal {
			terminalReasons = append(terminalReasons, event.AttemptCondition.Reason)
		}
	}
	if retries != 1 || !slices.Equal(terminalReasons, []string{runtime.TerminalLaunchFailure, runtime.TerminalSuccess}) {
		t.Fatalf("retry count=%d terminal reasons=%v events=%#v", retries, terminalReasons, events.events)
	}
}

func TestExecuteExternalDoesNotRetryUndeclaredResourceFailure(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Fail safely.",
		"agent.md": "runtime: fake\nmodel: frontier/test\nattempts:\n  restart: on_failure\n  max_attempts: 3\n  retryable_reasons: [launch_failure]\n  active_deadline: 1m\n",
	})
	root, graph := loadAndBuild(t, dir)
	invocations := 0
	events := &recordingEvents{}
	external := &fakeExternalRuntime{invoke: func(context.Context, runtime.Invocation, runtime.InvocationSink) error {
		invocations++
		return &runtime.ProcessResourceError{Resource: "memory", Limit: 1024, Err: errors.New("fixture breach")}
	}}
	result, err := Execute(context.Background(), root, graph, Config{
		ExternalFactory: externalFactory(external), Events: events, RunID: "resource-limit", NoCache: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || invocations != 1 {
		t.Fatalf("result=%#v invocations=%d", result, invocations)
	}
	var terminal *run.AttemptCondition
	for _, event := range events.events {
		if event.Type == run.EventAttemptTerminal {
			terminal = event.AttemptCondition
		}
	}
	if terminal == nil || terminal.Reason != runtime.TerminalMemoryLimit || terminal.Limit == nil || terminal.Limit.Name != "memory" || terminal.Limit.Value != 1024 {
		t.Fatalf("terminal resource evidence = %#v", terminal)
	}
}
