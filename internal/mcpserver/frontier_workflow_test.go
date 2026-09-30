package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/projects"
	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	designJSON    = `{"concept":"typed handoff","constraint":"explicit context only"}`
	challengeJSON = `{"risk":"implicit state","mitigation":"typed artifacts"}`
	synthesisJSON = `{"decision":"ship the typed workflow","rationale":"both inputs are explicit"}`
)

type frontierProcessRunner struct {
	t       *testing.T
	adapter string
	output  string

	mu       sync.Mutex
	requests []runtime.ProcessRequest
}

func (f *frontierProcessRunner) Run(_ context.Context, request runtime.ProcessRequest) (runtime.ProcessResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.mu.Unlock()

	for _, value := range request.Env {
		name := strings.SplitN(value, "=", 2)[0]
		if slices.Contains([]string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CODEX_API_KEY", "XAI_API_KEY", "GROK_DEPLOYMENT_KEY"}, name) {
			f.t.Errorf("%s fixture received API credential %s", f.adapter, name)
		}
	}
	if f.adapter == runtime.GrokRuntimeName {
		promptPath := argumentValue(request.Args, "--prompt-file")
		prompt, err := os.ReadFile(promptPath)
		if err != nil {
			return runtime.ProcessResult{}, err
		}
		request.Stdin = prompt
	}
	lines := frontierFixtureLines(f.adapter, f.output)
	for _, line := range lines {
		if err := request.StdoutLine([]byte(line)); err != nil {
			return runtime.ProcessResult{}, err
		}
	}
	return runtime.ProcessResult{Stdout: []byte(strings.Join(lines, "\n") + "\n")}, nil
}

func (f *frontierProcessRunner) prompt() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return ""
	}
	request := f.requests[len(f.requests)-1]
	if f.adapter != runtime.GrokRuntimeName {
		return string(request.Stdin)
	}
	promptPath := argumentValue(request.Args, "--prompt-file")
	data, _ := os.ReadFile(promptPath)
	return string(data)
}

func (f *frontierProcessRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func frontierFixtureLines(adapter, output string) []string {
	switch adapter {
	case runtime.ClaudeRuntimeName:
		return []string{
			`{"type":"system","subtype":"init","session_id":"frontier-claude-session","tools":["StructuredOutput"],"mcp_servers":[],"model":"claude-fable-5-1","permissionMode":"plan","slash_commands":[],"apiKeySource":"none","claude_code_version":"fixture","skills":[],"plugins":[]}`,
			fmt.Sprintf(`{"type":"result","subtype":"success","is_error":false,"session_id":"frontier-claude-session","duration_ms":1,"num_turns":1,"terminal_reason":"completed","usage":{"input_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":5},"modelUsage":{"claude-fable-5-1":{"canonicalModel":"claude-fable-5-1","costBasis":"list"}},"permission_denials":[],"result":%s,"structured_output":%s}`, mustJSON(output), output),
		}
	case runtime.GrokRuntimeName:
		return []string{
			`{"type":"available_commands","tools":[],"commands":[]}`,
			fmt.Sprintf(`{"type":"text","data":%s}`, mustJSON(output)),
			fmt.Sprintf(`{"type":"end","stopReason":"end_turn","sessionId":"frontier-grok-session","requestId":"fixture","usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8},"num_turns":1,"modelUsage":{"grok-4.5-build":{"inputTokens":3,"outputTokens":5,"modelCalls":1}},"structuredOutput":%s}`, output),
		}
	case runtime.CodexRuntimeName:
		wire, err := json.Marshal(map[string]string{"result_json": output})
		if err != nil {
			panic(err)
		}
		return []string{
			`{"type":"thread.started","thread_id":"frontier-codex-session"}`,
			`{"type":"turn.started"}`,
			fmt.Sprintf(`{"type":"item.completed","item":{"id":"result","type":"agent_message","text":%s}}`, mustJSON(string(wire))),
			`{"type":"turn.completed","usage":{"input_tokens":6,"cached_input_tokens":0,"output_tokens":8}}`,
		}
	default:
		panic("unknown frontier fixture adapter " + adapter)
	}
}

func mustJSON(value string) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func argumentValue(arguments []string, name string) string {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == name {
			return arguments[index+1]
		}
	}
	return ""
}

func TestTypedFrontierWorkflowEntirelyThroughMCP(t *testing.T) {
	claude := &frontierProcessRunner{t: t, adapter: runtime.ClaudeRuntimeName, output: designJSON}
	grok := &frontierProcessRunner{t: t, adapter: runtime.GrokRuntimeName, output: challengeJSON}
	codex := &frontierProcessRunner{t: t, adapter: runtime.CodexRuntimeName, output: synthesisJSON}
	factory := &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{
		runtime.ClaudeRuntimeName: &runtime.ClaudeRuntime{Runner: claude, Executable: "claude-fixture", Environment: []string{"HOME=/fixture"}, Admitter: frontierAdmitter()},
		runtime.GrokRuntimeName:   &runtime.GrokRuntime{Runner: grok, Executable: "grok-fixture", Environment: []string{"HOME=/fixture"}, Admitter: frontierAdmitter()},
		runtime.CodexRuntimeName:  &runtime.CodexRuntime{Runner: codex, Executable: "codex-fixture", Environment: []string{"HOME=/fixture"}, Admitter: frontierAdmitter()},
	}}

	root, session := frontierSession(t, factory)
	defer func() { _ = session.Close() }()
	materializeFrontierApp(t, session, root)
	first := runFrontierApp(t, session, root, true)
	assertFrontierRun(t, session, root, first, false, true)

	if !strings.Contains(codex.prompt(), canonicalJSON(t, designJSON)) || !strings.Contains(codex.prompt(), canonicalJSON(t, challengeJSON)) {
		t.Fatalf("Codex did not receive both exact typed artifacts:\n%s", codex.prompt())
	}
	if claude.count() != 1 || grok.count() != 1 || codex.count() != 1 {
		t.Fatalf("first-run adapter calls = claude:%d grok:%d codex:%d", claude.count(), grok.count(), codex.count())
	}

	second := runFrontierApp(t, session, root, false)
	assertFrontierRun(t, session, root, second, true, true)
	if claude.count() != 1 || grok.count() != 1 || codex.count() != 1 {
		t.Fatalf("cached run invoked adapters again = claude:%d grok:%d codex:%d", claude.count(), grok.count(), codex.count())
	}
}

func frontierAdmitter() runtime.ContainmentAdmitter {
	return runtime.ContainmentAdmitterFunc(func(_ context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
		return runtime.ContainmentAdmission{RequestedProfile: request.Profile, Mechanism: "test_containment", EffectiveLimits: request.Limits, Enforced: true}, nil
	})
}

func TestTypedFrontierWorkflowLiveSubscriptions(t *testing.T) {
	if os.Getenv("SMITH_LIVE_FRONTIER_WORKFLOW") != "1" {
		t.Skip("set SMITH_LIVE_FRONTIER_WORKFLOW=1 to spend one Claude, Grok, and Codex subscription call")
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CODEX_API_KEY", "XAI_API_KEY", "GROK_DEPLOYMENT_KEY"} {
		t.Setenv(key, "")
	}
	repoBefore := gitStatus(t)
	root, session := frontierSession(t, runtime.DefaultExternalFactory())
	defer func() { _ = session.Close() }()
	materializeFrontierApp(t, session, root)
	runID := runFrontierApp(t, session, root, true)
	assertFrontierRun(t, session, root, runID, false, false)
	if after := gitStatus(t); after != repoBefore {
		t.Fatalf("live MCP proof modified the Smith repository:\nbefore:\n%s\nafter:\n%s", repoBefore, after)
	}
}

func frontierSession(t *testing.T, factory *runtime.ExternalFactory) (string, *mcp.ClientSession) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "task.md"), "printf 'frontier workflow brief\\n'")
	writeFile(t, filepath.Join(root, "agent.md"), "model: shell\n")
	smith := service.New(service.Dependencies{ExternalFactory: factory, TrackProject: func(string) error { return nil }})
	server, err := New(Dependencies{Service: smith, Projects: func() (*projects.Index, error) {
		return &projects.Index{Projects: []string{root}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return root, connect(t, server)
}

func materializeFrontierApp(t *testing.T, session *mcp.ClientSession, root string) {
	t.Helper()
	var inspected struct {
		Revision string `json:"revision"`
	}
	callOK(t, session, "app_inspect", map[string]any{"app": root}, &inspected)
	objectSchema := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	}
	stringProperty := func() map[string]any { return map[string]any{"type": "string"} }
	operations := []map[string]any{
		{
			"type": "add_task", "task_id": "design",
			"task": map[string]any{
				"body":        "Design one small typed frontier-model workflow from the explicit brief. Return only the requested JSON.",
				"output_type": "json",
				"schema":      objectSchema(map[string]any{"concept": stringProperty(), "constraint": stringProperty()}, "concept", "constraint"),
				"agent":       map[string]any{"runtime": "claude", "model": "claude-fable-5-1", "profile": "reason", "session": map[string]any{"mode": "fresh"}},
			},
		},
		{
			"type": "add_task", "task_id": "challenge",
			"task": map[string]any{
				"body":        "Challenge the explicit brief with one concrete risk and mitigation. Return only the requested JSON.",
				"output_type": "json",
				"schema":      objectSchema(map[string]any{"risk": stringProperty(), "mitigation": stringProperty()}, "risk", "mitigation"),
				"agent":       map[string]any{"runtime": "grok", "model": "grok-4.5", "profile": "reason", "session": map[string]any{"mode": "fresh"}, "limits": map[string]any{"max_turns": 1}},
			},
		},
		{
			"type": "add_task", "task_id": "synthesis",
			"task": map[string]any{
				"body":        "Synthesize the exact typed design and challenge artifacts supplied as dependencies. Return only the requested JSON.",
				"depends_on":  []string{"design", "challenge"},
				"output_type": "json",
				"schema":      objectSchema(map[string]any{"decision": stringProperty(), "rationale": stringProperty()}, "decision", "rationale"),
				"agent":       map[string]any{"runtime": "codex", "model": "gpt-5.6-sol", "profile": "inspect", "workspace": "root", "session": map[string]any{"mode": "fresh"}},
			},
		},
	}
	callOK(t, session, "app_operate", map[string]any{
		"app": root, "expected_revision": inspected.Revision, "operations": operations,
	}, nil)
	var validation struct {
		Valid bool `json:"valid"`
	}
	callOK(t, session, "app_validate", map[string]any{"app": root}, &validation)
	if !validation.Valid {
		t.Fatal("materialized frontier app is invalid")
	}
}

func runFrontierApp(t *testing.T, session *mcp.ClientSession, root string, clearCache bool) string {
	t.Helper()
	var handle struct {
		RunID string `json:"run_id"`
	}
	callOK(t, session, "run_start", map[string]any{"app": root, "clear_cache": clearCache}, &handle)
	if handle.RunID == "" {
		t.Fatal("run_start returned no run ID")
	}
	return handle.RunID
}

func assertFrontierRun(t *testing.T, session *mcp.ClientSession, root, runID string, cached, exact bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Minute)
	var manifest run.Manifest
	for {
		callOK(t, session, "run_status", map[string]any{"app": root, "run_id": runID}, &manifest)
		if manifest.Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("frontier run %s did not finish", runID)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if manifest.Status != "success" || len(manifest.Tasks) != 4 {
		t.Fatalf("frontier manifest = %#v", manifest)
	}
	wantAdapters := map[string]string{"design": "claude", "challenge": "grok", "synthesis": "codex"}
	for _, task := range manifest.Tasks {
		wantStatus := "success"
		if cached {
			wantStatus = "cached"
		}
		if task.Status != wantStatus || task.Cached != cached {
			t.Fatalf("task %q status = %q cached=%v, want %q cached=%v", task.TaskID, task.Status, task.Cached, wantStatus, cached)
		}
		adapter, external := wantAdapters[task.TaskID]
		if cached || !external {
			continue
		}
		if len(task.RuntimeRecords) != 1 || task.RuntimeRecords[0].Adapter != adapter {
			t.Fatalf("task %q runtime records = %#v", task.TaskID, task.RuntimeRecords)
		}
		record := task.RuntimeRecords[0]
		if record.SessionID == nil || *record.SessionID == "" || record.Profile.Session.Mode != runtime.SessionFresh {
			t.Fatalf("task %q session provenance = %#v", task.TaskID, record)
		}
		if task.TaskID == "synthesis" && (record.Profile.Name != runtime.CapabilityInspect || record.Profile.Workspace.Access != runtime.WorkspaceReadOnly) {
			t.Fatalf("Codex synthesis profile = %#v", record.Profile)
		}
	}

	events := frontierEvents(t, session, root, runID)
	runtimeCompletions := 0
	artifacts := map[string]string{}
	for _, event := range events {
		if event.Type == run.EventRuntimeCompleted {
			runtimeCompletions++
		}
		if event.Type != run.EventArtifactPublished || event.Artifact == "" {
			continue
		}
		var artifact struct {
			Content string `json:"content"`
		}
		callOK(t, session, "artifact_read", map[string]any{"app": root, "run_id": runID, "path": event.Artifact}, &artifact)
		artifacts[event.TaskID] = strings.TrimSpace(artifact.Content)
	}
	if len(artifacts) != 4 {
		t.Fatalf("read %d published artifacts, want 4: %#v", len(artifacts), artifacts)
	}
	if cached && runtimeCompletions != 0 {
		t.Fatalf("cached run emitted %d runtime completions", runtimeCompletions)
	}
	if !cached && runtimeCompletions != 3 {
		t.Fatalf("frontier run emitted %d runtime completions, want 3", runtimeCompletions)
	}
	if exact && (artifacts["design"] != canonicalJSON(t, designJSON) || artifacts["challenge"] != canonicalJSON(t, challengeJSON) || artifacts["synthesis"] != canonicalJSON(t, synthesisJSON)) {
		t.Fatalf("typed artifacts = %#v", artifacts)
	}
	for _, taskID := range []string{"design", "challenge", "synthesis"} {
		if !json.Valid([]byte(artifacts[taskID])) {
			t.Fatalf("task %q artifact is not JSON: %q", taskID, artifacts[taskID])
		}
	}
	var history struct {
		Runs []run.Manifest `json:"runs"`
	}
	callOK(t, session, "run_list", map[string]any{"app": root}, &history)
	if !slices.ContainsFunc(history.Runs, func(item run.Manifest) bool { return item.RunID == runID }) {
		t.Fatalf("run_list omitted %s", runID)
	}
}

func canonicalJSON(t *testing.T, value string) string {
	t.Helper()
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func frontierEvents(t *testing.T, session *mcp.ClientSession, root, runID string) []run.Event {
	t.Helper()
	var all []run.Event
	var cursor uint64
	for {
		var page run.EventPage
		callOK(t, session, "run_events", map[string]any{"app": root, "run_id": runID, "after": cursor, "limit": 2}, &page)
		all = append(all, page.Events...)
		cursor = page.NextCursor
		if !page.HasMore {
			return all
		}
	}
}

func gitStatus(t *testing.T) string {
	t.Helper()
	command := exec.Command("git", "-C", filepath.Join("..", ".."), "status", "--porcelain=v1", "--untracked-files=all")
	data, err := command.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	return string(data)
}
