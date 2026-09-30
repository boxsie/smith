package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const completionProofNode = "completion_proof"

type ToolSession interface {
	Call(context.Context, string, any, any) error
	Close() error
}

type Connector func(context.Context) (ToolSession, error)

type Driver struct {
	Connect      Connector
	Log          io.Writer
	PollInterval time.Duration
}

type RunOptions struct {
	Root            string
	ProjectSlug     string
	PhaseID         string
	TicketID        string
	ClaudeModel     string
	CodexModel      string
	ReviewModel     string
	ResearchRoute   string
	ReviewRoute     string
	MemoryContext   []string
	Memories        []string
	ProveRecovery   bool
	EvidenceBaseDir string
}

type ContinueOptions struct {
	Root            string
	ProjectSlug     string
	RunID           string
	EvidenceBaseDir string
}

type DecisionOptions struct {
	ContinueOptions
	RequestID string
	Approved  bool
	Reason    string
}

type Result struct {
	RunID        string             `json:"run_id"`
	Status       string             `json:"status"`
	LastSequence uint64             `json:"last_sequence"`
	Gate         *GateRequest       `json:"gate,omitempty"`
	EvidencePath string             `json:"evidence_path,omitempty"`
	Receipt      *CompletionReceipt `json:"receipt,omitempty"`
}

type GateRequest struct {
	RequestID string          `json:"request_id"`
	NodeID    string          `json:"node_id"`
	Prompt    string          `json:"prompt"`
	Payload   json.RawMessage `json:"payload"`
}

type commandSession struct{ session *mcp.ClientSession }

func CommandConnector(executable string, extraEnv []string) Connector {
	return func(ctx context.Context) (ToolSession, error) {
		command := exec.CommandContext(ctx, executable, "mcp")
		command.Env = append(os.Environ(), extraEnv...)
		client := mcp.NewClient(&mcp.Implementation{Name: "smith-harness-conductor", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
		if err != nil {
			return nil, fmt.Errorf("connect smith mcp: %w", err)
		}
		return &commandSession{session: session}, nil
	}
}

func (s *commandSession) Close() error { return s.session.Close() }

func (s *commandSession) Call(ctx context.Context, name string, arguments, target any) error {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := s.session.CallTool(callCtx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return fmt.Errorf("%s transport: %w", name, err)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return fmt.Errorf("%s response: %w", name, err)
	}
	if result.IsError {
		var messages []string
		for _, content := range result.Content {
			if item, ok := content.(*mcp.TextContent); ok {
				messages = append(messages, item.Text)
			}
		}
		return fmt.Errorf("%s: %s structured=%s", name, strings.Join(messages, " "), data)
	}
	if target == nil {
		return nil
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("%s decode envelope %s: %w", name, data, err)
	}
	if err := json.Unmarshal(envelope.Result, target); err != nil {
		return fmt.Errorf("%s decode result %s: %w", name, envelope.Result, err)
	}
	return nil
}

func CanonicalChecks() []CommandCheck {
	return []CommandCheck{
		{ID: "full-suite", Executable: "go", Args: []string{"test", "./..."}, Timeout: "10m"},
		{ID: "race-suite", Executable: "go", Args: []string{"test", "-race", "./..."}, Timeout: "15m"},
		{ID: "vet", Executable: "go", Args: []string{"vet", "./..."}, Timeout: "10m"},
		{ID: "changed-lint", Executable: "golangci-lint", Args: []string{"run", "--new-from-rev=HEAD", "./..."}, Timeout: "10m"},
	}
}

func (d *Driver) Run(ctx context.Context, options RunOptions) (Result, error) {
	if err := validateRunOptions(options); err != nil {
		return Result{}, err
	}
	session, err := d.open(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if session != nil {
			_ = session.Close()
		}
	}()
	if err := d.recoverWorkspace(ctx, session, options.Root, "release a dead owner before starting the canonical harness"); err != nil {
		return Result{}, err
	}
	document, err := d.fetchTemplate(ctx, session, options)
	if err != nil {
		return Result{}, err
	}
	if _, err := ensureFreshPatch(ctx, session, options.Root, document); err != nil {
		return Result{}, err
	}
	grant := ticketGrant(options.ProjectSlug)
	var started patchStart
	if err := session.Call(ctx, "patch_start", map[string]any{
		"patch": options.Root, "options": patchOptions(),
		"writable_roots": []string{options.Root}, "capability_grants": []any{grant},
	}, &started); err != nil {
		return Result{}, err
	}
	payload := map[string]any{"project_slug": options.ProjectSlug, "ticket_id": options.TicketID}
	if options.PhaseID != "" {
		payload["phase_id"] = options.PhaseID
	}
	if err := session.Call(ctx, "patch_send", map[string]any{
		"patch": options.Root, "run_id": started.RunID,
		"node_id": "ticket_intake", "port_id": "start", "kind": "message", "payload": payload,
	}, nil); err != nil {
		return Result{}, err
	}
	d.logf("run %s started\n", started.RunID)
	cursor := uint64(0)
	if options.ProveRecovery {
		cursor, err = d.waitForEvent(ctx, session, options.Root, started.RunID, cursor, "workspace.acquired", "codex_work")
		if err != nil {
			return Result{}, err
		}
		session, err = d.restart(ctx, session, ContinueOptions{Root: options.Root, ProjectSlug: options.ProjectSlug, RunID: started.RunID}, "controlled restart during harness work")
		if err != nil {
			return Result{}, err
		}
		cursor, err = d.waitForEvent(ctx, session, options.Root, started.RunID, cursor, patchrun.EventCheckStarted, "deterministic_checks")
		if err != nil {
			return Result{}, err
		}
		session, err = d.restart(ctx, session, ContinueOptions{Root: options.Root, ProjectSlug: options.ProjectSlug, RunID: started.RunID}, "controlled restart during deterministic checks")
		if err != nil {
			return Result{}, err
		}
	}
	return d.waitForOutcome(ctx, session, ContinueOptions{
		Root: options.Root, ProjectSlug: options.ProjectSlug, RunID: started.RunID, EvidenceBaseDir: options.EvidenceBaseDir,
	}, cursor)
}

func (d *Driver) Resume(ctx context.Context, options ContinueOptions) (Result, error) {
	if err := validateContinueOptions(options); err != nil {
		return Result{}, err
	}
	session, err := d.open(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = session.Close() }()
	if err := d.recoverWorkspace(ctx, session, options.Root, "release a dead owner before resuming the canonical harness"); err != nil {
		return Result{}, err
	}
	if err := recoverPatch(ctx, session, options); err != nil {
		return Result{}, err
	}
	return d.waitForOutcome(ctx, session, options, 0)
}

func (d *Driver) Decide(ctx context.Context, options DecisionOptions) (Result, error) {
	if err := validateContinueOptions(options.ContinueOptions); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(options.RequestID) == "" || strings.TrimSpace(options.Reason) == "" {
		return Result{}, fmt.Errorf("request id and decision reason are required")
	}
	session, err := d.open(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = session.Close() }()
	if err := d.recoverWorkspace(ctx, session, options.Root, "release a dead owner before deciding the canonical harness gate"); err != nil {
		return Result{}, err
	}
	if err := recoverPatch(ctx, session, options.ContinueOptions); err != nil {
		return Result{}, err
	}
	request, err := d.waitForDecisionGate(ctx, session, options.ContinueOptions, options.RequestID)
	if err != nil {
		return Result{}, err
	}
	if err := session.Call(ctx, "patch_gate_decide", map[string]any{
		"patch": options.Root, "run_id": options.RunID, "request_id": request.RequestID,
		"approved": options.Approved, "reason": strings.TrimSpace(options.Reason),
	}, nil); err != nil {
		return Result{}, err
	}
	return d.waitForOutcome(ctx, session, options.ContinueOptions, 0)
}

func (d *Driver) waitForDecisionGate(ctx context.Context, session ToolSession, options ContinueOptions, requestedID string) (GateRequest, error) {
	var receipt *GateRequest
	for {
		requests, err := listGates(ctx, session, options.Root, options.RunID)
		if err != nil {
			return GateRequest{}, err
		}
		for _, request := range requests {
			if request.RequestID == requestedID {
				return request, nil
			}
		}
		if len(requests) > 0 {
			if receipt == nil {
				loaded, err := readGateEvidence(options)
				if err != nil {
					return GateRequest{}, fmt.Errorf("gate request %q is not pending and its receipt cannot be read: %w", requestedID, err)
				}
				if loaded.RequestID != requestedID {
					return GateRequest{}, fmt.Errorf("gate request %q does not match saved receipt %q", requestedID, loaded.RequestID)
				}
				receipt = &loaded
			}
			matches := make([]GateRequest, 0, 1)
			for _, request := range requests {
				if sameGate(*receipt, request) {
					matches = append(matches, request)
				}
			}
			switch len(matches) {
			case 1:
				d.logf("gate request %s recovered as %s\n", requestedID, matches[0].RequestID)
				return matches[0], nil
			case 0:
				return GateRequest{}, fmt.Errorf("gate request %q is not pending and no current gate matches its receipt", requestedID)
			default:
				return GateRequest{}, fmt.Errorf("gate request %q matches %d current gates; refusing an ambiguous decision", requestedID, len(matches))
			}
		}
		var state patchState
		if err := session.Call(ctx, "patch_state", map[string]any{"patch": options.Root, "run_id": options.RunID}, &state); err != nil {
			return GateRequest{}, err
		}
		if terminal(state.Status) {
			return GateRequest{}, fmt.Errorf("gate request %q is not pending; patch finished %s", requestedID, state.Status)
		}
		if err := d.pause(ctx); err != nil {
			return GateRequest{}, err
		}
	}
}

func readGateEvidence(options ContinueOptions) (GateRequest, error) {
	base := options.EvidenceBaseDir
	if base == "" {
		base = filepath.Join(options.Root, ".smith", "harness")
	}
	data, err := os.ReadFile(filepath.Join(base, options.RunID, "gate.json"))
	if err != nil {
		return GateRequest{}, err
	}
	var gate GateRequest
	if err := json.Unmarshal(data, &gate); err != nil {
		return GateRequest{}, err
	}
	return gate, nil
}

func sameGate(left, right GateRequest) bool {
	if left.NodeID != right.NodeID || left.Prompt != right.Prompt {
		return false
	}
	return bytes.Equal(compactJSON(left.Payload), compactJSON(right.Payload))
}

func compactJSON(value json.RawMessage) []byte {
	var compact bytes.Buffer
	if json.Compact(&compact, value) != nil {
		return bytes.TrimSpace(value)
	}
	return compact.Bytes()
}

func (d *Driver) fetchTemplate(ctx context.Context, session ToolSession, options RunOptions) (patch.Document, error) {
	var response struct {
		Name     string         `json:"name"`
		Document patch.Document `json:"document"`
	}
	if err := session.Call(ctx, "patch_template_get", map[string]any{
		"name": TicketCompletion, "memory_context": options.MemoryContext, "memories": options.Memories,
		"research_route": options.ResearchRoute, "review_route": options.ReviewRoute, "checks": CanonicalChecks(),
	}, &response); err != nil {
		return patch.Document{}, err
	}
	if response.Name != TicketCompletion {
		return patch.Document{}, fmt.Errorf("template response named %q, want %q", response.Name, TicketCompletion)
	}
	for index := range response.Document.Nodes {
		node := &response.Document.Nodes[index]
		if node.Runtime == nil {
			continue
		}
		switch node.Runtime.Runtime {
		case "claude":
			node.Runtime.Model = options.ClaudeModel
		case "codex":
			node.Runtime.Model = options.CodexModel
		}
		if node.ID == "fable_review" {
			node.Runtime.Model = options.ReviewModel
		}
	}
	return response.Document, nil
}

func ensureFreshPatch(ctx context.Context, session ToolSession, root string, desired patch.Document) (string, error) {
	var existing patch.Description
	inspectErr := session.Call(ctx, "patch_inspect", map[string]any{"patch": root}, &existing)
	if inspectErr == nil {
		if !sameDocument(desired, existing) {
			return "", fmt.Errorf("existing patch revision %s does not match freshly parameterized template %s", existing.Revision, documentDigest(desired))
		}
		return existing.Revision, nil
	}
	var created patch.Description
	if err := session.Call(ctx, "patch_create", map[string]any{"patch": root, "document": desired}, &created); err != nil {
		return "", fmt.Errorf("fresh template could not replace or create the current patch (inspect: %v): %w", inspectErr, err)
	}
	if !sameDocument(desired, created) {
		return "", fmt.Errorf("created patch revision %s differs from freshly parameterized template %s", created.Revision, documentDigest(desired))
	}
	return created.Revision, nil
}

func sameDocument(desired patch.Document, actual patch.Description) bool {
	revision, err := patch.DocumentRevision(desired)
	return err == nil && revision == actual.Revision
}

func documentDigest(document patch.Document) string {
	revision, err := patch.DocumentRevision(document)
	if err == nil {
		return revision
	}
	data, _ := json.Marshal(document)
	digest := sha256.Sum256(data)
	return "invalid-sha256:" + hex.EncodeToString(digest[:])
}

func (d *Driver) restart(ctx context.Context, current ToolSession, options ContinueOptions, reason string) (ToolSession, error) {
	if err := current.Close(); err != nil {
		return nil, fmt.Errorf("close smith controller: %w", err)
	}
	next, err := d.open(ctx)
	if err != nil {
		return nil, err
	}
	if err := d.recoverWorkspace(ctx, next, options.Root, reason); err != nil {
		_ = next.Close()
		return nil, err
	}
	if err := recoverPatch(ctx, next, options); err != nil {
		_ = next.Close()
		return nil, err
	}
	d.logf("run %s recovered after controller restart\n", options.RunID)
	return next, nil
}

func recoverPatch(ctx context.Context, session ToolSession, options ContinueOptions) error {
	var recovered patchStart
	return session.Call(ctx, "patch_recover", map[string]any{
		"patch": options.Root, "run_id": options.RunID,
		"writable_roots": []string{options.Root}, "capability_grants": []any{ticketGrant(options.ProjectSlug)},
	}, &recovered)
}

func (d *Driver) recoverWorkspace(ctx context.Context, session ToolSession, root, reason string) error {
	var inspection workspaceInspection
	if err := session.Call(ctx, "workspace_inspect", map[string]any{"workspace": root}, &inspection); err != nil {
		return err
	}
	if !inspection.RecoveryNeeded {
		return nil
	}
	if inspection.Owner == nil || inspection.Owner.ID == "" {
		return fmt.Errorf("workspace needs recovery without an owner id")
	}
	return session.Call(ctx, "workspace_recover", map[string]any{
		"workspace": root, "expected_owner_id": inspection.Owner.ID,
		"action": "release", "reason": reason,
	}, nil)
}

func (d *Driver) waitForEvent(ctx context.Context, session ToolSession, root, runID string, cursor uint64, eventType, nodeID string) (uint64, error) {
	for {
		page, err := readEvents(ctx, session, root, runID, cursor)
		if err != nil {
			return cursor, err
		}
		cursor = page.NextCursor
		for _, event := range page.Events {
			if event.Type == eventType && event.NodeID == nodeID {
				return cursor, nil
			}
			if event.Type == patchrun.EventInvocationFailed || event.Type == patchrun.EventPatchFailed {
				return cursor, eventFailure(event)
			}
		}
		if err := d.pause(ctx); err != nil {
			return cursor, err
		}
	}
}

func (d *Driver) waitForOutcome(ctx context.Context, session ToolSession, options ContinueOptions, cursor uint64) (Result, error) {
	proofInvocation := ""
	for {
		requests, err := listGates(ctx, session, options.Root, options.RunID)
		if err != nil {
			return Result{}, err
		}
		if len(requests) > 0 {
			path, err := writeGateEvidence(options, requests[0])
			if err != nil {
				return Result{}, err
			}
			return Result{RunID: options.RunID, Status: "waiting_for_human", LastSequence: cursor, Gate: &requests[0], EvidencePath: path}, nil
		}
		page, err := readEvents(ctx, session, options.Root, options.RunID, cursor)
		if err != nil {
			return Result{}, err
		}
		cursor = page.NextCursor
		for _, event := range page.Events {
			switch {
			case event.Type == patchrun.EventNodeObserved && event.NodeID == completionProofNode && event.Reason == "required capability calls observed":
				proofInvocation = event.InvocationID
			case event.Type == patchrun.EventInvocationCompleted && proofInvocation != "" && event.InvocationID == proofInvocation:
				var state patchState
				if err := session.Call(ctx, "patch_state", map[string]any{"patch": options.Root, "run_id": options.RunID}, &state); err != nil {
					return Result{}, err
				}
				if !terminal(state.Status) {
					if err := session.Call(ctx, "patch_control", map[string]any{"patch": options.Root, "run_id": options.RunID, "action": "drain"}, &state); err != nil {
						return Result{}, err
					}
				}
			case event.Type == patchrun.EventInvocationFailed || event.Type == patchrun.EventPatchFailed:
				return Result{}, eventFailure(event)
			}
		}
		var state patchState
		if err := session.Call(ctx, "patch_state", map[string]any{"patch": options.Root, "run_id": options.RunID}, &state); err != nil {
			return Result{}, err
		}
		if terminal(state.Status) {
			if state.Status != string(patchrun.StatusCompleted) {
				return Result{}, fmt.Errorf("patch finished %s", state.Status)
			}
			receipt, err := readCompletionReceipt(ctx, session, options, state.LastSequence)
			if err != nil {
				return Result{}, err
			}
			path, err := writeEvidence(options, "receipt.json", receipt)
			if err != nil {
				return Result{}, err
			}
			return Result{RunID: options.RunID, Status: state.Status, LastSequence: state.LastSequence, Receipt: receipt, EvidencePath: path}, nil
		}
		if err := d.pause(ctx); err != nil {
			return Result{}, err
		}
	}
}

func writeGateEvidence(options ContinueOptions, gate GateRequest) (string, error) {
	return writeEvidence(options, "gate.json", gate)
}

func writeEvidence(options ContinueOptions, filename string, value any) (string, error) {
	base := options.EvidenceBaseDir
	if base == "" {
		base = filepath.Join(options.Root, ".smith", "harness")
	}
	dir := filepath.Join(base, options.RunID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create harness evidence directory: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(dir, ".evidence-*.json")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	path := filepath.Join(dir, filename)
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", err
	}
	return path, nil
}

type patchStart struct {
	RunID string     `json:"run_id"`
	State patchState `json:"state"`
}

type patchEvent = patchrun.Event

type eventPage struct {
	Events     []patchEvent `json:"events"`
	NextCursor uint64       `json:"next_cursor"`
}

type patchState struct {
	Status       string `json:"status"`
	LastSequence uint64 `json:"last_sequence"`
}

type workspaceInspection struct {
	RecoveryNeeded bool `json:"recovery_needed"`
	Owner          *struct {
		ID string `json:"id"`
	} `json:"owner"`
}

func readEvents(ctx context.Context, session ToolSession, root, runID string, after uint64) (eventPage, error) {
	var page eventPage
	err := session.Call(ctx, "patch_events", map[string]any{"patch": root, "run_id": runID, "after": after, "limit": 1000}, &page)
	return page, err
}

func listGates(ctx context.Context, session ToolSession, root, runID string) ([]GateRequest, error) {
	var result struct {
		Requests []GateRequest `json:"requests"`
	}
	err := session.Call(ctx, "patch_gate_list", map[string]any{"patch": root, "run_id": runID}, &result)
	return result.Requests, err
}

func ticketGrant(project string) map[string]any {
	return map[string]any{"package": "tickets_please", "access": "mutate", "scope": map[string]string{"project": project}}
}

func patchOptions() map[string]any {
	return map[string]any{
		"max_parallel": 1, "max_hops": 32,
		"default_queue": map[string]any{"capacity": 32, "overflow": "reject"},
	}
}

func validateRunOptions(options RunOptions) error {
	if err := validateContinueOptions(ContinueOptions{Root: options.Root, ProjectSlug: options.ProjectSlug, RunID: "pending"}); err != nil {
		return err
	}
	if strings.TrimSpace(options.TicketID) == "" || strings.TrimSpace(options.ClaudeModel) == "" || strings.TrimSpace(options.CodexModel) == "" || strings.TrimSpace(options.ReviewModel) == "" {
		return fmt.Errorf("ticket, claude model, codex model, and review model are required")
	}
	return nil
}

func validateContinueOptions(options ContinueOptions) error {
	if strings.TrimSpace(options.Root) == "" || strings.TrimSpace(options.ProjectSlug) == "" || strings.TrimSpace(options.RunID) == "" {
		return fmt.Errorf("patch root, project slug, and run id are required")
	}
	if !filepath.IsAbs(options.Root) {
		return fmt.Errorf("patch root must be absolute")
	}
	return nil
}

func (d *Driver) open(ctx context.Context) (ToolSession, error) {
	if d.Connect == nil {
		return nil, fmt.Errorf("harness connector is required")
	}
	return d.Connect(ctx)
}

func (d *Driver) pause(ctx context.Context) error {
	delay := d.PollInterval
	if delay <= 0 {
		delay = 250 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (d *Driver) logf(format string, arguments ...any) {
	if d.Log != nil {
		_, _ = fmt.Fprintf(d.Log, format, arguments...)
	}
}

func eventFailure(event patchEvent) error {
	if event.Error != "" {
		return fmt.Errorf("%s %s failed: %s", event.Type, event.InvocationID, event.Error)
	}
	return fmt.Errorf("%s %s failed", event.Type, event.InvocationID)
}

func terminal(status string) bool {
	return status == string(patchrun.StatusCompleted) || status == string(patchrun.StatusStopped) || status == string(patchrun.StatusFailed)
}
