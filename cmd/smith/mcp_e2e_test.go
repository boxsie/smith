package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/capability"
	"github.com/boxsie/smith/internal/cli"
	"github.com/boxsie/smith/internal/mcpserver"
	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/projects"
	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type recoveryExternalRuntime struct{ marker string }

func (recoveryExternalRuntime) Admit(_ context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
	return runtime.ContainmentAdmission{RequestedProfile: request.Profile, Mechanism: "test_containment", EffectiveLimits: request.Limits, Enforced: true}, nil
}

func (r recoveryExternalRuntime) Invoke(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
	if len(invocation.MCPServers) != 1 || invocation.MCPServers[0].Capability != "test.mutate" || !invocation.Workspace.Granted {
		return fmt.Errorf("recovered invocation authority = %#v, %#v", invocation.MCPServers, invocation.Workspace)
	}
	if _, err := os.Stat(r.marker); os.IsNotExist(err) {
		if err := os.WriteFile(r.marker, []byte("first process entered work\n"), 0o600); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}
	return sink.Complete(ctx, &runtime.ExternalResult{JSON: json.RawMessage(`{"ok":true}`), Provenance: runtime.Provenance{Adapter: "recovery", RequestedModel: invocation.Model, BillingBasis: runtime.BillingSubscription}})
}

type recoveryCapability struct{}

func (recoveryCapability) Open(_ context.Context, request capability.OpenRequest) (*capability.Binding, error) {
	return &capability.Binding{Server: runtime.MCPServer{Name: "test", URL: "http://127.0.0.1/unused", Capability: "test." + request.Access}, Close: func() error { return nil }}, nil
}

type attemptRecoveryRuntime struct{}

func (attemptRecoveryRuntime) Admit(_ context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
	return runtime.ContainmentAdmission{
		RequestedProfile: request.Profile, Mechanism: "test_containment", EffectiveLimits: request.Limits, Enforced: true,
	}, nil
}

func (attemptRecoveryRuntime) Invoke(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
	exitCode := 137
	memory, tasks, cpu := int64(64<<20), int64(3), int64(70)
	record := runtime.ExternalProcessRecord{
		Schema: runtime.ExternalProcessSchema, Status: "failed", TerminalReason: runtime.TerminalMemoryLimit,
		CWD: runtime.RecordedText{Text: invocation.Workspace.Root}, ExitCode: &exitCode,
		Stdout: runtime.RecordedText{Text: "child pids: 101 102"}, Stderr: runtime.RecordedText{Text: "Finished with result: oom-kill"},
		DurationMS: 240,
		Measurements: runtime.ResourceMeasurements{
			PeakMemoryBytes: &memory, PeakTasks: &tasks, CPUTimeMS: &cpu, Method: "fixture_cgroup_v2",
		},
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := sink.Emit(ctx, runtime.RuntimeEvent{Type: "runtime.process.completed", Data: data}); err != nil {
		return err
	}
	return &runtime.ProcessResourceError{Resource: "memory", Limit: memory, Err: errors.New("fixture aggregate memory breach")}
}

func TestSmithMCPAttemptInspectionSurvivesProcessRestart(t *testing.T) {
	if os.Getenv("SMITH_ATTEMPT_RECOVERY_HELPER") == "1" {
		factory := &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{"attempt-fixture": attemptRecoveryRuntime{}}}
		smith := service.New(service.Dependencies{ExternalFactory: factory})
		server, err := mcpserver.New(mcpserver.Dependencies{Service: smith, Version: "attempt-recovery-test"})
		if err != nil || server.Run(context.Background(), &mcp.StdioTransport{}) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	home := t.TempDir()
	root := filepath.Join(home, "attempt-recovery")
	writeLifecycleFile(t, filepath.Join(root, "task.md"), "fail inside the contained fixture\n")
	writeLifecycleFile(t, filepath.Join(root, "agent.md"), "runtime: attempt-fixture\nmodel: fixture/memory\nprofile: reason\n")
	if err := projects.SaveTo(filepath.Join(home, ".smith"), &projects.Index{Projects: []string{root}}); err != nil {
		t.Fatal(err)
	}

	first := connectAttemptRecoverySmithCommand(t, ctx, home)
	runID := startRun(t, first, root)
	waitForTerminalEvent(t, first, root, runID, run.EventRunFailed)
	waitForRunStatus(t, first, root, runID, "failed")
	var before run.AttemptInspectionPage
	callSmithTool(t, first, "run_attempts", map[string]any{"app": root, "run_id": runID, "limit": 10}, &before)
	assertMemoryAttemptInspection(t, before)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	lease, err := run.AcquireRunLease(run.RunDir(root, runID))
	if err != nil {
		t.Fatalf("completed failed run retained its lease: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}

	second := connectAttemptRecoverySmithCommand(t, ctx, home)
	defer func() { _ = second.Close() }()
	var after run.AttemptInspectionPage
	callSmithTool(t, second, "run_attempts", map[string]any{"app": root, "run_id": runID, "limit": 10}, &after)
	assertMemoryAttemptInspection(t, after)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("attempt projection changed across Smith restart\nbefore: %#v\nafter: %#v", before, after)
	}
}

func connectAttemptRecoverySmithCommand(t *testing.T, ctx context.Context, home string) *mcp.ClientSession {
	t.Helper()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSmithMCPAttemptInspectionSurvivesProcessRestart$")
	command.Env = append(withoutSmithRuntime(os.Environ()), "SMITH_ATTEMPT_RECOVERY_HELPER=1", "HOME="+home)
	client := mcp.NewClient(&mcp.Implementation{Name: "attempt-recovery-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func assertMemoryAttemptInspection(t *testing.T, page run.AttemptInspectionPage) {
	t.Helper()
	if len(page.Attempts) != 1 {
		t.Fatalf("attempt page = %#v", page)
	}
	attempt := page.Attempts[0]
	if attempt.TerminalReason == nil || *attempt.TerminalReason != runtime.TerminalMemoryLimit ||
		attempt.Limit == nil || attempt.Limit.Name != "memory" || attempt.Limit.Value != 64<<20 ||
		attempt.Process == nil || attempt.Process.Record.ExitCode == nil || *attempt.Process.Record.ExitCode != 137 ||
		attempt.Measurements.PeakMemoryBytes == nil || *attempt.Measurements.PeakMemoryBytes != 64<<20 ||
		attempt.Measurements.PeakTasks == nil || *attempt.Measurements.PeakTasks != 3 ||
		attempt.Measurements.CPUTimeMS == nil || *attempt.Measurements.CPUTimeMS != 70 {
		t.Fatalf("memory attempt = %#v", attempt)
	}
}

func TestSmithMCPPatchRecoveryAcrossProcessRestart(t *testing.T) {
	if os.Getenv("SMITH_PATCH_RECOVERY_HELPER") == "1" {
		factory := &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{"recovery": recoveryExternalRuntime{marker: os.Getenv("SMITH_PATCH_RECOVERY_MARKER")}}}
		capabilities := capability.NewFactory()
		if err := capabilities.Register("test", recoveryCapability{}); err != nil {
			os.Exit(1)
		}
		smith := service.New(service.Dependencies{ExternalFactory: factory, CapabilityFactory: capabilities})
		server, err := mcpserver.New(mcpserver.Dependencies{Service: smith, Version: "recovery-test"})
		if err != nil || server.Run(context.Background(), &mcp.StdioTransport{}) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	home := t.TempDir()
	root := filepath.Join(home, "smith-recovery")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"config", "user.name", "smith test"}, {"config", "user.email", "smith@example.invalid"}, {"commit", "--allow-empty", "--quiet", "-m", "baseline"}} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if data, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, data)
		}
	}
	if err := projects.SaveTo(filepath.Join(home, ".smith"), &projects.Index{Projects: []string{root}}); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "work-entered")
	first := connectRecoverySmithCommand(t, ctx, home, marker)
	document := patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{{
		ID: "work", Kind: patch.NodeRuntime,
		Runtime: &patch.RuntimeReference{Runtime: "recovery", Model: "fake", Profile: runtime.CapabilityWork},
		Config:  map[string]any{"prompt": "hold work across restart", "workspace_mode": "root", "ticket": "smith/recovery", "capabilities": []any{"test.mutate"}},
		Inlets:  []patch.Port{{ID: "start", Kind: patch.EnvelopeMessage, Schema: map[string]any{"type": "object"}}},
		Outlets: []patch.Port{{ID: "done", Kind: patch.EnvelopeMessage, Schema: map[string]any{"type": "object", "required": []any{"ok"}}}},
	}}}
	callSmithTool(t, first, "patch_create", map[string]any{"patch": root, "document": document}, nil)
	var started struct {
		RunID string `json:"run_id"`
	}
	grant := map[string]any{"package": "test", "access": "mutate", "scope": map[string]string{"project": "scope-must-not-persist"}}
	options := map[string]any{"max_parallel": 1, "max_hops": 16, "default_queue": map[string]any{"capacity": 16, "overflow": "reject"}}
	callSmithTool(t, first, "patch_start", map[string]any{"patch": root, "options": options, "writable_roots": []string{root}, "capability_grants": []any{grant}}, &started)
	callSmithTool(t, first, "patch_send", map[string]any{"patch": root, "run_id": started.RunID, "node_id": "work", "port_id": "start", "kind": "message", "payload": map[string]any{}}, nil)
	waitForPatchEvent(t, first, root, started.RunID, patchrun.EventWorkspaceAcquired)
	callSmithToolError(t, first, "patch_recover", map[string]any{"patch": root, "run_id": started.RunID, "writable_roots": []string{root}, "capability_grants": []any{grant}}, "already open")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := connectRecoverySmithCommand(t, ctx, home, marker)
	defer func() { _ = second.Close() }()
	var inspection struct {
		RecoveryNeeded bool `json:"recovery_needed"`
		Owner          struct {
			ID string `json:"id"`
		} `json:"owner"`
	}
	callSmithTool(t, second, "workspace_inspect", map[string]any{"workspace": root}, &inspection)
	if !inspection.RecoveryNeeded || inspection.Owner.ID == "" {
		t.Fatalf("interrupted workspace = %#v", inspection)
	}
	callSmithTool(t, second, "workspace_recover", map[string]any{
		"workspace": root, "expected_owner_id": inspection.Owner.ID,
		"action": "release", "reason": "first MCP process was deliberately stopped",
	}, nil)
	callSmithToolError(t, second, "patch_recover", map[string]any{
		"patch": root, "run_id": started.RunID, "writable_roots": []string{root}, "capability_grants": []any{grant, grant},
	}, "granted more than once")
	var recovered struct {
		RunID string `json:"run_id"`
	}
	callSmithTool(t, second, "patch_recover", map[string]any{"patch": root, "run_id": started.RunID, "writable_roots": []string{root}, "capability_grants": []any{grant}}, &recovered)
	if recovered.RunID != started.RunID {
		t.Fatalf("recovered run = %q, want %q", recovered.RunID, started.RunID)
	}
	events := waitForPatchEvent(t, second, root, started.RunID, patchrun.EventInvocationCompleted)
	if !containsLifecycleEvent(events, patchrun.EventPatchRecovered) || !containsLifecycleEvent(events, patchrun.EventInvocationInterrupted) {
		t.Fatalf("recovery events = %#v", events)
	}
	callSmithTool(t, second, "patch_control", map[string]any{"patch": root, "run_id": started.RunID, "action": "stop"}, nil)
	callSmithToolError(t, second, "patch_recover", map[string]any{"patch": root, "run_id": started.RunID, "writable_roots": []string{root}, "capability_grants": []any{grant}}, "terminal")
	persisted, err := os.ReadFile(patchrun.EventsPath(patchrun.RunDir(root, started.RunID)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), "scope-must-not-persist") || strings.Contains(string(persisted), "127.0.0.1/unused") {
		t.Fatalf("process-local authority leaked into events: %s", persisted)
	}
}

func connectRecoverySmithCommand(t *testing.T, ctx context.Context, home, marker string) *mcp.ClientSession {
	t.Helper()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSmithMCPPatchRecoveryAcrossProcessRestart$")
	command.Env = append(withoutSmithRuntime(os.Environ()), "SMITH_PATCH_RECOVERY_HELPER=1", "SMITH_PATCH_RECOVERY_MARKER="+marker, "HOME="+home)
	client := mcp.NewClient(&mcp.Implementation{Name: "patch-recovery-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func waitForPatchEvent(t *testing.T, session *mcp.ClientSession, root, runID, wanted string) []lifecycleEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last []lifecycleEvent
	for time.Now().Before(deadline) {
		var page struct {
			Events []lifecycleEvent `json:"events"`
		}
		callSmithTool(t, session, "patch_events", map[string]any{"patch": root, "run_id": runID, "limit": 1000}, &page)
		last = page.Events
		if containsLifecycleEvent(page.Events, wanted) {
			return page.Events
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for patch event %q: %#v", wanted, last)
	return nil
}

func containsLifecycleEvent(events []lifecycleEvent, wanted string) bool {
	for _, event := range events {
		if event.Type == wanted {
			return true
		}
	}
	return false
}

func callSmithToolError(t *testing.T, session *mcp.ClientSession, name string, arguments any, wanted string) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	var message string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			message += text.Text
		}
	}
	if !result.IsError || !strings.Contains(message, wanted) {
		t.Fatalf("%s error = %v %q, want %q", name, result.IsError, message, wanted)
	}
}

func TestSmithMCPColdProcess(t *testing.T) {
	if os.Getenv("SMITH_MCP_HELPER") == "1" {
		os.Args = []string{os.Args[0], "mcp"}
		if err := cli.Execute(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSmithMCPColdProcess")
	command.Env = append(withoutSmithRuntime(os.Environ()), "SMITH_MCP_HELPER=1", "HOME="+t.TempDir())
	client := mcp.NewClient(&mcp.Implementation{Name: "cold-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "system_capabilities", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if result.IsError || !strings.Contains(text, `"configured":false`) || strings.Contains(text, "api_key") {
		t.Fatalf("cold capabilities = %s", data)
	}
}

func TestSmithMCPCompleteLifecycleAcrossRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	home := t.TempDir()
	appRoot := filepath.Join(home, "apps", "lifecycle")
	writeLifecycleFile(t, filepath.Join(appRoot, "task.md"), "printf 'lifecycle complete\\n'\n")
	writeLifecycleFile(t, filepath.Join(appRoot, "agent.md"), "model: shell\n")
	// Provisioning the known app is test-fixture setup. From the first MCP
	// connection onward, every inspect, mutation, run, and read crosses stdio.
	if err := projects.SaveTo(filepath.Join(home, ".smith"), &projects.Index{Projects: []string{appRoot}}); err != nil {
		t.Fatal(err)
	}

	first := connectSmithCommand(t, ctx, home)
	var capabilities struct {
		Version string `json:"version"`
	}
	callSmithTool(t, first, "system_capabilities", map[string]any{}, &capabilities)
	if capabilities.Version == "" {
		t.Fatal("system_capabilities returned no version")
	}
	var apps struct {
		Apps []struct {
			Path      string `json:"path"`
			Available bool   `json:"available"`
		} `json:"apps"`
	}
	callSmithTool(t, first, "app_list", map[string]any{}, &apps)
	if len(apps.Apps) != 1 || apps.Apps[0].Path != appRoot || !apps.Apps[0].Available {
		t.Fatalf("app_list = %+v", apps.Apps)
	}

	var inspected struct {
		Revision string `json:"revision"`
		Tasks    []struct {
			Body string `json:"body"`
		} `json:"tasks"`
	}
	callSmithTool(t, first, "app_inspect", map[string]any{"app": appRoot}, &inspected)
	if inspected.Revision == "" || len(inspected.Tasks) != 1 || !strings.Contains(inspected.Tasks[0].Body, "lifecycle complete") {
		t.Fatalf("app_inspect = %+v", inspected)
	}
	var operated struct {
		AfterRevision string `json:"after_revision"`
	}
	callSmithTool(t, first, "app_operate", map[string]any{
		"app":               appRoot,
		"expected_revision": inspected.Revision,
		"operations": []map[string]any{{
			"type": "put_context", "path": "brief.md", "content": "created through MCP",
		}},
	}, &operated)
	if operated.AfterRevision == "" || operated.AfterRevision == inspected.Revision {
		t.Fatalf("app_operate revision = %q", operated.AfterRevision)
	}
	var validation struct {
		Valid bool `json:"valid"`
	}
	callSmithTool(t, first, "app_validate", map[string]any{"app": appRoot}, &validation)
	if !validation.Valid {
		t.Fatal("app_validate did not report valid")
	}

	completedID := startRun(t, first, appRoot)
	completedEvents := waitForTerminalEvent(t, first, appRoot, completedID, "run.completed")
	completedManifest := waitForRunStatus(t, first, appRoot, completedID, "success")
	if completedManifest.Status != "success" {
		t.Fatalf("completed run status = %q", completedManifest.Status)
	}
	artifactPath := publishedArtifact(t, completedEvents)
	var artifact struct {
		Content string `json:"content"`
	}
	callSmithTool(t, first, "artifact_read", map[string]any{"app": appRoot, "run_id": completedID, "path": artifactPath}, &artifact)
	if artifact.Content != "lifecycle complete\n" {
		t.Fatalf("artifact content = %q", artifact.Content)
	}

	callSmithTool(t, first, "app_inspect", map[string]any{"app": appRoot}, &inspected)
	callSmithTool(t, first, "app_operate", map[string]any{
		"app":               appRoot,
		"expected_revision": inspected.Revision,
		"operations": []map[string]any{{
			"type": "set_task", "task_id": "", "patch": map[string]any{"body": "sleep 30\nprintf 'too late\\n'"},
		}},
	}, nil)
	cancelledID := startRun(t, first, appRoot)
	waitForEvent(t, first, appRoot, cancelledID, "invocation.started")
	var cancellation struct {
		Requested bool `json:"requested"`
	}
	callSmithTool(t, first, "run_cancel", map[string]any{"app": appRoot, "run_id": cancelledID}, &cancellation)
	if !cancellation.Requested {
		t.Fatal("run_cancel did not issue the cancellation request")
	}
	waitForTerminalEvent(t, first, appRoot, cancelledID, "run.cancelled")
	cancelledManifest := waitForRunStatus(t, first, appRoot, cancelledID, "cancelled")
	if cancelledManifest.Status != "cancelled" {
		t.Fatalf("cancelled run status = %q", cancelledManifest.Status)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := connectSmithCommand(t, ctx, home)
	defer func() { _ = second.Close() }()
	var history struct {
		Runs []struct {
			RunID  string `json:"run_id"`
			Status string `json:"status"`
		} `json:"runs"`
	}
	callSmithTool(t, second, "run_list", map[string]any{"app": appRoot}, &history)
	wantHistory := map[string]string{completedID: "success", cancelledID: "cancelled"}
	for _, manifest := range history.Runs {
		want, ok := wantHistory[manifest.RunID]
		if !ok {
			continue
		}
		if manifest.Status != want {
			t.Fatalf("run %s status after restart = %q, want %q", manifest.RunID, manifest.Status, want)
		}
		delete(wantHistory, manifest.RunID)
	}
	if len(wantHistory) != 0 {
		t.Fatalf("restart history missing runs: %v; got %+v", wantHistory, history.Runs)
	}
	waitForEvent(t, second, appRoot, completedID, "run.completed")
	waitForEvent(t, second, appRoot, cancelledID, "run.cancelled")
	artifact.Content = ""
	callSmithTool(t, second, "artifact_read", map[string]any{"app": appRoot, "run_id": completedID, "path": artifactPath}, &artifact)
	if artifact.Content != "lifecycle complete\n" {
		t.Fatalf("artifact content after restart = %q", artifact.Content)
	}
}

type lifecycleManifest struct {
	Status string `json:"status"`
}

type lifecycleEvent struct {
	Type         string `json:"type"`
	Artifact     string `json:"artifact"`
	InvocationID string `json:"invocation_id"`
	Error        string `json:"error"`
	Reason       string `json:"reason"`
}

func connectSmithCommand(t *testing.T, ctx context.Context, home string) *mcp.ClientSession {
	t.Helper()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSmithMCPColdProcess")
	command.Env = append(withoutSmithRuntime(os.Environ()), "SMITH_MCP_HELPER=1", "HOME="+home)
	client := mcp.NewClient(&mcp.Implementation{Name: "lifecycle-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func startRun(t *testing.T, session *mcp.ClientSession, appRoot string) string {
	t.Helper()
	var handle struct {
		RunID string `json:"run_id"`
	}
	callSmithTool(t, session, "run_start", map[string]any{"app": appRoot, "no_cache": true}, &handle)
	if handle.RunID == "" {
		t.Fatal("run_start returned no run ID")
	}
	return handle.RunID
}

func waitForRunStatus(t *testing.T, session *mcp.ClientSession, appRoot, runID, want string) lifecycleManifest {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var manifest lifecycleManifest
		callSmithTool(t, session, "run_status", map[string]any{"app": appRoot, "run_id": runID}, &manifest)
		if manifest.Status == want {
			return manifest
		}
		if manifest.Status != "running" || time.Now().After(deadline) {
			t.Fatalf("run %s status = %q, want %q", runID, manifest.Status, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForTerminalEvent(t *testing.T, session *mcp.ClientSession, appRoot, runID, want string) []lifecycleEvent {
	t.Helper()
	return waitForEvent(t, session, appRoot, runID, want)
}

func waitForEvent(t *testing.T, session *mcp.ClientSession, appRoot, runID, want string) []lifecycleEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var cursor uint64
	var observed []lifecycleEvent
	for {
		var page struct {
			Events     []lifecycleEvent `json:"events"`
			NextCursor uint64           `json:"next_cursor"`
		}
		callSmithTool(t, session, "run_events", map[string]any{"app": appRoot, "run_id": runID, "after": cursor, "limit": 2}, &page)
		observed = append(observed, page.Events...)
		for _, event := range page.Events {
			if event.Type == want {
				return observed
			}
		}
		cursor = page.NextCursor
		if time.Now().After(deadline) {
			t.Fatalf("run %s did not emit %s; events = %+v", runID, want, observed)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func publishedArtifact(t *testing.T, events []lifecycleEvent) string {
	t.Helper()
	for _, event := range events {
		if event.Type == "artifact.published" && event.Artifact != "" {
			return event.Artifact
		}
	}
	t.Fatal("completed run published no artifact")
	return ""
}

func callSmithTool(t *testing.T, session *mcp.ClientSession, name string, arguments any, target any) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		var messages []string
		for _, content := range result.Content {
			if text, ok := content.(*mcp.TextContent); ok {
				messages = append(messages, text.Text)
			}
		}
		t.Fatalf("%s failed: structured=%s content=%q", name, data, messages)
	}
	if target == nil {
		return
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envelope.Result, target); err != nil {
		t.Fatal(err)
	}
}

func writeLifecycleFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func withoutSmithRuntime(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, value := range environment {
		if strings.HasPrefix(value, "SMITH_ANTHROPIC_API_KEY=") || strings.HasPrefix(value, "SMITH_OLLAMA_ENDPOINT=") || strings.HasPrefix(value, "HOME=") {
			continue
		}
		result = append(result, value)
	}
	return result
}
