package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	GrokRuntimeName     = "grok"
	GrokAdapterVersion  = "1"
	GrokProtocolVersion = "grok-streaming-json/1"
)

var (
	ErrGrokExecutableNotFound = errors.New("grok executable not found")
	ErrGrokUnauthenticated    = errors.New("grok cli is not authenticated")
	ErrGrokUnsupportedModel   = errors.New("grok model is unsupported")
	ErrGrokConfiguration      = errors.New("grok configuration was rejected")
	ErrGrokProtocol           = errors.New("grok streaming-json protocol error")
	ErrGrokUndeclaredTool     = errors.New("grok exposed or attempted an undeclared tool")
	ErrGrokInvocation         = errors.New("grok invocation failed")
)

// GrokRuntime executes Grok Build as a complete external agent. Smith keeps
// OAuth-backed user state available to the CLI, but explicitly disables its
// ambient compatibility scanners and verifies the effective tool list from
// the streaming protocol before accepting any result.
type GrokRuntime struct {
	Runner      ProcessRunner
	Admitter    ContainmentAdmitter
	Executable  string
	Environment []string
}

func (g *GrokRuntime) Admit(ctx context.Context, request ContainmentRequest) (ContainmentAdmission, error) {
	if g.Admitter != nil {
		return g.Admitter.Admit(ctx, request)
	}
	return (HostContainmentAdmitter{}).Admit(ctx, request)
}

func NewGrokRuntime() *GrokRuntime {
	return &GrokRuntime{
		Runner:      OSProcessRunner{},
		Executable:  "grok",
		Environment: grokSubscriptionEnvironment(),
	}
}

func (g *GrokRuntime) Invoke(ctx context.Context, invocation Invocation, sink InvocationSink) error {
	if sink == nil {
		return fmt.Errorf("grok runtime sink is required")
	}
	if err := validateGrokInvocation(invocation); err != nil {
		return err
	}
	prompt, err := grokPrompt(invocation.Messages)
	if err != nil {
		return err
	}
	workspace, cleanupWorkspace, err := processWorkspace(invocation.Workspace)
	if err != nil {
		return err
	}
	defer cleanupWorkspace()
	processInvocation := invocation
	processInvocation.Workspace.Root = workspace

	promptPath, cleanupPrompt, err := writeGrokPrompt(prompt)
	if err != nil {
		return err
	}
	defer cleanupPrompt()

	runner := g.Runner
	if runner == nil {
		runner = OSProcessRunner{}
	}
	executable := g.Executable
	if executable == "" {
		executable = "grok"
	}
	environment := g.Environment
	if environment == nil {
		environment = grokSubscriptionEnvironment()
	}

	stream := &grokStream{
		sink:          sink,
		profile:       invocation.Profile,
		expectedTools: grokAllowedTools(invocation.Profile),
	}
	runCtx := ctx
	cancel := func() {}
	if invocation.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, invocation.Timeout)
	}
	defer cancel()
	startedAt := time.Now()
	processResult, runErr := runner.Run(runCtx, ProcessRequest{
		Executable:       executable,
		Args:             grokArgs(processInvocation, promptPath),
		Dir:              workspace,
		Env:              append([]string(nil), environment...),
		Limits:           invocation.Limits,
		Containment:      invocation.Containment,
		TerminationStage: invocation.TerminationStage,
		StdoutLine: func(line []byte) error {
			return stream.consume(runCtx, line)
		},
	})
	duration := time.Since(startedAt)
	if emitErr := emitExternalProcessRecord(runCtx, sink, workspace, processResult, runErr, duration); emitErr != nil {
		return errors.Join(runErr, emitErr)
	}
	if runErr != nil {
		return classifyGrokProcessError(runErr, processResult)
	}
	result, err := stream.externalResult(invocation, duration)
	if err != nil {
		return err
	}
	if err := sink.Complete(runCtx, result); err != nil {
		return fmt.Errorf("complete grok runtime result: %w", err)
	}
	return nil
}

func validateGrokInvocation(invocation Invocation) error {
	if invocation.Runtime != "" && invocation.Runtime != GrokRuntimeName {
		return fmt.Errorf("grok runtime cannot execute runtime %q", invocation.Runtime)
	}
	if strings.TrimSpace(invocation.Model) == "" {
		return fmt.Errorf("grok runtime model is required")
	}
	profile := invocation.Profile
	if profile == "" {
		profile = DefaultProfile
	}
	if profile != CapabilityReason && profile != CapabilityInspect && profile != CapabilityWork {
		return fmt.Errorf("grok runtime profile %q is unsupported", profile)
	}
	if err := validateProfileWorkspace(profile, invocation.Workspace); err != nil {
		return fmt.Errorf("grok runtime: %w", err)
	}
	if len(invocation.MCPServers) > 0 {
		return fmt.Errorf("grok runtime does not support invocation-scoped MCP capabilities")
	}
	if err := validateProfileCapabilities(profile, invocation.Capabilities, invocation.MCPServers); err != nil {
		return fmt.Errorf("grok runtime: %w", err)
	}
	if invocation.Output.Type != "json" {
		return fmt.Errorf("grok runtime requires JSON output")
	}
	if len(invocation.Output.Schema) == 0 || !json.Valid(invocation.Output.Schema) {
		return fmt.Errorf("grok runtime requires a valid JSON Schema")
	}
	switch invocation.Session.Mode {
	case "", SessionFresh:
		if invocation.Session.ID != "" {
			return fmt.Errorf("fresh grok sessions cannot specify a resume id")
		}
	case SessionSticky, SessionResume:
		if invocation.Session.Mode == SessionResume && invocation.Session.ID == "" {
			return fmt.Errorf("resumed grok sessions require a source id")
		}
	case SessionFork:
		if invocation.Session.ID == "" {
			return fmt.Errorf("forked grok sessions require a source id")
		}
	default:
		return fmt.Errorf("unsupported grok session mode %q", invocation.Session.Mode)
	}
	return nil
}

func grokArgs(invocation Invocation, promptPath string) []string {
	args := []string{
		"--cwd", invocation.Workspace.Root,
		"--prompt-file", promptPath,
		"--verbatim",
		"--agent", "grok-build",
		"--model", invocation.Model,
		"--output-format", "streaming-json",
		"--json-schema", string(invocation.Output.Schema),
		"--system-prompt-override", invocation.Persona,
		"--no-memory",
		"--no-subagents",
		"--disable-web-search",
		"--permission-mode", grokPermissionMode(invocation.Profile),
		"--sandbox", "strict",
	}
	if invocation.Session.ID != "" {
		args = append(args, "--resume", invocation.Session.ID)
		if invocation.Session.Mode == SessionFork {
			args = append(args, "--fork-session")
		}
	}
	maxTurns := invocation.Limits.MaxTurns
	if maxTurns == 0 && (invocation.Profile == "" || invocation.Profile == CapabilityReason) {
		maxTurns = 1
	}
	if maxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(maxTurns))
	}
	if allowed := grokAllowedTools(invocation.Profile); len(allowed) > 0 {
		args = append(args, "--tools", strings.Join(allowed, ","))
	}
	args = append(args, "--disallowed-tools", strings.Join(grokDeniedTools(invocation.Profile), ","))
	for _, rule := range grokDenyRules(invocation.Profile) {
		args = append(args, "--deny", rule)
	}
	return args
}

func grokPermissionMode(profile string) string {
	switch profile {
	case CapabilityWork:
		return "acceptEdits"
	case CapabilityInspect:
		return "dontAsk"
	default:
		return "plan"
	}
}

var grokKnownTools = []string{
	"run_terminal_cmd", "run_terminal_command", "read_file", "search_replace", "list_dir", "grep",
	"web_search", "web_fetch", "todo_write", "task", "Agent", "kill_command_or_subagent",
	"get_command_or_subagent_output", "scheduler_create", "scheduler_delete", "scheduler_list",
	"monitor", "search_tool", "use_tool", "workflow", "enter_plan_mode", "exit_plan_mode",
	"ask_user_question", "image_gen", "image_edit", "image_to_video", "reference_to_video",
}

func grokAllowedTools(profile string) []string {
	switch profile {
	case CapabilityInspect:
		return []string{"read_file", "list_dir", "grep"}
	case CapabilityWork:
		return []string{"read_file", "search_replace", "list_dir", "grep"}
	default:
		return nil
	}
}

func grokDeniedTools(profile string) []string {
	allowed := grokAllowedTools(profile)
	denied := make([]string, 0, len(grokKnownTools))
	for _, tool := range grokKnownTools {
		if !slices.Contains(allowed, tool) {
			denied = append(denied, tool)
		}
	}
	return denied
}

func grokDenyRules(profile string) []string {
	rules := []string{"Bash", "WebFetch", "MCPTool"}
	if profile != CapabilityWork {
		rules = append(rules, "Edit", "Write")
	}
	if profile == "" || profile == CapabilityReason {
		rules = append(rules, "Read", "Grep")
	}
	return rules
}

func grokPrompt(messages []Message) ([]byte, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("grok runtime requires at least one message")
	}
	if len(messages) == 1 && messages[0].Role == "user" && messages[0].ToolCall == nil && messages[0].ToolResult == nil {
		return []byte(messages[0].Text), nil
	}
	var prompt strings.Builder
	for _, message := range messages {
		if message.ToolCall != nil || message.ToolResult != nil {
			return nil, fmt.Errorf("grok external runtime does not accept Smith-owned tool turns")
		}
		if message.Role != "user" && message.Role != "assistant" {
			return nil, fmt.Errorf("grok external runtime does not accept message role %q", message.Role)
		}
		fmt.Fprintf(&prompt, "## %s\n\n%s\n\n", message.Role, message.Text)
	}
	return []byte(strings.TrimSpace(prompt.String())), nil
}

func writeGrokPrompt(prompt []byte) (string, func(), error) {
	file, err := os.CreateTemp("", "smith-grok-prompt-*.md")
	if err != nil {
		return "", func() {}, fmt.Errorf("create grok prompt: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("secure grok prompt: %w", err)
	}
	if _, err := file.Write(prompt); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write grok prompt: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close grok prompt: %w", err)
	}
	return path, cleanup, nil
}

func grokSubscriptionEnvironment() []string {
	keys := []string{
		"HOME", "GROK_HOME", "USERPROFILE", "USER", "LOGNAME", "PATH",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
		"APPDATA", "LOCALAPPDATA",
	}
	environment := make([]string, 0, len(keys)+28)
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			environment = append(environment, key+"="+value)
		}
	}
	environment = append(environment,
		"TERM=dumb", "NO_COLOR=1", "GROK_DISABLE_AUTOUPDATER=1",
		"GROK_MEMORY=0", "GROK_SUBAGENTS=0", "GROK_WRITE_FILE=0",
		"GROK_TOOL_SEARCH=0", "GROK_LSP_TOOLS=0", "GROK_WEB_FETCH=0",
		"GROK_TELEMETRY_ENABLED=0", "GROK_FEEDBACK_ENABLED=0",
		"GROK_SANDBOX_AUTO_ALLOW_BASH=0", "GROK_REMEMBER_TOOL_APPROVALS=0",
		"GROK_CURSOR_SKILLS_ENABLED=0", "GROK_CURSOR_RULES_ENABLED=0",
		"GROK_CURSOR_AGENTS_ENABLED=0", "GROK_CURSOR_MCPS_ENABLED=0",
		"GROK_CURSOR_HOOKS_ENABLED=0", "GROK_CURSOR_SESSIONS_ENABLED=0",
		"GROK_CLAUDE_SKILLS_ENABLED=0", "GROK_CLAUDE_RULES_ENABLED=0",
		"GROK_CLAUDE_AGENTS_ENABLED=0", "GROK_CLAUDE_MCPS_ENABLED=0",
		"GROK_CLAUDE_HOOKS_ENABLED=0", "GROK_CLAUDE_SESSIONS_ENABLED=0",
		"GROK_CODEX_SESSIONS_ENABLED=0",
	)
	return environment
}

type grokStream struct {
	sink             InvocationSink
	profile          string
	expectedTools    []string
	commandsObserved bool
	availableRaw     json.RawMessage
	toolCalls        map[string]string
	terminal         *grokEnd
	textChunks       int
	thoughtChunks    int
}

type grokEnvelope struct {
	Type string `json:"type"`
}

type grokAvailableCommands struct {
	Type     string   `json:"type"`
	Tools    []string `json:"tools"`
	Commands []string `json:"commands"`
}

type grokUsage struct {
	InputTokens              *int            `json:"input_tokens"`
	OutputTokens             *int            `json:"output_tokens"`
	CacheReadInputTokens     *int            `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int            `json:"cache_creation_input_tokens"`
	ReasoningTokens          *int            `json:"reasoning_tokens"`
	TotalTokens              *int            `json:"total_tokens"`
	Raw                      json.RawMessage `json:"-"`
}

type grokModelUsage struct {
	InputTokens              *int     `json:"inputTokens"`
	OutputTokens             *int     `json:"outputTokens"`
	CacheReadInputTokens     *int     `json:"cacheReadInputTokens"`
	CacheCreationInputTokens *int     `json:"cacheCreationInputTokens"`
	ModelCalls               *int     `json:"modelCalls"`
	CostUSD                  *float64 `json:"costUSD"`
}

type grokEnd struct {
	Type             string                    `json:"type"`
	StopReason       string                    `json:"stopReason"`
	SessionID        string                    `json:"sessionId"`
	RequestID        string                    `json:"requestId"`
	Usage            grokUsage                 `json:"usage"`
	NumTurns         *int                      `json:"num_turns"`
	TotalCostUSD     *float64                  `json:"total_cost_usd"`
	ModelUsage       map[string]grokModelUsage `json:"modelUsage"`
	StructuredOutput json.RawMessage           `json:"structuredOutput"`
	Raw              json.RawMessage           `json:"-"`
}

type grokToolEvent struct {
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	Title      string `json:"title"`
}

func (s *grokStream) consume(ctx context.Context, line []byte) error {
	if s.terminal != nil {
		return fmt.Errorf("%w: event occurred after terminal end", ErrGrokProtocol)
	}
	var envelope grokEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return fmt.Errorf("%w: invalid JSON line: %v", ErrGrokProtocol, err)
	}
	if envelope.Type == "" {
		return fmt.Errorf("%w: event omitted type", ErrGrokProtocol)
	}
	switch envelope.Type {
	case "available_commands":
		var available grokAvailableCommands
		if err := json.Unmarshal(line, &available); err != nil {
			return fmt.Errorf("%w: decode available_commands: %v", ErrGrokProtocol, err)
		}
		if !sameStringSet(available.Tools, s.expectedTools) {
			return fmt.Errorf("%w: profile %q expected tools %v, got %v", ErrGrokUndeclaredTool, normalizedProfile(s.profile), s.expectedTools, available.Tools)
		}
		if s.commandsObserved {
			return nil
		}
		s.commandsObserved = true
		s.availableRaw = append(json.RawMessage(nil), line...)
		data, _ := json.Marshal(map[string]any{"tools": available.Tools, "command_count": len(available.Commands)})
		return s.sink.Emit(ctx, RuntimeEvent{Type: "grok.session.ready", Data: data})
	case "thought":
		s.thoughtChunks++
	case "text":
		s.textChunks++
	case "usage":
		var event struct {
			Usage json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal(line, &event); err != nil || !json.Valid(event.Usage) {
			return fmt.Errorf("%w: malformed usage event", ErrGrokProtocol)
		}
		return s.sink.Emit(ctx, RuntimeEvent{Type: "grok.turn.usage", Data: append(json.RawMessage(nil), event.Usage...)})
	case "tool_call", "tool_call_update":
		if !s.commandsObserved {
			return fmt.Errorf("%w: tool event occurred before capability declaration", ErrGrokProtocol)
		}
		var event grokToolEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return fmt.Errorf("%w: decode %s: %v", ErrGrokProtocol, envelope.Type, err)
		}
		tool := event.ToolName
		if tool == "" {
			tool = event.Title
		}
		if tool == "" && event.ToolCallID != "" {
			tool = s.toolCalls[event.ToolCallID]
		}
		if !slices.Contains(s.expectedTools, tool) {
			return fmt.Errorf("%w: %s (%s)", ErrGrokUndeclaredTool, tool, envelope.Type)
		}
		if envelope.Type == "tool_call" && event.ToolCallID != "" {
			if s.toolCalls == nil {
				s.toolCalls = make(map[string]string)
			}
			s.toolCalls[event.ToolCallID] = tool
		}
		return s.sink.Emit(ctx, RuntimeEvent{Type: "grok.tool." + envelope.Type, Data: append(json.RawMessage(nil), line...)})
	case "end":
		if !s.commandsObserved {
			return fmt.Errorf("%w: missing available_commands capability declaration", ErrGrokProtocol)
		}
		var end grokEnd
		if err := json.Unmarshal(line, &end); err != nil {
			return fmt.Errorf("%w: decode end event: %v", ErrGrokProtocol, err)
		}
		var raw struct {
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(line, &raw) == nil {
			end.Usage.Raw = append(json.RawMessage(nil), raw.Usage...)
		}
		end.Raw = append(json.RawMessage(nil), line...)
		s.terminal = &end
		data, _ := json.Marshal(map[string]any{
			"session_id": end.SessionID, "request_id": end.RequestID,
			"stop_reason": end.StopReason, "turns": end.NumTurns,
		})
		return s.sink.Emit(ctx, RuntimeEvent{Type: "grok.session.completed", Data: data})
	case "error":
		var failure struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(line, &failure)
		return classifyGrokMessage(failure.Message)
	case "max_turns_reached":
		return s.sink.Emit(ctx, RuntimeEvent{Type: "grok.limit.max_turns", Data: append(json.RawMessage(nil), line...)})
	default:
		if strings.Contains(envelope.Type, "tool") {
			return fmt.Errorf("%w: unknown tool event type %q", ErrGrokProtocol, envelope.Type)
		}
		return s.sink.Emit(ctx, RuntimeEvent{Type: "grok.protocol.unknown", Message: envelope.Type, Data: append(json.RawMessage(nil), line...)})
	}
	return nil
}

func normalizedProfile(profile string) string {
	if profile == "" {
		return DefaultProfile
	}
	return profile
}

func (s *grokStream) externalResult(invocation Invocation, duration time.Duration) (*ExternalResult, error) {
	if !s.commandsObserved {
		return nil, fmt.Errorf("%w: missing available_commands event", ErrGrokProtocol)
	}
	if s.terminal == nil {
		return nil, fmt.Errorf("%w: missing terminal end event", ErrGrokProtocol)
	}
	if s.terminal.SessionID == "" || s.terminal.StopReason == "" {
		return nil, fmt.Errorf("%w: end event omitted session or stop reason", ErrGrokProtocol)
	}
	structured := append(json.RawMessage(nil), s.terminal.StructuredOutput...)
	if len(structured) == 0 || string(structured) == "null" || !json.Valid(structured) {
		return nil, fmt.Errorf("%w: end event omitted valid structured output", ErrGrokProtocol)
	}
	if s.terminal.Usage.InputTokens == nil || s.terminal.Usage.OutputTokens == nil || s.terminal.NumTurns == nil {
		return nil, fmt.Errorf("%w: end event omitted token or turn usage", ErrGrokProtocol)
	}
	canonicalModel, billingBasis := grokModelProvenance(invocation.Model, s.terminal)
	usageRaw, _ := json.Marshal(map[string]any{
		"usage": s.terminal.Usage.Raw, "model_usage": s.terminal.ModelUsage,
		"total_cost_usd": s.terminal.TotalCostUSD,
	})
	provenanceRaw, _ := json.Marshal(map[string]json.RawMessage{
		"available_commands": s.availableRaw,
		"end":                s.terminal.Raw,
	})
	return &ExternalResult{
		JSON: structured,
		Usage: Usage{
			InputTokens:       cloneInt(s.terminal.Usage.InputTokens),
			OutputTokens:      cloneInt(s.terminal.Usage.OutputTokens),
			CachedInputTokens: sumOptionalInts(s.terminal.Usage.CacheReadInputTokens, s.terminal.Usage.CacheCreationInputTokens),
			Turns:             cloneInt(s.terminal.NumTurns),
			Raw:               usageRaw,
		},
		Provenance: Provenance{
			Adapter:         GrokRuntimeName,
			AdapterVersion:  GrokAdapterVersion,
			ProtocolVersion: GrokProtocolVersion,
			RequestedModel:  invocation.Model,
			CanonicalModel:  canonicalModel,
			SessionID:       s.terminal.SessionID,
			TerminalReason:  s.terminal.StopReason,
			BillingBasis:    billingBasis,
			ReportedCostUSD: grokReportedCost(canonicalModel, s.terminal),
			Raw:             provenanceRaw,
		},
		Duration: duration,
	}, nil
}

func grokModelProvenance(requested string, end *grokEnd) (string, string) {
	canonical := requested
	if _, ok := end.ModelUsage[requested]; ok {
		canonical = requested
	} else {
		matches := make([]string, 0, len(end.ModelUsage))
		for model := range end.ModelUsage {
			if strings.HasPrefix(model, requested+"-") || strings.HasPrefix(requested, model+"-") {
				matches = append(matches, model)
			}
		}
		if len(matches) == 1 {
			canonical = matches[0]
		} else if len(end.ModelUsage) == 1 {
			for model := range end.ModelUsage {
				canonical = model
			}
		}
	}
	basis := BillingSubscription
	if end.TotalCostUSD != nil {
		basis = BillingReportedListEstimate
	} else if usage, ok := end.ModelUsage[canonical]; ok && usage.CostUSD != nil {
		basis = BillingReportedListEstimate
	}
	return canonical, basis
}

func grokReportedCost(canonical string, end *grokEnd) *float64 {
	if end.TotalCostUSD != nil {
		return cloneFloat(end.TotalCostUSD)
	}
	if usage, ok := end.ModelUsage[canonical]; ok {
		return cloneFloat(usage.CostUSD)
	}
	return nil
}

func classifyGrokProcessError(err error, result ProcessResult) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrProcessResourceLimit) || errors.Is(err, ErrProcessContainmentUnavailable) ||
		errors.Is(err, ErrGrokProtocol) || errors.Is(err, ErrGrokUndeclaredTool) ||
		errors.Is(err, ErrGrokUnauthenticated) || errors.Is(err, ErrGrokUnsupportedModel) ||
		errors.Is(err, ErrGrokConfiguration) || errors.Is(err, ErrGrokInvocation) {
		return err
	}
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%w: %v", ErrGrokExecutableNotFound, err)
	}
	var executableErr *exec.Error
	if errors.As(err, &executableErr) {
		return fmt.Errorf("%w: %v", ErrGrokExecutableNotFound, err)
	}
	diagnostic := strings.TrimSpace(string(result.Stderr) + "\n" + string(result.Stdout))
	classified := classifyGrokMessage(diagnostic)
	if !errors.Is(classified, ErrGrokInvocation) {
		return classified
	}
	return err
}

func classifyGrokMessage(message string) error {
	trimmed := strings.TrimSpace(message)
	lower := strings.ToLower(trimmed)
	switch {
	case strings.Contains(lower, "not authenticated"), strings.Contains(lower, "not logged in"),
		strings.Contains(lower, "login required"), strings.Contains(lower, "authentication required"),
		strings.Contains(lower, "unauthorized"):
		return fmt.Errorf("%w: %s", ErrGrokUnauthenticated, trimmed)
	case strings.Contains(lower, "unsupported model"), strings.Contains(lower, "invalid model"),
		strings.Contains(lower, "model not found"), strings.Contains(lower, "no fallback model"):
		return fmt.Errorf("%w: %s", ErrGrokUnsupportedModel, trimmed)
	case strings.Contains(lower, "invalid value"), strings.Contains(lower, "invalid configuration"),
		strings.Contains(lower, "couldn't create session"), strings.Contains(lower, "sandbox"):
		return fmt.Errorf("%w: %s", ErrGrokConfiguration, trimmed)
	default:
		return fmt.Errorf("%w: %s", ErrGrokInvocation, trimmed)
	}
}
