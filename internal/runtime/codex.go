package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	CodexRuntimeName     = "codex"
	CodexAdapterVersion  = "4"
	CodexProtocolVersion = "codex-exec-jsonl/1"
)

var (
	ErrCodexExecutableNotFound = errors.New("codex executable not found")
	ErrCodexUnauthenticated    = errors.New("codex cli is not authenticated")
	ErrCodexUnsupportedModel   = errors.New("codex model is unsupported")
	ErrCodexConfiguration      = errors.New("codex configuration was rejected")
	ErrCodexProtocol           = errors.New("codex JSONL protocol error")
	ErrCodexUndeclaredTool     = errors.New("codex attempted an undeclared tool")
	ErrCodexInvocation         = errors.New("codex invocation failed")
)

// CodexRuntime executes Codex as a complete external agent. The reason profile
// uses ChatGPT-managed authentication, a read-only sandbox, and explicit
// configuration that excludes ambient instructions, tools, plugins, and MCP.
type CodexRuntime struct {
	Runner      ProcessRunner
	Admitter    ContainmentAdmitter
	Executable  string
	Environment []string
}

func (c *CodexRuntime) Admit(ctx context.Context, request ContainmentRequest) (ContainmentAdmission, error) {
	if c.Admitter != nil {
		return c.Admitter.Admit(ctx, request)
	}
	return (HostContainmentAdmitter{}).Admit(ctx, request)
}

func NewCodexRuntime() *CodexRuntime {
	return &CodexRuntime{
		Runner:     OSProcessRunner{},
		Executable: "codex",
	}
}

var codexWireSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "result_json": {
      "type": "string",
      "description": "The complete requested Smith output encoded as JSON."
    }
  },
  "required": ["result_json"]
}`)

func (c *CodexRuntime) Invoke(ctx context.Context, invocation Invocation, sink InvocationSink) error {
	if sink == nil {
		return fmt.Errorf("codex runtime sink is required")
	}
	if err := validateCodexInvocation(invocation); err != nil {
		return err
	}
	prompt, err := codexPrompt(invocation.Messages, invocation.Output.Schema)
	if err != nil {
		return err
	}
	workspace, cleanupWorkspace, err := processWorkspace(invocation.Workspace)
	if err != nil {
		return err
	}
	defer cleanupWorkspace()
	if invocation.Profile == CapabilityWork {
		if err := validateCodexProtectedDirectories(workspace); err != nil {
			return err
		}
	}
	processInvocation := invocation
	processInvocation.Workspace.Root = workspace
	schemaPath, cleanup, err := writeCodexSchema()
	if err != nil {
		return err
	}
	defer cleanup()

	runner := c.Runner
	if runner == nil {
		runner = OSProcessRunner{}
	}
	executable := c.Executable
	if executable == "" {
		executable = "codex"
	}
	environment := c.Environment
	cleanupHome := func() {}
	if environment == nil {
		environment = codexSubscriptionEnvironment()
		environment, cleanupHome, err = codexSterileEnvironment(environment)
		if err != nil {
			return err
		}
	}
	defer cleanupHome()
	shellPath := environmentValue(environment, "PATH")
	if shellPath == "" {
		// A caller-supplied environment is an explicit injection boundary. Keep
		// it byte-for-byte intact, but still give Codex's work shell a usable,
		// deterministic base path instead of the CLI's bundled rg-only path.
		shellPath = "/usr/bin:/bin"
	}

	stream := &codexStream{sink: sink, profile: invocation.Profile, mcpServers: invocation.MCPServers, cwd: workspace}
	runCtx := ctx
	cancel := func() {}
	if invocation.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, invocation.Timeout)
	}
	defer cancel()
	startedAt := time.Now()
	processResult, runErr := runner.Run(runCtx, ProcessRequest{
		Executable:       executable,
		Args:             codexArgs(processInvocation, schemaPath, shellPath),
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
	duration := time.Since(startedAt)
	if emitErr := emitExternalProcessRecord(runCtx, sink, workspace, processResult, runErr, duration); emitErr != nil {
		return errors.Join(runErr, emitErr)
	}
	if runErr != nil {
		return classifyCodexProcessError(runErr, processResult)
	}
	result, err := stream.externalResult(invocation, duration)
	if err != nil {
		return err
	}
	if err := sink.Complete(runCtx, result); err != nil {
		return fmt.Errorf("complete codex runtime result: %w", err)
	}
	return nil
}

// Codex's workspace sandbox protects these directories with read-only mounts.
// A regular-file placeholder makes Codex 0.153.0's Linux bwrap setup fail before
// the shell starts ("Can't write data to file ...: Bad file descriptor"). Never
// remove or replace the path: report the workspace problem before a model call.
// .git is deliberately excluded: valid worktrees use a gitdir pointer file.
func validateCodexProtectedDirectories(workspace string) error {
	for _, name := range []string{".codex", ".agents"} {
		path := filepath.Join(workspace, name)
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			// A dangling symlink is not the same as an absent protected path.
			if _, linkErr := os.Lstat(path); errors.Is(linkErr, os.ErrNotExist) {
				continue
			}
		}
		if err != nil {
			return fmt.Errorf("%w: inspect protected workspace directory %s: %v", ErrCodexConfiguration, path, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: protected workspace path %s must be a directory, not a file; use a fresh checkout or repair it explicitly before running work (path left unchanged)", ErrCodexConfiguration, path)
		}
	}
	return nil
}

func validateCodexInvocation(invocation Invocation) error {
	if invocation.Runtime != "" && invocation.Runtime != CodexRuntimeName {
		return fmt.Errorf("codex runtime cannot execute runtime %q", invocation.Runtime)
	}
	if strings.TrimSpace(invocation.Model) == "" {
		return fmt.Errorf("codex runtime model is required")
	}
	profile := invocation.Profile
	if profile == "" {
		profile = DefaultProfile
	}
	if profile != CapabilityReason && profile != CapabilityInspect && profile != CapabilityWork {
		return fmt.Errorf("codex runtime profile %q is unsupported", profile)
	}
	if err := validateProfileWorkspace(profile, invocation.Workspace); err != nil {
		return fmt.Errorf("codex runtime: %w", err)
	}
	if err := validateProfileCapabilities(profile, invocation.Capabilities, invocation.MCPServers); err != nil {
		return fmt.Errorf("codex runtime: %w", err)
	}
	if invocation.Output.Type != "json" {
		return fmt.Errorf("codex reason profile requires JSON output")
	}
	if len(invocation.Output.Schema) == 0 || !json.Valid(invocation.Output.Schema) {
		return fmt.Errorf("codex reason profile requires a valid JSON Schema")
	}
	switch invocation.Session.Mode {
	case "", SessionFresh:
		if invocation.Session.ID != "" {
			return fmt.Errorf("fresh codex sessions cannot specify a resume id")
		}
	case SessionSticky, SessionResume:
		if invocation.Session.Mode == SessionResume && invocation.Session.ID == "" {
			return fmt.Errorf("resumed codex sessions require a source id")
		}
	case SessionFork:
		if invocation.Session.ID == "" {
			return fmt.Errorf("forked codex sessions require a source id")
		}
	default:
		return fmt.Errorf("unsupported codex session mode %q", invocation.Session.Mode)
	}
	return nil
}

func codexArgs(invocation Invocation, schemaPath, shellPath string) []string {
	args := []string{"exec"}
	resuming := invocation.Session.Mode == SessionSticky && invocation.Session.ID != ""
	if resuming || invocation.Session.Mode == SessionResume {
		args = append(args, "resume")
	} else if invocation.Session.Mode == SessionFork {
		args = append(args, "fork")
	}
	args = append(args, codexCommonArgs(invocation, schemaPath, shellPath)...)
	if resuming || invocation.Session.Mode == SessionResume || invocation.Session.Mode == SessionFork {
		args = append(args, "--config", `sandbox_mode="`+codexSandbox(invocation.Profile)+`"`, invocation.Session.ID, "-")
		return args
	}
	args = append(args,
		"--sandbox", codexSandbox(invocation.Profile),
		"--cd", invocation.Workspace.Root,
		"--color", "never",
	)
	if invocation.Session.Mode == "" || invocation.Session.Mode == SessionFresh {
		args = append(args, "--ephemeral")
	}
	return append(args, "-")
}

func codexCommonArgs(invocation Invocation, schemaPath, shellPath string) []string {
	args := []string{
		"--strict-config",
		"--ignore-user-config",
		"--ignore-rules",
		"--skip-git-repo-check",
		"--json",
		"--model", invocation.Model,
		"--output-schema", schemaPath,
		"--config", `forced_login_method="chatgpt"`,
		"--config", "developer_instructions=" + tomlString(invocation.Persona),
		"--config", "project_doc_max_bytes=0",
		"--config", "project_doc_fallback_filenames=[]",
		"--config", `web_search="disabled"`,
		"--config", `shell_environment_policy.inherit="none"`,
		"--config", "shell_environment_policy.set.PATH=" + tomlString(shellPath),
		"--config", `shell_environment_policy.set.BASH_ENV="/dev/null"`,
		"--config", `shell_environment_policy.set.ENV="/dev/null"`,
		"--config", "allow_login_shell=false",
	}
	disabled := []string{
		"apps", "browser_use", "browser_use_external", "computer_use", "goals",
		"hooks", "image_generation", "memories", "multi_agent", "plugins",
		"recommended_plugins", "shell_snapshot", "skill_mcp_dependency_install",
		"skill_search", "sleep_tool", "tool_suggest", "view_image",
	}
	if invocation.Profile == "" || invocation.Profile == CapabilityReason {
		disabled = append(disabled, "shell_tool")
	}
	for _, feature := range disabled {
		args = append(args, "--disable", feature)
	}
	for _, server := range invocation.MCPServers {
		args = append(args,
			"--config", "mcp_servers."+server.Name+".url="+tomlString(server.URL),
			"--config", "mcp_servers."+server.Name+".required=true",
			"--config", "mcp_servers."+server.Name+".enabled_tools="+tomlStringSlice(server.Tools),
			"--config", "mcp_servers."+server.Name+".default_tools_approval_mode=\"approve\"",
		)
	}
	return args
}

func codexSandbox(profile string) string {
	if profile == CapabilityWork {
		return "workspace-write"
	}
	return "read-only"
}

func tomlString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func tomlStringSlice(values []string) string {
	encoded := make([]string, len(values))
	for index, value := range values {
		encoded[index] = tomlString(value)
	}
	return "[" + strings.Join(encoded, ",") + "]"
}

func codexPrompt(messages []Message, schema json.RawMessage) ([]byte, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("codex runtime requires at least one message")
	}
	var prompt strings.Builder
	if len(messages) == 1 && messages[0].Role == "user" && messages[0].ToolCall == nil && messages[0].ToolResult == nil {
		prompt.WriteString(messages[0].Text)
	} else {
		for _, message := range messages {
			if message.ToolCall != nil || message.ToolResult != nil {
				return nil, fmt.Errorf("codex external runtime does not accept Smith-owned tool turns")
			}
			if message.Role != "user" && message.Role != "assistant" {
				return nil, fmt.Errorf("codex external runtime does not accept message role %q", message.Role)
			}
			fmt.Fprintf(&prompt, "## %s\n\n%s\n\n", message.Role, message.Text)
		}
	}
	fmt.Fprintf(&prompt, "\n\n## smith structured output\n\nReturn the complete requested Smith output as JSON encoded inside the `result_json` string required by the response schema. The decoded value must satisfy this original Smith JSON Schema:\n\n%s\n\nDo not omit existing baton fields unless the task explicitly removes them.", schema)
	return []byte(strings.TrimSpace(prompt.String())), nil
}

func writeCodexSchema() (string, func(), error) {
	file, err := os.CreateTemp("", "smith-codex-schema-*.json")
	if err != nil {
		return "", func() {}, fmt.Errorf("create codex output schema: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := file.Write(codexWireSchema); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write codex output schema: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close codex output schema: %w", err)
	}
	return path, cleanup, nil
}

func codexSubscriptionEnvironment() []string {
	// Codex auth is discovered through CODEX_HOME (or HOME/.codex). API keys
	// are intentionally excluded and forced_login_method pins ChatGPT auth.
	keys := []string{
		"HOME", "CODEX_HOME", "USERPROFILE", "USER", "LOGNAME", "PATH",
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

func codexSterileEnvironment(environment []string) ([]string, func(), error) {
	home, err := os.MkdirTemp("", "smith-codex-home-")
	if err != nil {
		return nil, func() {}, fmt.Errorf("create sterile codex home: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(home) }
	codexHome := environmentValue(environment, "CODEX_HOME")
	if environmentValue(environment, "PATH") == "" {
		cleanup()
		return nil, func() {}, fmt.Errorf("codex subscription runtime requires an explicit PATH for tool execution")
	}
	if codexHome == "" {
		originalHome := environmentValue(environment, "HOME")
		if originalHome == "" {
			cleanup()
			return nil, func() {}, fmt.Errorf("codex subscription runtime requires HOME or CODEX_HOME for authentication")
		}
		codexHome = filepath.Join(originalHome, ".codex")
	}

	rewritten := make([]string, 0, len(environment)+7)
	for _, value := range environment {
		key, _, found := strings.Cut(value, "=")
		if !found {
			continue
		}
		switch key {
		case "HOME", "CODEX_HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "APPDATA", "LOCALAPPDATA":
			continue
		default:
			rewritten = append(rewritten, value)
		}
	}
	rewritten = append(rewritten,
		"HOME="+home,
		"USERPROFILE="+home,
		"CODEX_HOME="+codexHome,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"APPDATA="+filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA="+filepath.Join(home, "AppData", "Local"),
	)
	return rewritten, cleanup, nil
}

func environmentValue(environment []string, wanted string) string {
	for _, value := range environment {
		key, item, found := strings.Cut(value, "=")
		if found && key == wanted {
			return item
		}
	}
	return ""
}

type codexStream struct {
	sink          InvocationSink
	profile       string
	mcpServers    []MCPServer
	cwd           string
	threadStarted json.RawMessage
	threadID      string
	turns         int
	message       string
	turnCompleted *codexTurnCompleted
}

type codexEnvelope struct {
	Type string `json:"type"`
}

type codexThreadStarted struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
}

type codexItemEnvelope struct {
	Type string    `json:"type"`
	Item codexItem `json:"item"`
}

type codexItem struct {
	ID               string            `json:"id"`
	Type             string            `json:"type"`
	Text             string            `json:"text"`
	Command          string            `json:"command"`
	AggregatedOutput string            `json:"aggregated_output"`
	ExitCode         *int              `json:"exit_code"`
	Status           string            `json:"status"`
	Changes          []codexFileChange `json:"changes"`
}

type codexFileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type codexTurnCompleted struct {
	Type  string     `json:"type"`
	Usage codexUsage `json:"usage"`
	Raw   json.RawMessage
}

type codexUsage struct {
	InputTokens           *int            `json:"input_tokens"`
	CachedInputTokens     *int            `json:"cached_input_tokens"`
	CacheWriteInputTokens *int            `json:"cache_write_input_tokens"`
	OutputTokens          *int            `json:"output_tokens"`
	ReasoningOutputTokens *int            `json:"reasoning_output_tokens"`
	Raw                   json.RawMessage `json:"-"`
}

func (s *codexStream) consume(ctx context.Context, line []byte) error {
	var envelope codexEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return fmt.Errorf("%w: invalid JSON line: %v", ErrCodexProtocol, err)
	}
	switch envelope.Type {
	case "thread.started":
		if s.threadID != "" {
			return fmt.Errorf("%w: duplicate thread.started event", ErrCodexProtocol)
		}
		var started codexThreadStarted
		if err := json.Unmarshal(line, &started); err != nil || started.ThreadID == "" {
			return fmt.Errorf("%w: thread.started omitted thread_id", ErrCodexProtocol)
		}
		s.threadID = started.ThreadID
		s.threadStarted = append(json.RawMessage(nil), line...)
		data, _ := json.Marshal(map[string]string{"session_id": started.ThreadID})
		return s.sink.Emit(ctx, RuntimeEvent{Type: "codex.session.started", Data: data})
	case "turn.started":
		if s.threadID == "" || s.turnCompleted != nil {
			return fmt.Errorf("%w: turn.started occurred outside an active thread", ErrCodexProtocol)
		}
		s.turns++
		return s.sink.Emit(ctx, RuntimeEvent{Type: "codex.turn.started"})
	case "item.started", "item.updated", "item.completed":
		if s.threadID == "" || s.turns == 0 || s.turnCompleted != nil {
			return fmt.Errorf("%w: item event occurred outside an active turn", ErrCodexProtocol)
		}
		var item codexItemEnvelope
		if err := json.Unmarshal(line, &item); err != nil || item.Item.Type == "" {
			return fmt.Errorf("%w: item event omitted item type", ErrCodexProtocol)
		}
		return s.consumeItem(ctx, envelope.Type, item.Item)
	case "turn.completed":
		if s.threadID == "" || s.turns == 0 || s.turnCompleted != nil {
			return fmt.Errorf("%w: unexpected turn.completed event", ErrCodexProtocol)
		}
		var completed codexTurnCompleted
		if err := json.Unmarshal(line, &completed); err != nil {
			return fmt.Errorf("%w: decode turn.completed: %v", ErrCodexProtocol, err)
		}
		var raw struct {
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(line, &raw) == nil {
			completed.Usage.Raw = append(json.RawMessage(nil), raw.Usage...)
		}
		completed.Raw = append(json.RawMessage(nil), line...)
		s.turnCompleted = &completed
		return s.sink.Emit(ctx, RuntimeEvent{Type: "codex.turn.completed", Data: completed.Usage.Raw})
	case "turn.failed", "error":
		var failed struct {
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(line, &failed)
		message := failed.Message
		if message == "" {
			message = failed.Error.Message
		}
		return classifyCodexMessage(message)
	default:
		return fmt.Errorf("%w: unknown event type %q", ErrCodexProtocol, envelope.Type)
	}
}

func (s *codexStream) consumeItem(ctx context.Context, eventType string, item codexItem) error {
	switch item.Type {
	case "agent_message":
		if eventType == "item.completed" {
			s.message = item.Text
			data, _ := json.Marshal(map[string]string{"item_id": item.ID, "kind": item.Type})
			return s.sink.Emit(ctx, RuntimeEvent{Type: "codex.agent.message", Data: data})
		}
	case "reasoning":
		if eventType == "item.completed" {
			data, _ := json.Marshal(map[string]string{"item_id": item.ID, "kind": item.Type})
			return s.sink.Emit(ctx, RuntimeEvent{Type: "codex.reasoning", Data: data})
		}
	case "plan", "plan_update":
		if eventType == "item.completed" {
			data, _ := json.Marshal(map[string]string{"item_id": item.ID, "kind": item.Type})
			return s.sink.Emit(ctx, RuntimeEvent{Type: "codex.plan", Data: data})
		}
	case "command_execution":
		if s.profile == CapabilityInspect || s.profile == CapabilityWork {
			if item.ID == "" || item.Command == "" {
				return fmt.Errorf("%w: command_execution omitted item id or command", ErrCodexProtocol)
			}
			record := ExternalToolRecord{
				Schema:   ExternalToolSchema,
				ItemID:   item.ID,
				Kind:     item.Type,
				State:    eventType,
				Status:   item.Status,
				Command:  pointerTo(recordExternalText(item.Command, externalCommandBytes)),
				CWD:      pointerTo(recordExternalText(s.cwd, externalPathBytes)),
				ExitCode: item.ExitCode,
			}
			if item.AggregatedOutput != "" {
				record.Output = pointerTo(recordExternalText(item.AggregatedOutput, externalOutputBytes))
			}
			data, _ := json.Marshal(record)
			return s.sink.Emit(ctx, RuntimeEvent{Type: "codex.tool.command", Data: data})
		}
		return fmt.Errorf("%w: %s (%s)", ErrCodexUndeclaredTool, item.Type, eventType)
	case "file_change":
		if s.profile == CapabilityWork {
			if item.ID == "" {
				return fmt.Errorf("%w: file_change omitted item id", ErrCodexProtocol)
			}
			changeCount := min(len(item.Changes), externalMaxChanges)
			record := ExternalToolRecord{
				Schema:         ExternalToolSchema,
				ItemID:         item.ID,
				Kind:           item.Type,
				State:          eventType,
				Status:         item.Status,
				CWD:            pointerTo(recordExternalText(s.cwd, externalPathBytes)),
				Changes:        make([]ExternalFileChange, 0, changeCount),
				ChangesOmitted: len(item.Changes) - changeCount,
			}
			for _, change := range item.Changes[:changeCount] {
				record.Changes = append(record.Changes, ExternalFileChange{
					Path: recordExternalText(change.Path, externalPathBytes),
					Kind: change.Kind,
				})
			}
			data, _ := json.Marshal(record)
			return s.sink.Emit(ctx, RuntimeEvent{Type: "codex.tool.file_change", Data: data})
		}
		return fmt.Errorf("%w: %s (%s)", ErrCodexUndeclaredTool, item.Type, eventType)
	case "mcp_tool_call":
		if len(s.mcpServers) > 0 {
			data, _ := json.Marshal(map[string]string{"item_id": item.ID, "kind": item.Type, "state": eventType})
			return s.sink.Emit(ctx, RuntimeEvent{Type: "codex.tool.mcp", Data: data})
		}
		return fmt.Errorf("%w: %s (%s)", ErrCodexUndeclaredTool, item.Type, eventType)
	default:
		return fmt.Errorf("%w: %s (%s)", ErrCodexUndeclaredTool, item.Type, eventType)
	}
	return nil
}

func (s *codexStream) externalResult(invocation Invocation, duration time.Duration) (*ExternalResult, error) {
	if s.threadID == "" {
		return nil, fmt.Errorf("%w: missing thread.started event", ErrCodexProtocol)
	}
	if s.turnCompleted == nil {
		return nil, fmt.Errorf("%w: missing terminal turn.completed event", ErrCodexProtocol)
	}
	var wire struct {
		ResultJSON string `json:"result_json"`
	}
	if err := json.Unmarshal([]byte(s.message), &wire); err != nil {
		return nil, fmt.Errorf("%w: final agent message was not valid structured output", ErrCodexProtocol)
	}
	if wire.ResultJSON == "" {
		return nil, fmt.Errorf("%w: final agent message was not a valid Codex wire envelope", ErrCodexProtocol)
	}
	structured := json.RawMessage(wire.ResultJSON)
	if !json.Valid(structured) {
		return nil, fmt.Errorf("%w: final agent message was not valid structured output", ErrCodexProtocol)
	}
	if s.turnCompleted.Usage.InputTokens == nil || s.turnCompleted.Usage.OutputTokens == nil {
		return nil, fmt.Errorf("%w: turn.completed omitted token usage", ErrCodexProtocol)
	}
	turns := s.turns
	raw, _ := json.Marshal(map[string]json.RawMessage{
		"thread_started": s.threadStarted,
		"turn_completed": s.turnCompleted.Raw,
	})
	return &ExternalResult{
		JSON: structured,
		Usage: Usage{
			InputTokens:       cloneInt(s.turnCompleted.Usage.InputTokens),
			OutputTokens:      cloneInt(s.turnCompleted.Usage.OutputTokens),
			CachedInputTokens: cloneInt(s.turnCompleted.Usage.CachedInputTokens),
			Turns:             &turns,
			Raw:               append(json.RawMessage(nil), s.turnCompleted.Usage.Raw...),
		},
		Provenance: Provenance{
			Adapter:         CodexRuntimeName,
			AdapterVersion:  CodexAdapterVersion,
			ProtocolVersion: CodexProtocolVersion,
			RequestedModel:  invocation.Model,
			SessionID:       s.threadID,
			TerminalReason:  "completed",
			BillingBasis:    BillingSubscription,
			Raw:             raw,
		},
		Duration: duration,
	}, nil
}

func classifyCodexProcessError(err error, result ProcessResult) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrProcessResourceLimit) || errors.Is(err, ErrProcessContainmentUnavailable) ||
		errors.Is(err, ErrCodexProtocol) || errors.Is(err, ErrCodexUndeclaredTool) ||
		errors.Is(err, ErrCodexUnauthenticated) || errors.Is(err, ErrCodexUnsupportedModel) ||
		errors.Is(err, ErrCodexConfiguration) || errors.Is(err, ErrCodexInvocation) {
		return err
	}
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%w: %v", ErrCodexExecutableNotFound, err)
	}
	var executableErr *exec.Error
	if errors.As(err, &executableErr) {
		return fmt.Errorf("%w: %v", ErrCodexExecutableNotFound, err)
	}
	diagnostic := strings.TrimSpace(string(result.Stderr) + "\n" + string(result.Stdout))
	classified := classifyCodexMessage(diagnostic)
	if !errors.Is(classified, ErrCodexInvocation) {
		return classified
	}
	return err
}

func classifyCodexMessage(message string) error {
	trimmed := strings.TrimSpace(message)
	lower := strings.ToLower(trimmed)
	switch {
	case strings.Contains(lower, "not logged in"),
		strings.Contains(lower, "login required"),
		strings.Contains(lower, "codex login"),
		strings.Contains(lower, "authentication required"),
		strings.Contains(lower, "unauthorized"):
		return fmt.Errorf("%w: %s", ErrCodexUnauthenticated, trimmed)
	case strings.Contains(lower, "unsupported model"),
		strings.Contains(lower, "invalid model"),
		strings.Contains(lower, "model not found"),
		strings.Contains(lower, "does not exist or you do not have access"):
		return fmt.Errorf("%w: %s", ErrCodexUnsupportedModel, trimmed)
	case strings.Contains(lower, "error loading config"),
		strings.Contains(lower, "unknown configuration field"),
		strings.Contains(lower, "invalid configuration"),
		strings.Contains(lower, "invalid value"):
		return fmt.Errorf("%w: %s", ErrCodexConfiguration, trimmed)
	default:
		return fmt.Errorf("%w: %s", ErrCodexInvocation, trimmed)
	}
}
