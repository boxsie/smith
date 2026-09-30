package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

type processRunnerFunc func(context.Context, ProcessRequest) (ProcessResult, error)

func (f processRunnerFunc) Run(ctx context.Context, request ProcessRequest) (ProcessResult, error) {
	return f(ctx, request)
}

type claudeSink struct {
	events []RuntimeEvent
	result *ExternalResult
}

func (s *claudeSink) Emit(_ context.Context, event RuntimeEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *claudeSink) Complete(_ context.Context, result *ExternalResult) error {
	if s.result != nil {
		return errors.New("duplicate completion")
	}
	s.result = result
	return nil
}

func claudeInvocation(_ string) Invocation {
	return Invocation{
		Messages: []Message{{Role: "user", Text: "Return the answer."}},
		Persona:  "You are exact.",
		Output: OutputContract{
			Type:   "json",
			Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`),
		},
		Runtime:      ClaudeRuntimeName,
		Model:        "claude-fable-5-1",
		Profile:      DefaultProfile,
		Workspace:    WorkspacePolicy{Access: WorkspaceNone, Isolation: "ephemeral", Granted: true},
		Capabilities: CapabilityPolicy{Profile: CapabilityReason},
		Session:      SessionPolicy{Mode: SessionFresh},
	}
}

func fixtureProcessRunner(t *testing.T, name string, inspect func(ProcessRequest)) ProcessRunner {
	t.Helper()
	fixture, err := os.ReadFile("testdata/claude/" + name)
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

func TestClaudeRuntimeParsesObservedStreamAndUsesSterileArgv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "must-not-leak")
	invocation := claudeInvocation(t.TempDir())
	var request ProcessRequest
	runtime := &ClaudeRuntime{
		Executable:  "claude-test",
		Environment: claudeSubscriptionEnvironment(),
	}
	runtime.Runner = fixtureProcessRunner(t, "success.jsonl", func(got ProcessRequest) { request = got })
	sink := &claudeSink{}

	if err := runtime.Invoke(context.Background(), invocation, sink); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if request.Executable != "claude-test" || !strings.Contains(request.Dir, "smith-sterile-") {
		t.Fatalf("process request = %#v", request)
	}
	if _, err := os.Stat(request.Dir); !os.IsNotExist(err) {
		t.Fatalf("sterile workspace was not removed: %v", err)
	}
	if got := string(request.Stdin); got != "Return the answer." {
		t.Fatalf("stdin = %q", got)
	}
	if slices.Contains(request.Args, "Return the answer.") {
		t.Fatal("user prompt leaked into argv")
	}
	for _, required := range []string{
		"--restricted", "--strict-mcp-config", "--disable-slash-commands",
		"--no-chrome", "--tools", "--permission-mode", "--json-schema", "--system-prompt",
		"--no-session-persistence",
	} {
		if !slices.Contains(request.Args, required) {
			t.Errorf("argv missing %q: %v", required, request.Args)
		}
	}
	if slices.Contains(request.Args, "--safe-mode") {
		t.Fatal("safe mode suppresses explicitly configured invocation MCP")
	}
	assertArgPair(t, request.Args, "--input-format", "text")
	assertArgPair(t, request.Args, "--output-format", "stream-json")
	assertArgPair(t, request.Args, "--mcp-config", `{"mcpServers":{}}`)
	assertArgPair(t, request.Args, "--tools", "")
	assertArgPair(t, request.Args, "--permission-mode", "plan")
	assertArgPair(t, request.Args, "--system-prompt", invocation.Persona)
	if slices.ContainsFunc(request.Env, func(value string) bool { return strings.HasPrefix(value, "ANTHROPIC_API_KEY=") }) {
		t.Fatal("Anthropic API key leaked into subscription runtime")
	}
	if sink.result == nil {
		t.Fatal("runtime did not complete")
	}
	if got := string(sink.result.JSON); got != `{"answer":42}` {
		t.Fatalf("structured output = %s", got)
	}
	if sink.result.Provenance.Adapter != ClaudeRuntimeName || sink.result.Provenance.AdapterVersion != "1" ||
		sink.result.Provenance.ProtocolVersion != "claude-stream-json/1" || sink.result.Provenance.CLIVersion != "2.1.258" {
		t.Fatalf("adapter provenance = %#v", sink.result.Provenance)
	}
	if sink.result.Provenance.RequestedModel != "claude-fable-5-1" || sink.result.Provenance.CanonicalModel != "claude-fable-5-1" {
		t.Fatalf("model provenance = %#v", sink.result.Provenance)
	}
	if sink.result.Provenance.SessionID != "session-1" || sink.result.Provenance.TerminalReason != "completed" {
		t.Fatalf("terminal provenance = %#v", sink.result.Provenance)
	}
	if sink.result.Provenance.BillingBasis != BillingReportedListEstimate || sink.result.Provenance.ReportedCostUSD == nil || *sink.result.Provenance.ReportedCostUSD != 0.055277 {
		t.Fatalf("billing basis = %q", sink.result.Provenance.BillingBasis)
	}
	if sink.result.Usage.InputTokens == nil || *sink.result.Usage.InputTokens != 2 ||
		sink.result.Usage.OutputTokens == nil || *sink.result.Usage.OutputTokens != 176 ||
		sink.result.Usage.CachedInputTokens == nil || *sink.result.Usage.CachedInputTokens != 2286 ||
		sink.result.Usage.Turns == nil || *sink.result.Usage.Turns != 2 {
		t.Fatalf("usage = %#v", sink.result.Usage)
	}
	if sink.result.Duration != 3965*time.Millisecond {
		t.Fatalf("duration = %v", sink.result.Duration)
	}
	eventTypes := make([]string, len(sink.events))
	for index, event := range sink.events {
		eventTypes[index] = event.Type
	}
	wantEvents := []string{"claude.session.started", "claude.api.retry", "claude.turn", "runtime.process.completed"}
	if !slices.Equal(eventTypes, wantEvents) {
		t.Fatalf("events = %v, want %v", eventTypes, wantEvents)
	}
}

func TestClaudeRuntimeInjectsOnlyInvocationMCPTools(t *testing.T) {
	invocation := claudeInvocation(t.TempDir())
	invocation.MCPServers = []MCPServer{{Name: "tickets_please", URL: "http://127.0.0.1:3210/mcp/token", Tools: []string{"get_ticket"}, Capability: "tickets_please.read"}}
	invocation.Capabilities.Allow = []string{"tickets_please.read"}
	args := claudeArgs(invocation)
	assertArgPair(t, args, "--mcp-config", `{"mcpServers":{"tickets_please":{"type":"http","url":"http://127.0.0.1:3210/mcp/token"}}}`)
	assertArgPair(t, args, "--tools", "mcp__tickets_please__get_ticket")
	assertArgPair(t, args, "--allowedTools", "mcp__tickets_please__get_ticket")
	init := claudeInit{
		SessionID: "session", Model: invocation.Model, Version: "test", APIKeySource: "none", PermissionMode: "dontAsk",
		Tools: []string{"StructuredOutput", "mcp__tickets_please__get_ticket"}, MCPServers: []any{map[string]any{"name": "tickets_please"}},
	}
	if err := validateClaudeInit(init, invocation); err != nil {
		t.Fatalf("configured MCP rejected: %v", err)
	}
	init.MCPServers = append(init.MCPServers, map[string]any{"name": "ambient"})
	if err := validateClaudeInit(init, invocation); err == nil {
		t.Fatal("ambient MCP server was accepted")
	}
}

func TestClaudeRuntimeNormalizesPermissionDenials(t *testing.T) {
	runtime := &ClaudeRuntime{Runner: fixtureProcessRunner(t, "permission-denied.jsonl", nil), Executable: "claude"}
	sink := &claudeSink{}
	if err := runtime.Invoke(context.Background(), claudeInvocation(t.TempDir()), sink); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	want := []string{"Read: not allowed", "WebFetch"}
	if !slices.Equal(sink.result.Provenance.PermissionDenials, want) {
		t.Fatalf("permission denials = %v, want %v", sink.result.Provenance.PermissionDenials, want)
	}
}

func TestClaudeRuntimeStickySessionUsesExplicitResume(t *testing.T) {
	invocation := claudeInvocation(t.TempDir())
	invocation.Session = SessionPolicy{Mode: SessionSticky, ID: "a9c42d85-2075-4cdf-8d87-18418719ba06"}
	runtime := &ClaudeRuntime{Executable: "claude", Runner: fixtureProcessRunner(t, "success.jsonl", func(request ProcessRequest) {
		if slices.Contains(request.Args, "--no-session-persistence") {
			t.Fatal("sticky session disabled persistence")
		}
		assertArgPair(t, request.Args, "--resume", invocation.Session.ID)
		assertArgPair(t, request.Args, "--system-prompt-snapshot", "on")
	})}
	if err := runtime.Invoke(context.Background(), invocation, &claudeSink{}); err != nil {
		t.Fatalf("invoke: %v", err)
	}
}

func TestClaudeRuntimeValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Invocation)
		want   string
	}{
		{name: "missing model", mutate: func(i *Invocation) { i.Model = "" }, want: "model is required"},
		{name: "unknown profile", mutate: func(i *Invocation) { i.Profile = "admin" }, want: "unsupported"},
		{name: "tools", mutate: func(i *Invocation) { i.Capabilities.Allow = []string{"filesystem.read"} }, want: "capabilities"},
		{name: "text output", mutate: func(i *Invocation) { i.Output.Type = "markdown" }, want: "requires JSON"},
		{name: "missing schema", mutate: func(i *Invocation) { i.Output.Schema = nil }, want: "JSON Schema"},
		{name: "relative workspace", mutate: func(i *Invocation) {
			i.Profile = CapabilityInspect
			i.Workspace = WorkspacePolicy{Root: "relative", Access: WorkspaceReadOnly}
			i.Capabilities = CapabilityPolicy{Profile: CapabilityInspect, Allow: []string{"workspace.inspect"}}
		}, want: "absolute"},
		{name: "fresh resume", mutate: func(i *Invocation) { i.Session.ID = "session" }, want: "fresh"},
		{name: "unknown session", mutate: func(i *Invocation) { i.Session.Mode = "recent" }, want: "session mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invocation := claudeInvocation(t.TempDir())
			test.mutate(&invocation)
			runtime := &ClaudeRuntime{Runner: processRunnerFunc(func(context.Context, ProcessRequest) (ProcessResult, error) {
				t.Fatal("invalid invocation started a process")
				return ProcessResult{}, nil
			})}
			err := runtime.Invoke(context.Background(), invocation, &claudeSink{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestClaudeRuntimeClassifiesProcessFailures(t *testing.T) {
	tests := []struct {
		name   string
		result ProcessResult
		err    error
		want   error
	}{
		{name: "missing executable", err: &exec.Error{Name: "claude", Err: exec.ErrNotFound}, want: ErrClaudeExecutableNotFound},
		{name: "unauthenticated", result: ProcessResult{Stderr: []byte("Not logged in. Run claude auth login")}, err: &ProcessError{Executable: "claude", ExitCode: 1}, want: ErrClaudeUnauthenticated},
		{name: "unsupported model", result: ProcessResult{Stderr: []byte("invalid model claude-never")}, err: &ProcessError{Executable: "claude", ExitCode: 1}, want: ErrClaudeUnsupportedModel},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &ClaudeRuntime{Runner: processRunnerFunc(func(context.Context, ProcessRequest) (ProcessResult, error) {
				return test.result, test.err
			})}
			err := runtime.Invoke(context.Background(), claudeInvocation(t.TempDir()), &claudeSink{})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestClaudeRuntimeRejectsAPIKeyAuthentication(t *testing.T) {
	line := `{"type":"system","subtype":"init","session_id":"session","model":"claude-fable-5-1","claude_code_version":"2.1.258","apiKeySource":"ANTHROPIC_API_KEY","permissionMode":"plan","tools":["StructuredOutput"],"mcp_servers":[],"slash_commands":[],"skills":[],"plugins":[]}`
	runtime := &ClaudeRuntime{Runner: processRunnerFunc(func(_ context.Context, request ProcessRequest) (ProcessResult, error) {
		return ProcessResult{}, request.StdoutLine([]byte(line))
	})}
	err := runtime.Invoke(context.Background(), claudeInvocation(t.TempDir()), &claudeSink{})
	if !errors.Is(err, ErrClaudeSubscriptionAuth) {
		t.Fatalf("error = %v, want subscription authentication error", err)
	}
}

func TestClaudeRuntimeDetectsProtocolDrift(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{name: "terminal noise", lines: []string{"not-json"}, want: "invalid JSON line"},
		{name: "missing init", lines: []string{`{"type":"result","subtype":"success","is_error":false,"structured_output":{"answer":1}}`}, want: "missing init"},
		{name: "missing result", lines: []string{`{"type":"system","subtype":"init","session_id":"session","model":"claude-fable-5-1","claude_code_version":"2.1.258","apiKeySource":"none","permissionMode":"plan","tools":["StructuredOutput"],"mcp_servers":[],"slash_commands":[],"skills":[],"plugins":[]}`}, want: "missing terminal result"},
		{name: "ambient tools", lines: []string{`{"type":"system","subtype":"init","session_id":"session","model":"claude-fable-5-1","claude_code_version":"2.1.258","apiKeySource":"none","permissionMode":"plan","tools":["StructuredOutput","Read"],"mcp_servers":[],"slash_commands":[],"skills":[],"plugins":[]}`}, want: "unexpected tools"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &ClaudeRuntime{Runner: processRunnerFunc(func(_ context.Context, request ProcessRequest) (ProcessResult, error) {
				for _, line := range test.lines {
					if err := request.StdoutLine([]byte(line)); err != nil {
						return ProcessResult{}, err
					}
				}
				return ProcessResult{}, nil
			})}
			err := runtime.Invoke(context.Background(), claudeInvocation(t.TempDir()), &claudeSink{})
			if !errors.Is(err, ErrClaudeProtocol) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want protocol error containing %q", err, test.want)
			}
		})
	}
}

func TestDefaultExternalFactoryResolvesClaude(t *testing.T) {
	resolved, err := DefaultExternalFactory().Resolve(ClaudeRuntimeName)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, ok := resolved.(*ClaudeRuntime); !ok {
		t.Fatalf("resolved runtime = %T", resolved)
	}
}

func assertArgPair(t *testing.T, args []string, key, value string) {
	t.Helper()
	for index := 0; index+1 < len(args); index++ {
		if args[index] == key && args[index+1] == value {
			return
		}
	}
	t.Fatalf("argv missing %s %q: %v", key, value, args)
}
