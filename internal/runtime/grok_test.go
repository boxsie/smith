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
)

type grokSink struct {
	events []RuntimeEvent
	result *ExternalResult
}

func (s *grokSink) Emit(_ context.Context, event RuntimeEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *grokSink) Complete(_ context.Context, result *ExternalResult) error {
	if s.result != nil {
		return errors.New("duplicate completion")
	}
	s.result = result
	return nil
}

func grokInvocation() Invocation {
	return Invocation{
		Messages: []Message{{Role: "user", Text: "Return the answer."}},
		Persona:  "You are exact.",
		Output: OutputContract{
			Type:   "json",
			Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`),
		},
		Runtime:      GrokRuntimeName,
		Model:        "grok-4.5",
		Profile:      DefaultProfile,
		Workspace:    WorkspacePolicy{Access: WorkspaceNone, Isolation: "ephemeral", Granted: true},
		Capabilities: CapabilityPolicy{Profile: CapabilityReason},
		Session:      SessionPolicy{Mode: SessionFresh},
	}
}

func grokFixtureRunner(t *testing.T, name string, inspect func(ProcessRequest)) ProcessRunner {
	t.Helper()
	fixture, err := os.ReadFile("testdata/grok/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return processRunnerFunc(func(_ context.Context, request ProcessRequest) (ProcessResult, error) {
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

func TestGrokRuntimeParsesObservedStreamAndUsesSterileArgv(t *testing.T) {
	t.Setenv("XAI_API_KEY", "must-not-leak")
	t.Setenv("GROK_DEPLOYMENT_KEY", "must-not-leak")
	invocation := grokInvocation()
	var request ProcessRequest
	runtime := &GrokRuntime{Executable: "grok-test", Environment: grokSubscriptionEnvironment()}
	runtime.Runner = grokFixtureRunner(t, "success.jsonl", func(got ProcessRequest) {
		request = got
		promptPath := argValue(t, got.Args, "--prompt-file")
		prompt, err := os.ReadFile(promptPath)
		if err != nil {
			t.Fatalf("read prompt file: %v", err)
		}
		if string(prompt) != "Return the answer." {
			t.Fatalf("prompt = %q", prompt)
		}
	})
	sink := &grokSink{}

	if err := runtime.Invoke(context.Background(), invocation, sink); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if request.Executable != "grok-test" || !strings.Contains(request.Dir, "smith-sterile-") {
		t.Fatalf("process request = %#v", request)
	}
	if _, err := os.Stat(request.Dir); !os.IsNotExist(err) {
		t.Fatalf("sterile workspace was not removed: %v", err)
	}
	promptPath := argValue(t, request.Args, "--prompt-file")
	if _, err := os.Stat(promptPath); !os.IsNotExist(err) {
		t.Fatalf("prompt file was not removed: %v", err)
	}
	if slices.Contains(request.Args, "Return the answer.") || len(request.Stdin) != 0 {
		t.Fatal("user prompt escaped its private prompt file")
	}
	for _, required := range []string{
		"--cwd", "--prompt-file", "--verbatim", "--agent", "--model", "--output-format",
		"--json-schema", "--system-prompt-override", "--no-memory", "--no-subagents",
		"--disable-web-search", "--permission-mode", "--sandbox", "--max-turns",
		"--disallowed-tools",
	} {
		if !slices.Contains(request.Args, required) {
			t.Errorf("argv missing %q: %v", required, request.Args)
		}
	}
	assertArgPair(t, request.Args, "--cwd", request.Dir)
	assertArgPair(t, request.Args, "--agent", "grok-build")
	assertArgPair(t, request.Args, "--model", invocation.Model)
	assertArgPair(t, request.Args, "--output-format", "streaming-json")
	assertArgPair(t, request.Args, "--permission-mode", "plan")
	assertArgPair(t, request.Args, "--sandbox", "strict")
	assertArgPair(t, request.Args, "--max-turns", "1")
	if slices.Contains(request.Args, "--worktree") || slices.Contains(request.Args, "--always-approve") || slices.Contains(request.Args, "--tools") {
		t.Fatalf("unsafe or ineffective argv = %v", request.Args)
	}
	denied := strings.Split(argValue(t, request.Args, "--disallowed-tools"), ",")
	for _, tool := range grokKnownTools {
		if !slices.Contains(denied, tool) {
			t.Errorf("deny list omitted %q", tool)
		}
	}
	for _, value := range request.Env {
		if strings.HasPrefix(value, "XAI_API_KEY=") || strings.HasPrefix(value, "GROK_DEPLOYMENT_KEY=") {
			t.Fatalf("API credential leaked: %s", strings.SplitN(value, "=", 2)[0])
		}
	}
	for _, setting := range []string{
		"GROK_MEMORY=0", "GROK_SUBAGENTS=0", "GROK_TOOL_SEARCH=0",
		"GROK_CLAUDE_SKILLS_ENABLED=0", "GROK_CLAUDE_MCPS_ENABLED=0",
		"GROK_CURSOR_SKILLS_ENABLED=0", "GROK_CODEX_SESSIONS_ENABLED=0",
	} {
		if !slices.Contains(request.Env, setting) {
			t.Errorf("environment missing %q: %v", setting, request.Env)
		}
	}
	if sink.result == nil || string(sink.result.JSON) != `{"answer":42}` {
		t.Fatalf("result = %#v", sink.result)
	}
	if sink.result.Provenance.Adapter != GrokRuntimeName || sink.result.Provenance.RequestedModel != "grok-4.5" ||
		sink.result.Provenance.CanonicalModel != "grok-4.5-build" || sink.result.Provenance.SessionID != "smith-grok-session" ||
		sink.result.Provenance.BillingBasis != BillingReportedListEstimate || sink.result.Provenance.ReportedCostUSD == nil || *sink.result.Provenance.ReportedCostUSD != 0.01 ||
		sink.result.Provenance.AdapterVersion != "1" || sink.result.Provenance.ProtocolVersion != "grok-streaming-json/1" {
		t.Fatalf("provenance = %#v", sink.result.Provenance)
	}
	if valueOrZero(sink.result.Usage.InputTokens) != 100 || valueOrZero(sink.result.Usage.CachedInputTokens) != 25 ||
		valueOrZero(sink.result.Usage.OutputTokens) != 10 || valueOrZero(sink.result.Usage.Turns) != 1 {
		t.Fatalf("usage = %#v", sink.result.Usage)
	}
	gotEvents := make([]string, len(sink.events))
	for index, event := range sink.events {
		gotEvents[index] = event.Type
	}
	wantEvents := []string{"grok.session.ready", "grok.turn.usage", "grok.session.completed", "runtime.process.completed"}
	if !slices.Equal(gotEvents, wantEvents) {
		t.Fatalf("events = %v, want %v", gotEvents, wantEvents)
	}
}

func TestGrokRuntimeSessionAndProfileModes(t *testing.T) {
	sessionID := "01a0628c-6f24-7c21-b0b7-d6b5b38cc378"
	tests := []struct {
		name           string
		profile        string
		session        SessionPolicy
		workspace      WorkspacePolicy
		capabilities   CapabilityPolicy
		wantPermission string
		wantTools      []string
		wantResume     bool
		wantFork       bool
	}{
		{name: "fresh reason", profile: CapabilityReason, session: SessionPolicy{Mode: SessionFresh}, workspace: WorkspacePolicy{Access: WorkspaceNone}, capabilities: CapabilityPolicy{Profile: CapabilityReason}, wantPermission: "plan"},
		{name: "sticky new", profile: CapabilityReason, session: SessionPolicy{Mode: SessionSticky}, workspace: WorkspacePolicy{Access: WorkspaceNone}, capabilities: CapabilityPolicy{Profile: CapabilityReason}, wantPermission: "plan"},
		{name: "resume", profile: CapabilityReason, session: SessionPolicy{Mode: SessionResume, ID: sessionID}, workspace: WorkspacePolicy{Access: WorkspaceNone}, capabilities: CapabilityPolicy{Profile: CapabilityReason}, wantPermission: "plan", wantResume: true},
		{name: "fork", profile: CapabilityReason, session: SessionPolicy{Mode: SessionFork, ID: sessionID}, workspace: WorkspacePolicy{Access: WorkspaceNone}, capabilities: CapabilityPolicy{Profile: CapabilityReason}, wantPermission: "plan", wantResume: true, wantFork: true},
		{name: "inspect", profile: CapabilityInspect, session: SessionPolicy{Mode: SessionFresh}, workspace: WorkspacePolicy{Access: WorkspaceReadOnly}, capabilities: CapabilityPolicy{Profile: CapabilityInspect, Allow: []string{"workspace.inspect"}}, wantPermission: "dontAsk", wantTools: []string{"read_file", "list_dir", "grep"}},
		{name: "work", profile: CapabilityWork, session: SessionPolicy{Mode: SessionFresh}, workspace: WorkspacePolicy{Access: WorkspaceWritable, Granted: true}, capabilities: CapabilityPolicy{Profile: CapabilityWork, Allow: []string{"workspace.inspect", "workspace.edit"}}, wantPermission: "acceptEdits", wantTools: []string{"read_file", "search_replace", "list_dir", "grep"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invocation := grokInvocation()
			invocation.Profile = test.profile
			invocation.Session = test.session
			invocation.Capabilities = test.capabilities
			if test.workspace.Access != WorkspaceNone {
				test.workspace.Root = t.TempDir()
			}
			invocation.Workspace = test.workspace
			runtime := &GrokRuntime{Runner: grokFixtureRunner(t, "success.jsonl", func(request ProcessRequest) {
				assertArgPair(t, request.Args, "--permission-mode", test.wantPermission)
				if test.wantResume {
					assertArgPair(t, request.Args, "--resume", sessionID)
				} else if slices.Contains(request.Args, "--resume") {
					t.Fatalf("unexpected resume argv: %v", request.Args)
				}
				if slices.Contains(request.Args, "--fork-session") != test.wantFork {
					t.Fatalf("fork argv = %v", request.Args)
				}
				if len(test.wantTools) > 0 {
					assertArgPair(t, request.Args, "--tools", strings.Join(test.wantTools, ","))
				}
			})}
			fixture := runtime.Runner
			runtime.Runner = processRunnerFunc(func(ctx context.Context, request ProcessRequest) (ProcessResult, error) {
				original := request.StdoutLine
				request.StdoutLine = func(line []byte) error {
					if strings.Contains(string(line), `"type":"available_commands"`) {
						line, _ = json.Marshal(grokAvailableCommands{Type: "available_commands", Tools: test.wantTools})
					}
					return original(line)
				}
				return fixture.Run(ctx, request)
			})
			if err := runtime.Invoke(context.Background(), invocation, &grokSink{}); err != nil {
				t.Fatalf("invoke: %v", err)
			}
		})
	}
}

func TestGrokRuntimeValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Invocation)
		want   string
	}{
		{name: "missing model", mutate: func(i *Invocation) { i.Model = "" }, want: "model is required"},
		{name: "unknown profile", mutate: func(i *Invocation) { i.Profile = "admin" }, want: "unsupported"},
		{name: "tools", mutate: func(i *Invocation) { i.Capabilities.Allow = []string{"shell"} }, want: "capabilities"},
		{name: "writable reason", mutate: func(i *Invocation) { i.Workspace.Access = WorkspaceWritable }, want: "no workspace"},
		{name: "text output", mutate: func(i *Invocation) { i.Output.Type = "markdown" }, want: "requires JSON"},
		{name: "missing schema", mutate: func(i *Invocation) { i.Output.Schema = nil }, want: "JSON Schema"},
		{name: "fresh resume", mutate: func(i *Invocation) { i.Session.ID = "session" }, want: "fresh"},
		{name: "fork without id", mutate: func(i *Invocation) { i.Session = SessionPolicy{Mode: SessionFork} }, want: "source id"},
		{name: "unknown session", mutate: func(i *Invocation) { i.Session.Mode = "recent" }, want: "session mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invocation := grokInvocation()
			test.mutate(&invocation)
			runtime := &GrokRuntime{Runner: processRunnerFunc(func(context.Context, ProcessRequest) (ProcessResult, error) {
				t.Fatal("invalid invocation started a process")
				return ProcessResult{}, nil
			})}
			err := runtime.Invoke(context.Background(), invocation, &grokSink{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestGrokRuntimeClassifiesProcessFailures(t *testing.T) {
	tests := []struct {
		name   string
		result ProcessResult
		err    error
		want   error
	}{
		{name: "missing executable", err: &exec.Error{Name: "grok", Err: exec.ErrNotFound}, want: ErrGrokExecutableNotFound},
		{name: "unauthenticated", result: ProcessResult{Stderr: []byte("You are not authenticated")}, err: &ProcessError{Executable: "grok", ExitCode: 1}, want: ErrGrokUnauthenticated},
		{name: "unsupported model", result: ProcessResult{Stderr: []byte("No fallback model for invalid model")}, err: &ProcessError{Executable: "grok", ExitCode: 1}, want: ErrGrokUnsupportedModel},
		{name: "configuration", result: ProcessResult{Stderr: []byte("invalid configuration: sandbox rejected")}, err: &ProcessError{Executable: "grok", ExitCode: 1}, want: ErrGrokConfiguration},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &GrokRuntime{Runner: processRunnerFunc(func(context.Context, ProcessRequest) (ProcessResult, error) {
				return test.result, test.err
			})}
			err := runtime.Invoke(context.Background(), grokInvocation(), &grokSink{})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestGrokRuntimeRejectsUnsafeOrMalformedProtocol(t *testing.T) {
	validEnd := `{"type":"end","stopReason":"end_turn","sessionId":"session","usage":{"input_tokens":2,"output_tokens":1},"num_turns":1,"structuredOutput":{"answer":42}}`
	tests := []struct {
		name  string
		lines []string
		want  error
		text  string
	}{
		{name: "terminal noise", lines: []string{"not-json"}, want: ErrGrokProtocol, text: "invalid JSON"},
		{name: "exposed tool", lines: []string{`{"type":"available_commands","tools":["read_file"]}`}, want: ErrGrokUndeclaredTool, text: "read_file"},
		{name: "tool before declaration", lines: []string{`{"type":"tool_call","toolName":"read_file"}`}, want: ErrGrokProtocol, text: "before capability"},
		{name: "unknown tool event", lines: []string{`{"type":"available_commands","tools":[]}`, `{"type":"new_tool_event"}`}, want: ErrGrokProtocol, text: "unknown tool"},
		{name: "missing commands", lines: []string{validEnd}, want: ErrGrokProtocol, text: "available_commands"},
		{name: "missing end", lines: []string{`{"type":"available_commands","tools":[]}`}, want: ErrGrokProtocol, text: "terminal end"},
		{name: "invalid output", lines: []string{`{"type":"available_commands","tools":[]}`, `{"type":"end","stopReason":"end_turn","sessionId":"session","usage":{"input_tokens":2,"output_tokens":1},"num_turns":1,"structuredOutput":null}`}, want: ErrGrokProtocol, text: "structured output"},
		{name: "missing usage", lines: []string{`{"type":"available_commands","tools":[]}`, `{"type":"end","stopReason":"end_turn","sessionId":"session","usage":{},"num_turns":1,"structuredOutput":{"answer":42}}`}, want: ErrGrokProtocol, text: "usage"},
		{name: "after end", lines: []string{`{"type":"available_commands","tools":[]}`, validEnd, `{"type":"text","data":"late"}`}, want: ErrGrokProtocol, text: "after terminal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &GrokRuntime{Runner: processRunnerFunc(func(_ context.Context, request ProcessRequest) (ProcessResult, error) {
				for _, line := range test.lines {
					if err := request.StdoutLine([]byte(line)); err != nil {
						return ProcessResult{}, err
					}
				}
				return ProcessResult{}, nil
			})}
			err := runtime.Invoke(context.Background(), grokInvocation(), &grokSink{})
			if !errors.Is(err, test.want) || !strings.Contains(err.Error(), test.text) {
				t.Fatalf("error = %v, want %v containing %q", err, test.want, test.text)
			}
		})
	}
}

func TestGrokRuntimeSurfacesUnknownNonToolEvents(t *testing.T) {
	fixture, err := os.ReadFile("testdata/grok/success.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &GrokRuntime{Runner: processRunnerFunc(func(_ context.Context, request ProcessRequest) (ProcessResult, error) {
		lines := strings.Split(strings.TrimSpace(string(fixture)), "\n")
		lines = slices.Insert(lines, 1, `{"type":"auto_compact_started","reason":"fixture"}`)
		for _, line := range lines {
			if err := request.StdoutLine([]byte(line)); err != nil {
				return ProcessResult{}, err
			}
		}
		return ProcessResult{}, nil
	})}
	sink := &grokSink{}
	if err := runtime.Invoke(context.Background(), grokInvocation(), sink); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !slices.ContainsFunc(sink.events, func(event RuntimeEvent) bool {
		return event.Type == "grok.protocol.unknown" && event.Message == "auto_compact_started"
	}) {
		t.Fatalf("unknown protocol event not surfaced: %#v", sink.events)
	}
}

func TestDefaultExternalFactoryResolvesGrok(t *testing.T) {
	resolved, err := DefaultExternalFactory().Resolve(GrokRuntimeName)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, ok := resolved.(*GrokRuntime); !ok {
		t.Fatalf("resolved runtime = %T", resolved)
	}
}
