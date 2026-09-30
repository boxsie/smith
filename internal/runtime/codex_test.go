package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type codexSink struct {
	events []RuntimeEvent
	result *ExternalResult
}

func (s *codexSink) Emit(_ context.Context, event RuntimeEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *codexSink) Complete(_ context.Context, result *ExternalResult) error {
	if s.result != nil {
		return errors.New("duplicate completion")
	}
	s.result = result
	return nil
}

func codexInvocation(_ string) Invocation {
	return Invocation{
		Messages: []Message{{Role: "user", Text: "Return the answer."}},
		Persona:  "You are exact.",
		Output: OutputContract{
			Type:   "json",
			Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`),
		},
		Runtime:      CodexRuntimeName,
		Model:        "gpt-5.6-sol",
		Profile:      DefaultProfile,
		Workspace:    WorkspacePolicy{Access: WorkspaceNone, Isolation: "ephemeral", Granted: true},
		Capabilities: CapabilityPolicy{Profile: CapabilityReason},
		Session:      SessionPolicy{Mode: SessionFresh},
	}
}

func codexFixtureRunner(t *testing.T, name string, inspect func(ProcessRequest)) ProcessRunner {
	t.Helper()
	fixture, err := os.ReadFile("testdata/codex/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return processRunnerFunc(func(ctx context.Context, request ProcessRequest) (ProcessResult, error) {
		if inspect != nil {
			inspect(request)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(fixture)), "\n") {
			if err := request.StdoutLine([]byte(line)); err != nil {
				return ProcessResult{Stdout: fixture}, err
			}
		}
		return ProcessResult{Stdout: fixture}, nil
	})
}

func TestCodexRuntimeParsesObservedStreamAndUsesSterileArgv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("CODEX_API_KEY", "must-not-leak")
	invocation := codexInvocation(t.TempDir())
	var request ProcessRequest
	runtime := &CodexRuntime{Executable: "codex-test"}
	runtime.Runner = codexFixtureRunner(t, "success.jsonl", func(got ProcessRequest) {
		request = got
		schemaPath := argValue(t, got.Args, "--output-schema")
		schema, err := os.ReadFile(schemaPath)
		if err != nil {
			t.Fatalf("read live schema file: %v", err)
		}
		if string(schema) != string(codexWireSchema) {
			t.Fatalf("schema = %s", schema)
		}
		if !strings.Contains(string(got.Stdin), string(invocation.Output.Schema)) || !strings.Contains(string(got.Stdin), "`result_json`") {
			t.Fatalf("prompt omitted original output contract: %s", got.Stdin)
		}
	})
	sink := &codexSink{}

	if err := runtime.Invoke(context.Background(), invocation, sink); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if request.Executable != "codex-test" || !strings.Contains(request.Dir, "smith-sterile-") {
		t.Fatalf("process request = %#v", request)
	}
	if _, err := os.Stat(request.Dir); !os.IsNotExist(err) {
		t.Fatalf("sterile workspace was not removed: %v", err)
	}
	if got := string(request.Stdin); !strings.HasPrefix(got, "Return the answer.\n\n## smith structured output") {
		t.Fatalf("stdin = %q", got)
	}
	if slices.Contains(request.Args, "Return the answer.") {
		t.Fatal("user prompt leaked into argv")
	}
	if len(request.Args) == 0 || request.Args[0] != "exec" || request.Args[len(request.Args)-1] != "-" {
		t.Fatalf("argv = %v", request.Args)
	}
	for _, required := range []string{
		"--strict-config", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check",
		"--json", "--output-schema", "--sandbox", "--cd", "--ephemeral",
	} {
		if !slices.Contains(request.Args, required) {
			t.Errorf("argv missing %q: %v", required, request.Args)
		}
	}
	assertArgPair(t, request.Args, "--sandbox", "read-only")
	assertArgPair(t, request.Args, "--cd", request.Dir)
	assertArgPair(t, request.Args, "--model", invocation.Model)
	for _, config := range []string{
		`forced_login_method="chatgpt"`,
		"developer_instructions=" + tomlString(invocation.Persona),
		"project_doc_max_bytes=0",
		"project_doc_fallback_filenames=[]",
		`web_search="disabled"`,
		`shell_environment_policy.inherit="none"`,
		`shell_environment_policy.set.PATH="` + environmentValue(request.Env, "PATH") + `"`,
		`shell_environment_policy.set.BASH_ENV="/dev/null"`,
		`shell_environment_policy.set.ENV="/dev/null"`,
		"allow_login_shell=false",
	} {
		assertArgPair(t, request.Args, "--config", config)
	}
	for _, feature := range []string{"apps", "hooks", "multi_agent", "plugins", "shell_tool", "skill_search", "view_image"} {
		assertArgPair(t, request.Args, "--disable", feature)
	}
	for _, value := range request.Env {
		if strings.HasPrefix(value, "OPENAI_API_KEY=") || strings.HasPrefix(value, "CODEX_API_KEY=") {
			t.Fatalf("API key leaked into subscription runtime: %s", value)
		}
	}
	if sink.result == nil || string(sink.result.JSON) != `{"answer":42}` {
		t.Fatalf("result = %#v", sink.result)
	}
	if sink.result.Provenance.Adapter != CodexRuntimeName || sink.result.Provenance.RequestedModel != invocation.Model ||
		sink.result.Provenance.CanonicalModel != "" || sink.result.Provenance.BillingBasis != BillingSubscription ||
		sink.result.Provenance.AdapterVersion != "4" || sink.result.Provenance.ProtocolVersion != "codex-exec-jsonl/1" || sink.result.Provenance.CLIVersion != "" {
		t.Fatalf("provenance = %#v", sink.result.Provenance)
	}
	if sink.result.Provenance.SessionID != "01a06237-6337-7792-9215-9264629a6cc6" || sink.result.Provenance.TerminalReason != "completed" {
		t.Fatalf("terminal provenance = %#v", sink.result.Provenance)
	}
	if sink.result.Usage.InputTokens == nil || *sink.result.Usage.InputTokens != 15417 ||
		sink.result.Usage.CachedInputTokens == nil || *sink.result.Usage.CachedInputTokens != 0 ||
		sink.result.Usage.OutputTokens == nil || *sink.result.Usage.OutputTokens != 17 ||
		sink.result.Usage.Turns == nil || *sink.result.Usage.Turns != 1 {
		t.Fatalf("usage = %#v", sink.result.Usage)
	}
	if sink.result.Duration <= 0 {
		t.Fatalf("duration = %v", sink.result.Duration)
	}
	gotEvents := make([]string, len(sink.events))
	for index, event := range sink.events {
		gotEvents[index] = event.Type
	}
	wantEvents := []string{"codex.session.started", "codex.turn.started", "codex.agent.message", "codex.turn.completed", "runtime.process.completed"}
	if !slices.Equal(gotEvents, wantEvents) {
		t.Fatalf("events = %v, want %v", gotEvents, wantEvents)
	}
	var process ExternalProcessRecord
	if err := json.Unmarshal(sink.events[len(sink.events)-1].Data, &process); err != nil {
		t.Fatal(err)
	}
	if process.Schema != ExternalProcessSchema || process.Status != "completed" || process.TerminalReason != TerminalSuccess ||
		process.ExitCode == nil || *process.ExitCode != 0 || process.CWD.Text != request.Dir || process.Measurements.PeakMemoryBytes != nil ||
		process.Measurements.UnavailableReason == "" {
		t.Fatalf("process record = %#v", process)
	}
}

func TestCodexSterileEnvironmentSeparatesAuthFromShellHome(t *testing.T) {
	authHome := t.TempDir()
	environment, cleanup, err := codexSterileEnvironment([]string{
		"HOME=" + authHome,
		"PATH=/usr/bin",
		"XDG_CONFIG_HOME=/home/user/.config",
	})
	if err != nil {
		t.Fatal(err)
	}
	sterileHome := environmentValue(environment, "HOME")
	if sterileHome == "" || sterileHome == authHome {
		t.Fatalf("sterile HOME = %q", sterileHome)
	}
	if got := environmentValue(environment, "CODEX_HOME"); got != filepath.Join(authHome, ".codex") {
		t.Fatalf("CODEX_HOME = %q", got)
	}
	if got := environmentValue(environment, "XDG_CONFIG_HOME"); got != filepath.Join(sterileHome, ".config") {
		t.Fatalf("XDG_CONFIG_HOME = %q", got)
	}
	cleanup()
	if _, err := os.Stat(sterileHome); !os.IsNotExist(err) {
		t.Fatalf("sterile home remained after cleanup: %v", err)
	}
}

func TestCodexRuntimeRecordsBoundedRedactedToolProvenance(t *testing.T) {
	sink := &codexSink{}
	stream := &codexStream{sink: sink, profile: CapabilityWork, cwd: "/work/project"}
	exitCode := 17
	longOutput := "begin OPENAI_API_KEY=sk-outputsecret\n" + strings.Repeat("x", externalOutputBytes) + "\nuseful tail"
	item := codexItem{
		ID:               "command-1",
		Type:             "command_execution",
		Command:          `curl -u operator:basic-secret -H "Authorization: Bearer bearer-secret" --token=flag-secret https://user:password@example.test/run`,
		AggregatedOutput: longOutput,
		ExitCode:         &exitCode,
		Status:           "failed",
	}
	if err := stream.consumeItem(context.Background(), "item.completed", item); err != nil {
		t.Fatal(err)
	}
	changes := make([]codexFileChange, externalMaxChanges+1)
	changes[0] = codexFileChange{Path: "config/API_TOKEN=path-secret", Kind: "update"}
	for index := 1; index < len(changes); index++ {
		changes[index] = codexFileChange{Path: fmt.Sprintf("generated/%03d.txt", index), Kind: "add"}
	}
	if err := stream.consumeItem(context.Background(), "item.completed", codexItem{
		ID: "change-1", Type: "file_change", Status: "completed",
		Changes: changes,
	}); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 2 {
		t.Fatalf("events = %d", len(sink.events))
	}

	var command ExternalToolRecord
	if err := json.Unmarshal(sink.events[0].Data, &command); err != nil {
		t.Fatal(err)
	}
	if command.Schema != ExternalToolSchema || command.ItemID != item.ID || command.State != "item.completed" ||
		command.Status != item.Status || command.ExitCode == nil || *command.ExitCode != exitCode {
		t.Fatalf("command record = %#v", command)
	}
	if command.Command == nil || command.CWD == nil || command.Output == nil {
		t.Fatalf("command record omitted provenance: %#v", command)
	}
	if command.CWD.Text != "/work/project" || command.CWD.Truncated || command.CWD.Redacted {
		t.Fatalf("cwd = %#v", command.CWD)
	}
	for _, forbidden := range []string{"basic-secret", "bearer-secret", "flag-secret", "password", "sk-outputsecret"} {
		if strings.Contains(string(sink.events[0].Data), forbidden) {
			t.Fatalf("credential %q leaked in %s", forbidden, sink.events[0].Data)
		}
	}
	if !command.Command.Redacted || !command.Output.Redacted || !command.Output.Truncated ||
		len(command.Output.Text) > externalOutputBytes || !strings.Contains(command.Output.Text, "useful tail") {
		t.Fatalf("bounded command record = %#v", command)
	}

	var change ExternalToolRecord
	if err := json.Unmarshal(sink.events[1].Data, &change); err != nil {
		t.Fatal(err)
	}
	if len(change.Changes) != externalMaxChanges || change.ChangesOmitted != 1 || change.Changes[0].Kind != "update" || !change.Changes[0].Path.Redacted ||
		strings.Contains(change.Changes[0].Path.Text, "path-secret") {
		t.Fatalf("file change record = %#v", change)
	}
}

func TestCodexRuntimeInjectsOnlyInvocationMCPTools(t *testing.T) {
	invocation := codexInvocation(t.TempDir())
	invocation.MCPServers = []MCPServer{{Name: "tickets_please", URL: "http://127.0.0.1:3210/mcp/token", Tools: []string{"get_ticket", "list_comments"}, Capability: "tickets_please.read"}}
	invocation.Capabilities.Allow = []string{"tickets_please.read"}
	args := codexCommonArgs(invocation, "/tmp/schema.json", "/usr/bin")
	for _, config := range []string{
		`mcp_servers.tickets_please.url="http://127.0.0.1:3210/mcp/token"`,
		"mcp_servers.tickets_please.required=true",
		`mcp_servers.tickets_please.enabled_tools=["get_ticket","list_comments"]`,
		`mcp_servers.tickets_please.default_tools_approval_mode="approve"`,
	} {
		assertArgPair(t, args, "--config", config)
	}
	stream := &codexStream{sink: &codexSink{}, profile: invocation.Profile, mcpServers: invocation.MCPServers}
	if err := stream.consumeItem(context.Background(), "item.completed", codexItem{ID: "mcp-1", Type: "mcp_tool_call"}); err != nil {
		t.Fatalf("configured MCP call rejected: %v", err)
	}
	stream.mcpServers = nil
	if err := stream.consumeItem(context.Background(), "item.completed", codexItem{ID: "mcp-2", Type: "mcp_tool_call"}); !errors.Is(err, ErrCodexUndeclaredTool) {
		t.Fatalf("ambient MCP call error = %v", err)
	}
}

func TestCodexRuntimeSessionModes(t *testing.T) {
	sessionID := "0199a213-81c0-7800-8aa1-bbab2a035a53"
	tests := []struct {
		name       string
		session    SessionPolicy
		wantPrefix []string
		wantID     bool
		wantFresh  bool
	}{
		{name: "fresh", session: SessionPolicy{Mode: SessionFresh}, wantPrefix: []string{"exec"}, wantFresh: true},
		{name: "sticky new", session: SessionPolicy{Mode: SessionSticky}, wantPrefix: []string{"exec"}},
		{name: "resume", session: SessionPolicy{Mode: SessionSticky, ID: sessionID}, wantPrefix: []string{"exec", "resume"}, wantID: true},
		{name: "fork", session: SessionPolicy{Mode: SessionFork, ID: sessionID}, wantPrefix: []string{"exec", "fork"}, wantID: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invocation := codexInvocation(t.TempDir())
			invocation.Session = test.session
			runtime := &CodexRuntime{Executable: "codex", Runner: codexFixtureRunner(t, "success.jsonl", func(request ProcessRequest) {
				if !slices.Equal(request.Args[:len(test.wantPrefix)], test.wantPrefix) {
					t.Fatalf("argv prefix = %v, want %v", request.Args, test.wantPrefix)
				}
				if slices.Contains(request.Args, "--ephemeral") != test.wantFresh {
					t.Fatalf("ephemeral argv = %v", request.Args)
				}
				if test.wantID && !slices.Contains(request.Args, sessionID) {
					t.Fatalf("session id missing: %v", request.Args)
				}
				if test.wantID {
					assertArgPair(t, request.Args, "--config", `sandbox_mode="read-only"`)
				} else {
					assertArgPair(t, request.Args, "--sandbox", "read-only")
				}
			})}
			if err := runtime.Invoke(context.Background(), invocation, &codexSink{}); err != nil {
				t.Fatalf("invoke: %v", err)
			}
		})
	}
}

func TestCodexRuntimeValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Invocation)
		want   string
	}{
		{name: "missing model", mutate: func(i *Invocation) { i.Model = "" }, want: "model is required"},
		{name: "unknown profile", mutate: func(i *Invocation) { i.Profile = "admin" }, want: "unsupported"},
		{name: "tools", mutate: func(i *Invocation) { i.Capabilities.Allow = []string{"shell"} }, want: "capabilities"},
		{name: "writable", mutate: func(i *Invocation) { i.Workspace.Access = WorkspaceWritable }, want: "no workspace"},
		{name: "text output", mutate: func(i *Invocation) { i.Output.Type = "markdown" }, want: "requires JSON"},
		{name: "missing schema", mutate: func(i *Invocation) { i.Output.Schema = nil }, want: "JSON Schema"},
		{name: "relative workspace", mutate: func(i *Invocation) {
			i.Profile = CapabilityInspect
			i.Workspace = WorkspacePolicy{Root: "relative", Access: WorkspaceReadOnly}
			i.Capabilities = CapabilityPolicy{Profile: CapabilityInspect, Allow: []string{"workspace.inspect"}}
		}, want: "absolute"},
		{name: "fresh resume", mutate: func(i *Invocation) { i.Session.ID = "session" }, want: "fresh"},
		{name: "fork without id", mutate: func(i *Invocation) { i.Session = SessionPolicy{Mode: SessionFork} }, want: "source id"},
		{name: "unknown session", mutate: func(i *Invocation) { i.Session.Mode = "recent" }, want: "session mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invocation := codexInvocation(t.TempDir())
			test.mutate(&invocation)
			runtime := &CodexRuntime{Runner: processRunnerFunc(func(context.Context, ProcessRequest) (ProcessResult, error) {
				t.Fatal("invalid invocation started a process")
				return ProcessResult{}, nil
			})}
			err := runtime.Invoke(context.Background(), invocation, &codexSink{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCodexRuntimeClassifiesProcessFailures(t *testing.T) {
	tests := []struct {
		name   string
		result ProcessResult
		err    error
		want   error
	}{
		{name: "missing executable", err: &exec.Error{Name: "codex", Err: exec.ErrNotFound}, want: ErrCodexExecutableNotFound},
		{name: "unauthenticated", result: ProcessResult{Stderr: []byte("Not logged in. Run codex login")}, err: &ProcessError{Executable: "codex", ExitCode: 1}, want: ErrCodexUnauthenticated},
		{name: "unsupported model", result: ProcessResult{Stderr: []byte("invalid model gpt-never")}, err: &ProcessError{Executable: "codex", ExitCode: 1}, want: ErrCodexUnsupportedModel},
		{name: "rejected config", result: ProcessResult{Stderr: []byte("Error loading config.toml: unknown configuration field")}, err: &ProcessError{Executable: "codex", ExitCode: 1}, want: ErrCodexConfiguration},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &CodexRuntime{Runner: processRunnerFunc(func(context.Context, ProcessRequest) (ProcessResult, error) {
				return test.result, test.err
			})}
			err := runtime.Invoke(context.Background(), codexInvocation(t.TempDir()), &codexSink{})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCodexRuntimeDetectsProtocolDriftAndToolAttempts(t *testing.T) {
	thread := `{"type":"thread.started","thread_id":"thread-1"}`
	turn := `{"type":"turn.started"}`
	completed := `{"type":"turn.completed","usage":{"input_tokens":2,"cached_input_tokens":0,"output_tokens":1}}`
	tests := []struct {
		name  string
		lines []string
		want  error
		text  string
	}{
		{name: "terminal noise", lines: []string{"not-json"}, want: ErrCodexProtocol, text: "invalid JSON"},
		{name: "unknown event", lines: []string{thread, `{"type":"surprise"}`}, want: ErrCodexProtocol, text: "unknown event"},
		{name: "missing thread", lines: []string{turn}, want: ErrCodexProtocol, text: "active thread"},
		{name: "missing completion", lines: []string{thread, turn, `{"type":"item.completed","item":{"id":"i","type":"agent_message","text":"{}"}}`}, want: ErrCodexProtocol, text: "missing terminal"},
		{name: "invalid final JSON", lines: []string{thread, turn, `{"type":"item.completed","item":{"id":"i","type":"agent_message","text":"nope"}}`, completed}, want: ErrCodexProtocol, text: "not valid structured"},
		{name: "missing wire result", lines: []string{thread, turn, `{"type":"item.completed","item":{"id":"i","type":"agent_message","text":"{}"}}`, completed}, want: ErrCodexProtocol, text: "wire envelope"},
		{name: "invalid wrapped JSON", lines: []string{thread, turn, `{"type":"item.completed","item":{"id":"i","type":"agent_message","text":"{\\\"result_json\\\":\\\"nope\\\"}"}}`, completed}, want: ErrCodexProtocol, text: "not valid structured"},
		{name: "tool attempt", lines: []string{thread, turn, `{"type":"item.started","item":{"id":"i","type":"command_execution","command":"pwd"}}`}, want: ErrCodexUndeclaredTool, text: "command_execution"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &CodexRuntime{Runner: processRunnerFunc(func(_ context.Context, request ProcessRequest) (ProcessResult, error) {
				for _, line := range test.lines {
					if err := request.StdoutLine([]byte(line)); err != nil {
						return ProcessResult{}, err
					}
				}
				return ProcessResult{}, nil
			})}
			err := runtime.Invoke(context.Background(), codexInvocation(t.TempDir()), &codexSink{})
			if !errors.Is(err, test.want) || !strings.Contains(err.Error(), test.text) {
				t.Fatalf("error = %v, want %v containing %q", err, test.want, test.text)
			}
		})
	}
}

func TestDefaultExternalFactoryResolvesCodex(t *testing.T) {
	resolved, err := DefaultExternalFactory().Resolve(CodexRuntimeName)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, ok := resolved.(*CodexRuntime); !ok {
		t.Fatalf("resolved runtime = %T", resolved)
	}
}

func argValue(t *testing.T, args []string, key string) string {
	t.Helper()
	for index := 0; index+1 < len(args); index++ {
		if args[index] == key {
			return args[index+1]
		}
	}
	t.Fatalf("argv missing %s: %v", key, args)
	return ""
}
