package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	ClaudeRuntimeName     = "claude"
	ClaudeAdapterVersion  = "1"
	ClaudeProtocolVersion = "claude-stream-json/1"
)

var (
	ErrClaudeExecutableNotFound = errors.New("claude executable not found")
	ErrClaudeUnauthenticated    = errors.New("claude cli is not authenticated")
	ErrClaudeSubscriptionAuth   = errors.New("claude subscription authentication required")
	ErrClaudeUnsupportedModel   = errors.New("claude model is unsupported")
	ErrClaudeProtocol           = errors.New("claude stream-json protocol error")
	ErrClaudeInvocation         = errors.New("claude invocation failed")
)

// ClaudeRuntime executes Claude Code as a complete external agent. Safe reason
// mode deliberately leaves Claude's own tools, settings, hooks, plugins, MCP
// servers, skills, and ambient project instructions outside the invocation.
type ClaudeRuntime struct {
	Runner      ProcessRunner
	Admitter    ContainmentAdmitter
	Executable  string
	Environment []string
}

func (c *ClaudeRuntime) Admit(ctx context.Context, request ContainmentRequest) (ContainmentAdmission, error) {
	if c.Admitter != nil {
		return c.Admitter.Admit(ctx, request)
	}
	return (HostContainmentAdmitter{}).Admit(ctx, request)
}

func NewClaudeRuntime() *ClaudeRuntime {
	return &ClaudeRuntime{
		Runner:      OSProcessRunner{},
		Executable:  "claude",
		Environment: claudeSubscriptionEnvironment(),
	}
}

func (c *ClaudeRuntime) Invoke(ctx context.Context, invocation Invocation, sink InvocationSink) error {
	if sink == nil {
		return fmt.Errorf("claude runtime sink is required")
	}
	if err := validateClaudeInvocation(invocation); err != nil {
		return err
	}
	prompt, err := claudePrompt(invocation.Messages)
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

	runner := c.Runner
	if runner == nil {
		runner = OSProcessRunner{}
	}
	executable := c.Executable
	if executable == "" {
		executable = "claude"
	}
	environment := c.Environment
	if environment == nil {
		environment = claudeSubscriptionEnvironment()
	}

	args := claudeArgs(processInvocation)
	stream := &claudeStream{sink: sink, requestedModel: invocation.Model, invocation: invocation}
	runCtx := ctx
	cancel := func() {}
	if invocation.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, invocation.Timeout)
	}
	defer cancel()
	startedAt := time.Now()
	processResult, runErr := runner.Run(runCtx, ProcessRequest{
		Executable:       executable,
		Args:             args,
		Dir:              workspace,
		Env:              append([]string(nil), environment...),
		Stdin:            prompt,
		Limits:           invocation.Limits,
		Containment:      invocation.Containment,
		TerminationStage: invocation.TerminationStage,
		StdoutLine: func(line []byte) error {
			return stream.consume(runCtx, line)
		},
	})
	if emitErr := emitExternalProcessRecord(runCtx, sink, workspace, processResult, runErr, time.Since(startedAt)); emitErr != nil {
		return errors.Join(runErr, emitErr)
	}
	if runErr != nil {
		return classifyClaudeProcessError(runErr, processResult)
	}
	result, err := stream.externalResult(invocation)
	if err != nil {
		return err
	}
	if err := sink.Complete(runCtx, result); err != nil {
		return fmt.Errorf("complete claude runtime result: %w", err)
	}
	return nil
}

func validateClaudeInvocation(invocation Invocation) error {
	if invocation.Runtime != "" && invocation.Runtime != ClaudeRuntimeName {
		return fmt.Errorf("claude runtime cannot execute runtime %q", invocation.Runtime)
	}
	if strings.TrimSpace(invocation.Model) == "" {
		return fmt.Errorf("claude runtime model is required")
	}
	profile := invocation.Profile
	if profile == "" {
		profile = DefaultProfile
	}
	if profile != CapabilityReason && profile != CapabilityInspect && profile != CapabilityWork {
		return fmt.Errorf("claude runtime profile %q is unsupported", profile)
	}
	if invocation.Output.Type != "json" {
		return fmt.Errorf("claude reason profile requires JSON output")
	}
	if len(invocation.Output.Schema) == 0 || !json.Valid(invocation.Output.Schema) {
		return fmt.Errorf("claude reason profile requires a valid JSON Schema")
	}
	if err := validateProfileWorkspace(profile, invocation.Workspace); err != nil {
		return fmt.Errorf("claude runtime: %w", err)
	}
	if err := validateProfileCapabilities(profile, invocation.Capabilities, invocation.MCPServers); err != nil {
		return fmt.Errorf("claude runtime: %w", err)
	}
	switch invocation.Session.Mode {
	case "", SessionFresh:
		if invocation.Session.ID != "" {
			return fmt.Errorf("fresh claude sessions cannot specify a resume id")
		}
	case SessionSticky, SessionResume:
		if invocation.Session.Mode == SessionResume && invocation.Session.ID == "" {
			return fmt.Errorf("resumed claude sessions require a source id")
		}
	case SessionFork:
		if invocation.Session.ID == "" {
			return fmt.Errorf("forked claude sessions require a source id")
		}
	default:
		return fmt.Errorf("unsupported claude session mode %q", invocation.Session.Mode)
	}
	return nil
}

func claudeArgs(invocation Invocation) []string {
	tools := claudeTools(invocation)
	// --safe-mode also disables servers supplied by --mcp-config. Restricted
	// mode plus strict config and the exact tool lists below exclude ambient
	// customisation while preserving invocation-scoped capabilities.
	args := []string{
		"--print",
		"--input-format", "text",
		"--output-format", "stream-json",
		"--verbose",
		"--restricted",
		"--strict-mcp-config",
		"--mcp-config", claudeMCPConfig(invocation.MCPServers),
		"--disable-slash-commands",
		"--no-chrome",
		"--tools", tools,
		"--permission-mode", claudePermissionMode(invocation),
		"--model", invocation.Model,
		"--json-schema", string(invocation.Output.Schema),
		"--system-prompt", invocation.Persona,
	}
	if len(invocation.MCPServers) > 0 {
		args = append(args, "--allowedTools", tools)
	}
	if invocation.Session.Mode == SessionSticky || invocation.Session.Mode == SessionResume || invocation.Session.Mode == SessionFork {
		args = append(args, "--system-prompt-snapshot", "on")
		if invocation.Session.ID != "" {
			args = append(args, "--resume", invocation.Session.ID)
		}
		if invocation.Session.Mode == SessionFork {
			args = append(args, "--fork-session")
		}
	} else {
		args = append(args, "--no-session-persistence")
	}
	return args
}

func claudeTools(invocation Invocation) string {
	var tools []string
	switch invocation.Profile {
	case CapabilityInspect:
		tools = []string{"Read", "Glob", "Grep"}
	case CapabilityWork:
		tools = []string{"Read", "Glob", "Grep", "Edit", "Write"}
	}
	for _, server := range invocation.MCPServers {
		for _, tool := range server.Tools {
			tools = append(tools, "mcp__"+server.Name+"__"+tool)
		}
	}
	return strings.Join(tools, ",")
}

func claudeMCPConfig(servers []MCPServer) string {
	entries := make(map[string]map[string]any, len(servers))
	for _, server := range servers {
		entries[server.Name] = map[string]any{"type": "http", "url": server.URL}
	}
	data, _ := json.Marshal(map[string]any{"mcpServers": entries})
	return string(data)
}

func claudePermissionMode(invocation Invocation) string {
	if len(invocation.MCPServers) > 0 {
		return "dontAsk"
	}
	if invocation.Profile == CapabilityWork {
		return "acceptEdits"
	}
	return "plan"
}

func claudePrompt(messages []Message) ([]byte, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("claude runtime requires at least one message")
	}
	if len(messages) == 1 && messages[0].Role == "user" && messages[0].ToolCall == nil && messages[0].ToolResult == nil {
		return []byte(messages[0].Text), nil
	}
	var prompt strings.Builder
	for _, message := range messages {
		if message.ToolCall != nil || message.ToolResult != nil {
			return nil, fmt.Errorf("claude external runtime does not accept Smith-owned tool turns")
		}
		if message.Role != "user" && message.Role != "assistant" {
			return nil, fmt.Errorf("claude external runtime does not accept message role %q", message.Role)
		}
		fmt.Fprintf(&prompt, "## %s\n\n%s\n\n", message.Role, message.Text)
	}
	return []byte(strings.TrimSpace(prompt.String())), nil
}

func claudeSubscriptionEnvironment() []string {
	// OAuth credentials are found through the user's config home. API-key and
	// third-party-provider variables are intentionally not forwarded.
	keys := []string{
		"HOME", "USERPROFILE", "USER", "LOGNAME", "PATH",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
		"APPDATA", "LOCALAPPDATA",
	}
	environment := make([]string, 0, len(keys)+2)
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			environment = append(environment, key+"="+value)
		}
	}
	return append(environment, "TERM=dumb", "NO_COLOR=1")
}

type claudeStream struct {
	sink           InvocationSink
	requestedModel string
	invocation     Invocation
	init           *claudeInit
	result         *claudeResult
}

type claudeEnvelope struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
}

type claudeInit struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype"`
	SessionID      string          `json:"session_id"`
	Model          string          `json:"model"`
	PermissionMode string          `json:"permissionMode"`
	Tools          []string        `json:"tools"`
	MCPServers     []any           `json:"mcp_servers"`
	Version        string          `json:"claude_code_version"`
	APIKeySource   string          `json:"apiKeySource"`
	SlashCommands  []string        `json:"slash_commands"`
	Skills         []string        `json:"skills"`
	Plugins        []any           `json:"plugins"`
	Raw            json.RawMessage `json:"-"`
}

type claudeRetry struct {
	Attempt      int    `json:"attempt"`
	MaxRetries   int    `json:"max_retries"`
	RetryDelayMS int    `json:"retry_delay_ms"`
	ErrorStatus  any    `json:"error_status"`
	Error        string `json:"error"`
}

type claudeAssistant struct {
	Message struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"content"`
	} `json:"message"`
}

type claudeResult struct {
	Type              string                      `json:"type"`
	Subtype           string                      `json:"subtype"`
	IsError           bool                        `json:"is_error"`
	Result            string                      `json:"result"`
	StructuredOutput  json.RawMessage             `json:"structured_output"`
	SessionID         string                      `json:"session_id"`
	DurationMS        int64                       `json:"duration_ms"`
	DurationAPIMS     int64                       `json:"duration_api_ms"`
	NumTurns          *int                        `json:"num_turns"`
	TerminalReason    string                      `json:"terminal_reason"`
	PermissionDenials []json.RawMessage           `json:"permission_denials"`
	TotalCostUSD      *float64                    `json:"total_cost_usd"`
	Usage             claudeUsage                 `json:"usage"`
	ModelUsage        map[string]claudeModelUsage `json:"modelUsage"`
	Raw               json.RawMessage             `json:"-"`
}

type claudeUsage struct {
	InputTokens              *int            `json:"input_tokens"`
	OutputTokens             *int            `json:"output_tokens"`
	CacheCreationInputTokens *int            `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int            `json:"cache_read_input_tokens"`
	Raw                      json.RawMessage `json:"-"`
}

type claudeModelUsage struct {
	CanonicalModel string   `json:"canonicalModel"`
	CostUSD        *float64 `json:"costUSD"`
	CostBasis      string   `json:"costBasis"`
}

func (s *claudeStream) consume(ctx context.Context, line []byte) error {
	var envelope claudeEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return fmt.Errorf("%w: invalid JSON line: %v", ErrClaudeProtocol, err)
	}
	switch envelope.Type {
	case "system":
		switch envelope.Subtype {
		case "init":
			if s.init != nil {
				return fmt.Errorf("%w: duplicate init event", ErrClaudeProtocol)
			}
			var init claudeInit
			if err := json.Unmarshal(line, &init); err != nil {
				return fmt.Errorf("%w: decode init event: %v", ErrClaudeProtocol, err)
			}
			init.Raw = append(json.RawMessage(nil), line...)
			if err := validateClaudeInit(init, s.invocation); err != nil {
				return err
			}
			s.init = &init
			data, _ := json.Marshal(map[string]any{
				"session_id":      init.SessionID,
				"model":           init.Model,
				"adapter_version": init.Version,
				"permission_mode": init.PermissionMode,
				"tools":           init.Tools,
			})
			return s.sink.Emit(ctx, RuntimeEvent{Type: "claude.session.started", Data: data})
		case "api_retry":
			var retry claudeRetry
			if err := json.Unmarshal(line, &retry); err != nil {
				return fmt.Errorf("%w: decode retry event: %v", ErrClaudeProtocol, err)
			}
			data, _ := json.Marshal(map[string]any{
				"attempt": retry.Attempt, "max_retries": retry.MaxRetries,
				"retry_delay_ms": retry.RetryDelayMS, "error_status": retry.ErrorStatus,
			})
			return s.sink.Emit(ctx, RuntimeEvent{Type: "claude.api.retry", Message: retry.Error, Data: data})
		}
	case "assistant":
		var assistant claudeAssistant
		if err := json.Unmarshal(line, &assistant); err != nil {
			return fmt.Errorf("%w: decode assistant event: %v", ErrClaudeProtocol, err)
		}
		contentTypes := make([]string, 0, len(assistant.Message.Content))
		for _, content := range assistant.Message.Content {
			if content.Type == "thinking" {
				continue
			}
			if content.Name != "" {
				contentTypes = append(contentTypes, content.Type+":"+content.Name)
			} else {
				contentTypes = append(contentTypes, content.Type)
			}
		}
		if len(contentTypes) > 0 {
			data, _ := json.Marshal(map[string]any{
				"message_id":    assistant.Message.ID,
				"model":         assistant.Message.Model,
				"content_types": contentTypes,
				"stop_reason":   assistant.Message.StopReason,
			})
			return s.sink.Emit(ctx, RuntimeEvent{Type: "claude.turn", Data: data})
		}
	case "result":
		if s.result != nil {
			return fmt.Errorf("%w: duplicate result event", ErrClaudeProtocol)
		}
		var result claudeResult
		if err := json.Unmarshal(line, &result); err != nil {
			return fmt.Errorf("%w: decode result event: %v", ErrClaudeProtocol, err)
		}
		result.Raw = append(json.RawMessage(nil), line...)
		var rawEnvelope struct {
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(line, &rawEnvelope) == nil {
			result.Usage.Raw = append(json.RawMessage(nil), rawEnvelope.Usage...)
		}
		s.result = &result
	}
	return nil
}

func (s *claudeStream) externalResult(invocation Invocation) (*ExternalResult, error) {
	if s.init == nil {
		return nil, fmt.Errorf("%w: missing init event", ErrClaudeProtocol)
	}
	if s.result == nil {
		return nil, fmt.Errorf("%w: missing terminal result event", ErrClaudeProtocol)
	}
	if s.result.IsError || s.result.Subtype != "success" {
		return nil, classifyClaudeMessage(s.result.Result)
	}

	structured := append(json.RawMessage(nil), s.result.StructuredOutput...)
	if len(structured) == 0 || string(structured) == "null" {
		structured = json.RawMessage(s.result.Result)
	}
	if !json.Valid(structured) {
		return nil, fmt.Errorf("%w: terminal result did not contain valid structured output", ErrClaudeProtocol)
	}

	cachedTokens := sumOptionalInts(s.result.Usage.CacheCreationInputTokens, s.result.Usage.CacheReadInputTokens)
	canonicalModel, billingBasis := s.modelProvenance()
	terminalReason := s.result.TerminalReason
	if terminalReason == "" {
		terminalReason = s.result.Subtype
	}
	usageRaw, _ := json.Marshal(map[string]any{
		"usage":           s.result.Usage.Raw,
		"model_usage":     s.result.ModelUsage,
		"total_cost_usd":  s.result.TotalCostUSD,
		"duration_api_ms": s.result.DurationAPIMS,
	})
	provenanceRaw, _ := json.Marshal(map[string]json.RawMessage{
		"init": s.init.Raw, "result": s.result.Raw,
	})
	reportedCost := s.result.TotalCostUSD
	if reportedCost == nil {
		if modelUsage, ok := s.result.ModelUsage[s.requestedModel]; ok && modelUsage.CostBasis == "list" {
			reportedCost = modelUsage.CostUSD
		} else if modelUsage, ok := s.result.ModelUsage[s.init.Model]; ok && modelUsage.CostBasis == "list" {
			reportedCost = modelUsage.CostUSD
		}
	}
	return &ExternalResult{
		JSON: structured,
		Usage: Usage{
			InputTokens:       cloneInt(s.result.Usage.InputTokens),
			OutputTokens:      cloneInt(s.result.Usage.OutputTokens),
			CachedInputTokens: cachedTokens,
			Turns:             cloneInt(s.result.NumTurns),
			Raw:               usageRaw,
		},
		Provenance: Provenance{
			Adapter:           ClaudeRuntimeName,
			AdapterVersion:    ClaudeAdapterVersion,
			ProtocolVersion:   ClaudeProtocolVersion,
			CLIVersion:        s.init.Version,
			RequestedModel:    invocation.Model,
			CanonicalModel:    canonicalModel,
			SessionID:         s.result.SessionID,
			TerminalReason:    terminalReason,
			BillingBasis:      billingBasis,
			ReportedCostUSD:   cloneFloat(reportedCost),
			PermissionDenials: claudePermissionDenials(s.result.PermissionDenials),
			Raw:               provenanceRaw,
		},
		Duration: time.Duration(s.result.DurationMS) * time.Millisecond,
	}, nil
}

func validateClaudeInit(init claudeInit, invocation Invocation) error {
	if init.SessionID == "" || init.Model == "" || init.Version == "" {
		return fmt.Errorf("%w: init event omitted session, model, or adapter version", ErrClaudeProtocol)
	}
	if init.APIKeySource == "" {
		return fmt.Errorf("%w: init event omitted apiKeySource", ErrClaudeProtocol)
	}
	if init.APIKeySource != "none" {
		return fmt.Errorf("%w: init reported apiKeySource %q", ErrClaudeSubscriptionAuth, init.APIKeySource)
	}
	wantPermission := claudePermissionMode(invocation)
	if init.PermissionMode != wantPermission {
		return fmt.Errorf("%w: expected %s permission mode, got %q", ErrClaudeProtocol, wantPermission, init.PermissionMode)
	}
	if len(init.MCPServers) != len(invocation.MCPServers) || len(init.SlashCommands) != 0 || len(init.Skills) != 0 || len(init.Plugins) != 0 {
		return fmt.Errorf("%w: safe invocation loaded ambient MCP, commands, skills, or plugins", ErrClaudeProtocol)
	}
	wantTools := strings.Split(claudeTools(invocation), ",")
	if len(wantTools) == 1 && wantTools[0] == "" {
		wantTools = nil
	}
	wantTools = append(wantTools, "StructuredOutput")
	if !sameStringSet(init.Tools, wantTools) {
		return fmt.Errorf("%w: safe invocation exposed unexpected tools %v", ErrClaudeProtocol, init.Tools)
	}
	return nil
}

func sameStringSet(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	values := make(map[string]int, len(actual))
	for _, value := range actual {
		values[value]++
	}
	for _, value := range expected {
		values[value]--
	}
	for _, count := range values {
		if count != 0 {
			return false
		}
	}
	return true
}

func (s *claudeStream) modelProvenance() (string, string) {
	model := s.init.Model
	usage, ok := s.result.ModelUsage[s.requestedModel]
	if !ok {
		usage, ok = s.result.ModelUsage[s.init.Model]
	}
	if ok && usage.CanonicalModel != "" {
		model = usage.CanonicalModel
	}
	basis := BillingSubscription
	if ok && usage.CostBasis == "list" {
		basis = BillingReportedListEstimate
	} else if s.result.TotalCostUSD != nil {
		basis = BillingReportedListEstimate
	}
	return model, basis
}

func sumOptionalInts(values ...*int) *int {
	total := 0
	found := false
	for _, value := range values {
		if value != nil {
			total += *value
			found = true
		}
	}
	if !found {
		return nil
	}
	return &total
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func claudePermissionDenials(values []json.RawMessage) []string {
	denials := make([]string, 0, len(values))
	for _, value := range values {
		var text string
		if json.Unmarshal(value, &text) == nil && text != "" {
			denials = append(denials, text)
			continue
		}
		var object struct {
			ToolName string `json:"tool_name"`
			Reason   string `json:"reason"`
		}
		if json.Unmarshal(value, &object) == nil && object.ToolName != "" {
			if object.Reason != "" {
				denials = append(denials, object.ToolName+": "+object.Reason)
			} else {
				denials = append(denials, object.ToolName)
			}
			continue
		}
		denials = append(denials, string(value))
	}
	return denials
}

func classifyClaudeProcessError(err error, result ProcessResult) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrProcessResourceLimit) || errors.Is(err, ErrProcessContainmentUnavailable) {
		return err
	}
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%w: %v", ErrClaudeExecutableNotFound, err)
	}
	var executableErr *exec.Error
	if errors.As(err, &executableErr) {
		return fmt.Errorf("%w: %v", ErrClaudeExecutableNotFound, err)
	}
	diagnostic := strings.TrimSpace(string(result.Stderr) + "\n" + string(result.Stdout))
	classified := classifyClaudeMessage(diagnostic)
	if errors.Is(classified, ErrClaudeUnauthenticated) || errors.Is(classified, ErrClaudeUnsupportedModel) {
		return classified
	}
	return err
}

func classifyClaudeMessage(message string) error {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "not logged in"),
		strings.Contains(lower, "authentication required"),
		strings.Contains(lower, "claude auth login"),
		strings.Contains(lower, "please login"),
		strings.Contains(lower, "invalid api key"):
		return fmt.Errorf("%w: %s", ErrClaudeUnauthenticated, strings.TrimSpace(message))
	case strings.Contains(lower, "unsupported model"),
		strings.Contains(lower, "invalid model"),
		strings.Contains(lower, "model not found"),
		strings.Contains(lower, "model does not exist"),
		strings.Contains(lower, "does not exist or you do not have access"),
		strings.Contains(lower, "you do not have access to the model"):
		return fmt.Errorf("%w: %s", ErrClaudeUnsupportedModel, strings.TrimSpace(message))
	default:
		return fmt.Errorf("%w: %s", ErrClaudeInvocation, strings.TrimSpace(message))
	}
}
