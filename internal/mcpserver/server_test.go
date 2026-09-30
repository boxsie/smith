package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/config"
	"github.com/boxsie/smith/internal/modeldisc"
	"github.com/boxsie/smith/internal/projects"
	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProtocolSurfaceIsSharedAcrossSessions(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "task.md"), "root task")
	writeFile(t, filepath.Join(root, "agent.md"), "model: mock/test\n")
	smith := service.New(service.Dependencies{})
	server, err := New(Dependencies{
		Service: smith, Version: "test",
		Projects:   func() (*projects.Index, error) { return &projects.Index{Projects: []string{root}}, nil },
		LoadConfig: func() (config.Config, error) { return config.Config{}, nil },
		Probe: func(config.Config) *modeldisc.ProbeResult {
			return &modeldisc.ProbeResult{Ollama: modeldisc.ProviderStatus{Configured: true, Reachable: true, Models: []modeldisc.DiscoveredModel{{ID: "ollama/test:8b", Provider: "ollama", SizeTier: "small"}}}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := connect(t, server)
	defer func() { _ = first.Close() }()
	second := connect(t, server)
	defer func() { _ = second.Close() }()

	tools := map[string]*mcp.Tool{}
	for tool, listErr := range first.Tools(context.Background(), nil) {
		if listErr != nil {
			t.Fatal(listErr)
		}
		if tool.Description == "" {
			t.Fatalf("tool %s has no description", tool.Name)
		}
		tools[tool.Name] = tool
	}
	for _, name := range []string{"system_capabilities", "app_list", "app_inspect", "app_validate", "app_operate", "run_start", "run_status", "run_cancel", "run_events", "run_attempts", "run_list", "artifact_read"} {
		if tools[name] == nil {
			t.Errorf("missing tool %s", name)
		}
	}
	assertToolContract(t, tools["app_operate"], false, "expected_revision", "mutates")
	assertToolContract(t, tools["run_start"], false, "returning immediately", "clear_cache")
	assertToolContract(t, tools["run_attempts"], true, "nullable measurements", "pagination")
	assertToolContract(t, tools["artifact_read"], true, "not arbitrary filesystem access")
	resources := map[string]bool{}
	for resource, listErr := range first.Resources(context.Background(), nil) {
		if listErr != nil {
			t.Fatal(listErr)
		}
		resources[resource.URI] = true
	}
	for _, uri := range []string{"smith://system/summary", "smith://apps"} {
		if !resources[uri] {
			t.Fatalf("missing resource %s", uri)
		}
		if _, err := second.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri}); err != nil {
			t.Fatal(err)
		}
	}

	var capabilities json.RawMessage
	callOK(t, first, "system_capabilities", map[string]any{}, &capabilities)
	if !strings.Contains(string(capabilities), `"size_tier":"small"`) || strings.Contains(string(capabilities), `"SizeTier"`) {
		t.Fatalf("capabilities do not use the stable wire shape: %s", capabilities)
	}
	callOK(t, first, "app_list", map[string]any{}, nil)
	var inspected struct {
		Revision string `json:"revision"`
	}
	callOK(t, first, "app_inspect", map[string]any{"app": root}, &inspected)
	if inspected.Revision == "" {
		t.Fatal("inspect returned no revision")
	}
	callOK(t, first, "app_validate", map[string]any{"app": root}, nil)
	callOK(t, first, "app_operate", map[string]any{"app": root, "expected_revision": inspected.Revision, "operations": []map[string]any{{"type": "put_context", "path": "brief.md", "content": "brief"}}}, nil)
	var observed struct {
		Revision string `json:"revision"`
	}
	callOK(t, second, "app_inspect", map[string]any{"app": root}, &observed)
	if observed.Revision == inspected.Revision {
		t.Fatal("second session did not observe mutation")
	}

	var handle struct {
		RunID string `json:"run_id"`
	}
	callOK(t, first, "run_start", map[string]any{"app": root}, &handle)
	if handle.RunID == "" {
		t.Fatal("run_start returned no id")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var manifest struct {
			Status string `json:"status"`
		}
		callOK(t, second, "run_status", map[string]any{"app": root, "run_id": handle.RunID}, &manifest)
		if manifest.Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	var page struct {
		Events []struct{ Type, Artifact string } `json:"events"`
	}
	callOK(t, second, "run_events", map[string]any{"app": root, "run_id": handle.RunID, "limit": 100}, &page)
	artifact := ""
	for _, event := range page.Events {
		if event.Type == "artifact.published" {
			artifact = event.Artifact
		}
	}
	if artifact == "" {
		t.Fatal("no published artifact")
	}
	callOK(t, first, "artifact_read", map[string]any{"app": root, "run_id": handle.RunID, "path": artifact}, nil)
	callOK(t, first, "run_list", map[string]any{"app": root}, nil)
	callOK(t, first, "run_cancel", map[string]any{"app": root, "run_id": handle.RunID}, nil)
}

func TestRunAttemptsProjectsTypedLimitEvidenceOverMCP(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "task.md"), "root task")
	writeFile(t, filepath.Join(root, "agent.md"), "model: mock/test\n")
	runID := "attempt-inspection"
	store, err := run.NewEventStore(run.RunDir(root, runID), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := runtime.ResolveAttemptPolicy(runtime.AttemptPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	taskInvocationID := runID + "/i-000001"
	attemptID := run.AttemptID(taskInvocationID, 1)
	spec := run.AttemptSpec{
		ID: attemptID, Ordinal: 1, TaskInvocationID: taskInvocationID, TaskID: "task",
		Runtime: "codex", Model: "gpt-test", InputSHA256: "input-hash",
		ContainmentProfile: runtime.ContainmentAdmission{
			RequestedProfile: runtime.ExecutionProfileLocalSubscription, Mechanism: runtime.ContainmentSystemdUser, Enforced: true,
			EffectiveLimits: runtime.LimitPolicy{Timeout: "5m", MaxMemoryBytes: 64 << 20, MaxProcesses: 16},
		},
		ControllerPolicy: policy,
	}
	appendEvent := func(event run.Event) {
		t.Helper()
		if _, err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	condition := func(status run.AttemptStatus, reason string, limit *run.AttemptLimit) *run.AttemptCondition {
		return &run.AttemptCondition{Status: status, Reason: reason, Limit: limit}
	}
	appendEvent(run.Event{Type: run.EventRunQueued, RunID: runID, AppRoot: root, TaskIDs: []string{"task"}})
	appendEvent(run.Event{Type: run.EventAttemptPending, RunID: runID, InvocationID: attemptID, ParentInvocationID: taskInvocationID, TaskID: "task", AttemptID: attemptID, AttemptOrdinal: 1, AttemptSpec: &spec, AttemptCondition: condition(run.AttemptPending, "attempt_created", nil)})
	commandData, err := json.Marshal(runtime.ExternalToolRecord{
		Schema: runtime.ExternalToolSchema, ItemID: "command-1", Kind: "command_execution", State: "item.started", Status: "in_progress",
		Command: &runtime.RecordedText{Text: "bash -lc 'worker'", OriginalBytes: 19, SHA256: "command-hash"},
		CWD:     &runtime.RecordedText{Text: root, OriginalBytes: len(root), SHA256: "cwd-hash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(run.Event{Type: run.EventRuntimeEmitted, RunID: runID, InvocationID: attemptID, RuntimeEvent: "codex.tool.command", RuntimeData: commandData})
	processData, err := json.Marshal(runtime.ExternalProcessRecord{
		Schema: runtime.ExternalProcessSchema, Status: "failed", TerminalReason: runtime.TerminalMemoryLimit,
		CWD: runtime.RecordedText{Text: root}, Stdout: runtime.RecordedText{Text: "partial protocol"}, Stderr: runtime.RecordedText{Text: "result: oom-kill"},
		Measurements: runtime.ResourceMeasurements{UnavailableReason: "fixture runner exposes the breach but not peak counters"},
	})
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(run.Event{Type: run.EventRuntimeEmitted, RunID: runID, InvocationID: attemptID, RuntimeEvent: "runtime.process.completed", RuntimeData: processData})
	appendEvent(run.Event{Type: run.EventAttemptTerminal, RunID: runID, InvocationID: attemptID, ParentInvocationID: taskInvocationID, TaskID: "task", AttemptID: attemptID, AttemptOrdinal: 1, AttemptCondition: condition(run.AttemptTerminal, runtime.TerminalMemoryLimit, &run.AttemptLimit{Name: "memory", Value: 64 << 20})})
	appendEvent(run.Event{Type: run.EventRunFailed, RunID: runID, Error: "memory boundary reached"})

	server, err := New(Dependencies{
		Service:  service.New(service.Dependencies{}),
		Projects: func() (*projects.Index, error) { return &projects.Index{Projects: []string{root}}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	client := connect(t, server)
	defer func() { _ = client.Close() }()
	var page run.AttemptInspectionPage
	callOK(t, client, "run_attempts", map[string]any{"app": root, "run_id": runID, "limit": 10}, &page)
	if len(page.Attempts) != 1 || page.Attempts[0].TerminalReason == nil || *page.Attempts[0].TerminalReason != runtime.TerminalMemoryLimit ||
		page.Attempts[0].Limit == nil || page.Attempts[0].Limit.Name != "memory" || page.Attempts[0].Limit.Value != 64<<20 ||
		page.Attempts[0].Measurements.PeakMemoryBytes != nil || page.Attempts[0].Measurements.UnavailableReason == "" ||
		page.Attempts[0].Process == nil || page.Attempts[0].Process.Record.Stderr.Text != "result: oom-kill" ||
		len(page.Attempts[0].Commands) != 1 || page.Attempts[0].Commands[0].Record.Command == nil || page.Attempts[0].Commands[0].Record.Command.Text != "bash -lc 'worker'" {
		t.Fatalf("attempt page = %#v", page)
	}
	wire, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), `"peak_memory_bytes":null`) || !strings.Contains(string(wire), `"peak_tasks":null`) || !strings.Contains(string(wire), `"cpu_time_ms":null`) {
		t.Fatalf("unavailable measurements were not explicit nulls: %s", wire)
	}
}

func TestStructuredRevisionError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "task.md"), "root")
	writeFile(t, filepath.Join(root, "agent.md"), "model: mock/test\n")
	server, _ := New(Dependencies{Service: service.New(service.Dependencies{}), Projects: func() (*projects.Index, error) { return &projects.Index{Projects: []string{root}}, nil }})
	client := connect(t, server)
	defer func() { _ = client.Close() }()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "app_operate", Arguments: map[string]any{"app": root, "expected_revision": "sha256:stale", "operations": []map[string]any{{"type": "put_context", "path": "x", "content": "x"}}}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	if !result.IsError || !json.Valid(data) || !contains(string(data), `"code":"revision_conflict"`) {
		t.Fatalf("unstructured error: %s", data)
	}
}

func TestStructuredErrorClasses(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "task.md"), "root")
	writeFile(t, filepath.Join(root, "agent.md"), "model: mock/test\n")
	server, err := New(Dependencies{Service: service.New(service.Dependencies{}), Projects: func() (*projects.Index, error) { return &projects.Index{Projects: []string{root}}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	client := connect(t, server)
	defer func() { _ = client.Close() }()
	if code := callErrorCode(t, client, "app_inspect", map[string]any{"app": filepath.Join(root, "missing")}); code != "unknown_app" {
		t.Fatalf("unknown app code = %q", code)
	}
	if code := callErrorCode(t, client, "run_status", map[string]any{"app": root, "run_id": "missing"}); code != "unknown_run" {
		t.Fatalf("unknown run code = %q", code)
	}
	if err := os.Remove(filepath.Join(root, "agent.md")); err != nil {
		t.Fatal(err)
	}
	if code := callErrorCode(t, client, "app_validate", map[string]any{"app": root}); code != "validation_failed" {
		t.Fatalf("validation code = %q", code)
	}
	writeFile(t, filepath.Join(root, "agent.md"), "model: unavailable/model\n")
	if code := callErrorCode(t, client, "app_validate", map[string]any{"app": root}); code != "runtime_unavailable" {
		t.Fatalf("runtime code = %q", code)
	}
}

func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func callOK(t *testing.T, session *mcp.ClientSession, name string, arguments any, target any) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		message := ""
		if len(result.Content) > 0 {
			if text, ok := result.Content[0].(*mcp.TextContent); ok {
				message = text.Text
			}
		}
		t.Fatalf("%s: %s", name, message)
	}
	if target == nil {
		return
	}
	data, _ := json.Marshal(result.StructuredContent)
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

func callErrorCode(t *testing.T, session *mcp.ClientSession, name string, arguments any) string {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("%s unexpectedly succeeded: %s", name, data)
	}
	return envelope.Error.Code
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertToolContract(t *testing.T, tool *mcp.Tool, readOnly bool, phrases ...string) {
	t.Helper()
	if tool == nil {
		t.Fatal("tool is missing")
	}
	if tool.Annotations == nil || tool.Annotations.ReadOnlyHint != readOnly {
		t.Fatalf("tool %s read-only annotation is not %v", tool.Name, readOnly)
	}
	for _, phrase := range phrases {
		if !strings.Contains(strings.ToLower(tool.Description), phrase) {
			t.Errorf("tool %s description does not mention %q: %s", tool.Name, phrase, tool.Description)
		}
	}
}

func contains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
