package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/projects"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/service"
	"github.com/boxsie/smith/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type proofRuntime struct {
	mu    sync.Mutex
	calls map[string]int
}

func (f *proofRuntime) Invoke(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
	f.mu.Lock()
	f.calls[invocation.Runtime]++
	f.mu.Unlock()
	if err := sink.Emit(ctx, runtime.RuntimeEvent{Type: "proof.progress", Message: invocation.Runtime}); err != nil {
		return err
	}
	result := json.RawMessage(`{"design":"bounded frontier patch"}`)
	if invocation.Profile == runtime.CapabilityWork {
		result = json.RawMessage(`{"written":true,"path":"frontier-proof.txt"}`)
	} else if invocation.Runtime != runtime.ClaudeRuntimeName {
		result = json.RawMessage(`{"accepted":true,"revision_request":""}`)
	}
	return sink.Complete(ctx, &runtime.ExternalResult{JSON: result, Provenance: runtime.Provenance{Adapter: invocation.Runtime, RequestedModel: invocation.Model, BillingBasis: runtime.BillingSubscription}})
}

func (f *proofRuntime) Admit(_ context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
	return runtime.ContainmentAdmission{RequestedProfile: request.Profile, Mechanism: "test_containment", EffectiveLimits: request.Limits, Enforced: true}, nil
}

func (f *proofRuntime) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func TestPatchControlDrainCompletesSettledRun(t *testing.T) {
	fake := &proofRuntime{calls: make(map[string]int)}
	factory := &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{
		runtime.ClaudeRuntimeName: fake,
		runtime.CodexRuntimeName:  fake,
		runtime.GrokRuntimeName:   fake,
	}}
	root, session := livePatchSession(t, factory)
	defer func() { _ = session.Close() }()
	createProofPatch(t, session, root)

	var started service.PatchStartResult
	callOK(t, session, "patch_start", map[string]any{
		"patch": root,
		"options": patchrun.Options{
			MaxParallel: 1,
			MaxHops:     8,
			DefaultQueue: patchrun.QueuePolicy{
				Capacity: 8,
				Overflow: patchrun.OverflowReject,
			},
		},
	}, &started)

	var drained patchrun.State
	callOK(t, session, "patch_control", map[string]any{
		"patch": root, "run_id": started.RunID, "action": "drain",
	}, &drained)
	if drained.Status != patchrun.StatusCompleted {
		t.Fatalf("drained status = %q, want %q", drained.Status, patchrun.StatusCompleted)
	}

	events := readAllPatchEventsMCP(t, session, root, started.RunID, 2)
	drainSequence, completedSequence := uint64(0), uint64(0)
	for _, event := range events {
		switch event.Type {
		case patchrun.EventPatchDrainStarted:
			drainSequence = event.Sequence
		case patchrun.EventPatchCompleted:
			completedSequence = event.Sequence
		}
	}
	if drainSequence == 0 || completedSequence <= drainSequence {
		t.Fatalf("drain lifecycle = drain %d, completed %d", drainSequence, completedSequence)
	}
}

func TestLiveFrontierPatchLifecycleEntirelyThroughMCP(t *testing.T) {
	fake := &proofRuntime{calls: make(map[string]int)}
	factory := &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{
		runtime.ClaudeRuntimeName: fake,
		runtime.CodexRuntimeName:  fake,
		runtime.GrokRuntimeName:   fake,
	}}
	root, session := livePatchSession(t, factory)
	defer func() { _ = session.Close() }()
	createProofPatch(t, session, root)
	var capabilities patchCapabilities
	callOK(t, session, "patch_capabilities", map[string]any{}, &capabilities)
	if len(capabilities.Builtins) != len(service.RegisteredBuiltinSpecs()) || len(capabilities.Profiles) != 3 || len(capabilities.Templates) != 1 {
		t.Fatalf("patch capabilities = %#v", capabilities)
	}
	var template struct {
		Name     string         `json:"name"`
		Document patch.Document `json:"document"`
	}
	callOK(t, session, "patch_template_get", map[string]any{
		"name": "ticket-completion", "memories": []string{"project_smith"},
		"research_route": "grok", "review_route": "fable",
		"checks": []map[string]any{{"id": "test", "executable": "go", "args": []string{"test", "./..."}}},
	}, &template)
	if template.Name != "ticket-completion" || len(template.Document.Nodes) < 10 {
		t.Fatalf("patch template = %#v", template)
	}
	var listed struct {
		Patches []patch.Description `json:"patches"`
	}
	callOK(t, session, "patch_list", map[string]any{}, &listed)
	if len(listed.Patches) != 1 || listed.Patches[0].Root != root {
		t.Fatalf("patch list = %#v", listed.Patches)
	}

	var started service.PatchStartResult
	callOK(t, session, "patch_start", map[string]any{
		"patch":          root,
		"options":        patchrun.Options{MaxParallel: 3, MaxHops: 8, DefaultQueue: patchrun.QueuePolicy{Capacity: 8, Overflow: patchrun.OverflowReject}},
		"writable_roots": []string{root},
	}, &started)
	callOK(t, session, "patch_control", map[string]any{"patch": root, "run_id": started.RunID, "action": "pause"}, nil)
	if code := callErrorCode(t, session, "patch_operate", map[string]any{
		"patch": root, "run_id": started.RunID, "expected_topology_revision": "sha256:stale",
		"actor": "conductor", "source": "mcp", "operations": []patch.Operation{{Type: "move_node", NodeID: "designer", Position: &patch.Position{X: 1, Y: 1}}},
	}); code != "topology_conflict" {
		t.Fatalf("stale patch operation code = %q", code)
	}

	var changed patchrun.TopologyChangeResult
	callOK(t, session, "patch_operate", map[string]any{
		"patch": root, "run_id": started.RunID,
		"expected_topology_revision": started.State.TopologyRevision,
		"actor":                      "conductor", "source": "mcp", "removal": "reject",
		"operations": []patch.Operation{{Type: "configure_node", NodeID: "critic_switch", Config: &patch.NodeConfiguration{
			Kind: patch.NodeBuiltin, Builtin: &patch.BuiltinReference{Type: "switch"}, Values: map[string]any{"route": "grok"},
		}}},
	}, &changed)
	callOK(t, session, "patch_send", map[string]any{"patch": root, "run_id": started.RunID, "node_id": "designer", "port_id": "brief", "kind": "message", "payload": map[string]any{"brief": "shape the design"}}, nil)

	var paused patchrun.State
	callOK(t, session, "patch_state", map[string]any{"patch": root, "run_id": started.RunID}, &paused)
	if paused.Status != patchrun.StatusPaused || paused.Queues["designer.brief"] != 1 {
		t.Fatalf("paused projection = status %s queues %#v", paused.Status, paused.Queues)
	}
	var queueDetail service.PatchDetail
	callOK(t, session, "patch_detail", map[string]any{"patch": root, "run_id": started.RunID, "kind": "queue", "id": "designer.brief"}, &queueDetail)
	if queueDetail.QueueSize == nil || *queueDetail.QueueSize != 1 {
		t.Fatalf("queue detail = %#v", queueDetail)
	}
	callOK(t, session, "patch_control", map[string]any{"patch": root, "run_id": started.RunID, "action": "resume"}, nil)
	firstGate := waitForGate(t, session, root, started.RunID)
	callOK(t, session, "patch_gate_decide", map[string]any{"patch": root, "run_id": started.RunID, "request_id": firstGate.RequestID, "approved": true, "reason": "first design is safe"}, nil)
	waitForNoGate(t, session, root, started.RunID)
	waitForPatchIdle(t, session, root, started.RunID)
	var ownership workspace.Inspection
	callOK(t, session, "workspace_inspect", map[string]any{"workspace": root}, &ownership)
	if ownership.Owner == nil || ownership.Owner.Status != workspace.StatusReleased || ownership.Owner.InvocationID == "" || ownership.LeaseHeld || ownership.RecoveryNeeded {
		t.Fatalf("workspace inspection = %#v", ownership)
	}
	var handoff workspace.Handoff
	callOK(t, session, "workspace_handoff", map[string]any{
		"workspace": root, "expected_owner_id": ownership.Owner.ID, "next_role": "reviewer",
		"tests": []string{"credential-free MCP proof"}, "evidence": []string{"workspace writer completed"}, "doubts": []string{"live subscriptions are separate"},
	}, &handoff)
	if handoff.FromOwnerID != ownership.Owner.ID || handoff.InvocationID != ownership.Owner.InvocationID || handoff.NextRole != "reviewer" {
		t.Fatalf("workspace handoff = %#v", handoff)
	}

	callOK(t, session, "patch_operate", map[string]any{
		"patch": root, "run_id": started.RunID,
		"expected_topology_revision": changed.After.TopologyRevision,
		"actor":                      "conductor", "source": "mcp", "removal": "reject",
		"operations": []patch.Operation{{Type: "configure_node", NodeID: "critic_switch", Config: &patch.NodeConfiguration{
			Kind: patch.NodeBuiltin, Builtin: &patch.BuiltinReference{Type: "switch"}, Values: map[string]any{"route": "codex"},
		}}},
	}, &changed)
	callOK(t, session, "patch_send", map[string]any{"patch": root, "run_id": started.RunID, "node_id": "designer", "port_id": "brief", "kind": "message", "payload": map[string]any{"brief": "challenge it again"}}, nil)
	secondGate := waitForGate(t, session, root, started.RunID)
	callOK(t, session, "patch_gate_decide", map[string]any{"patch": root, "run_id": started.RunID, "request_id": secondGate.RequestID, "approved": false, "reason": "keep the workspace unchanged"}, nil)
	waitForNoGate(t, session, root, started.RunID)
	waitForPatchIdle(t, session, root, started.RunID)

	events := readAllPatchEventsMCP(t, session, root, started.RunID, 7)
	for _, eventType := range []string{patchrun.EventPatchPaused, patchrun.EventPatchResumed, patchrun.EventTopologyCommitted, patchrun.EventRuntimeStarted, patchrun.EventRuntimeCompleted, patchrun.EventWorkspaceAcquired, patchrun.EventWorkspaceReleased, patchrun.EventGateRequested, patchrun.EventGateResolved, patchrun.EventGateRejected} {
		if !patchEventPresent(events, eventType) {
			t.Errorf("missing causal event %s", eventType)
		}
	}
	if fake.count(runtime.ClaudeRuntimeName) != 2 || fake.count(runtime.GrokRuntimeName) != 1 || fake.count(runtime.CodexRuntimeName) != 2 {
		t.Fatalf("runtime route counts = claude:%d grok:%d codex:%d", fake.count(runtime.ClaudeRuntimeName), fake.count(runtime.GrokRuntimeName), fake.count(runtime.CodexRuntimeName))
	}

	var invocationID, artifactID string
	for _, event := range events {
		if invocationID == "" && event.Type == patchrun.EventRuntimeStarted {
			invocationID = event.InvocationID
		}
		if artifactID == "" && event.Envelope != nil && event.Envelope.Payload != nil {
			artifactID = event.Envelope.Payload.SHA256
		}
	}
	var detail service.PatchDetail
	callOK(t, session, "patch_detail", map[string]any{"patch": root, "run_id": started.RunID, "kind": "invocation", "id": invocationID}, &detail)
	callOK(t, session, "patch_detail", map[string]any{"patch": root, "run_id": started.RunID, "kind": "artifact", "id": artifactID}, &detail)
	if len(detail.Payload) == 0 {
		t.Fatal("artifact detail omitted the validated payload")
	}
	callOK(t, session, "patch_operate", map[string]any{
		"patch": root, "run_id": started.RunID,
		"expected_topology_revision": changed.After.TopologyRevision,
		"actor":                      "conductor", "source": "mcp", "removal": "reject",
		"operations": []patch.Operation{{Type: "configure_node", NodeID: "critic_switch", Config: &patch.NodeConfiguration{
			Kind: patch.NodeBuiltin, Builtin: &patch.BuiltinReference{Type: "switch"}, Values: map[string]any{"route": "missing"},
		}}},
	}, &changed)
	callOK(t, session, "patch_send", map[string]any{"patch": root, "run_id": started.RunID, "node_id": "critic_switch", "port_id": "in", "kind": "message", "payload": map[string]any{"design": "bad route"}}, nil)
	waitForPatchIdle(t, session, root, started.RunID)
	for _, event := range readAllPatchEventsMCP(t, session, root, started.RunID, 11) {
		if event.Type == patchrun.EventInvocationFailed {
			callOK(t, session, "patch_detail", map[string]any{"patch": root, "run_id": started.RunID, "kind": "failure", "id": event.InvocationID}, &detail)
			if len(detail.Events) == 0 {
				t.Fatal("failure detail omitted causal events")
			}
			callOK(t, session, "patch_control", map[string]any{"patch": root, "run_id": started.RunID, "action": "stop"}, nil)
			return
		}
	}
	t.Fatal("misconfigured live route did not leave an inspectable failure")
}

func TestPatchCapabilitiesCoversAllBuiltins(t *testing.T) {
	fake := &proofRuntime{calls: make(map[string]int)}
	factory := &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{
		runtime.ClaudeRuntimeName: fake,
	}}
	_, session := livePatchSession(t, factory)
	defer func() { _ = session.Close() }()

	var capabilities patchCapabilities
	callOK(t, session, "patch_capabilities", map[string]any{}, &capabilities)
	want := service.RegisteredBuiltinSpecs()
	if mismatch := builtinCapabilityMismatch(want, capabilities.Builtins); mismatch != "" {
		t.Fatal(mismatch)
	}

	var checkRouter *service.BuiltinSpec
	for index := range capabilities.Builtins {
		if capabilities.Builtins[index].Kind == "check_router" {
			checkRouter = &capabilities.Builtins[index]
			break
		}
	}
	if checkRouter == nil {
		t.Fatal("check_router was not discovered")
	}
	for _, fragment := range []string{"smith.command_checks/1", "repair", "failed", "environmental", patchrun.EventRepairHop, patchrun.EventChecksTerminal} {
		contract := checkRouter.Config + " " + checkRouter.Ports + " " + strings.Join(checkRouter.EmittedEvents, " ")
		if !strings.Contains(contract, fragment) {
			t.Errorf("check_router contract does not name %q: %s", fragment, contract)
		}
	}
}

func TestBuiltinCapabilityMismatchRejectsUndescribedBuiltin(t *testing.T) {
	registered := service.RegisteredBuiltinSpecs()
	registered = append(registered, service.BuiltinSpec{Kind: "test_only_undescribed"})
	if mismatch := builtinCapabilityMismatch(registered, service.RegisteredBuiltinSpecs()); !strings.Contains(mismatch, "not discovered: test_only_undescribed") {
		t.Fatalf("negative control did not detect the undescribed builtin: %q", mismatch)
	}
}

func builtinCapabilityMismatch(registered, discovered []service.BuiltinSpec) string {
	registeredKinds := make(map[string]bool, len(registered))
	discoveredKinds := make(map[string]bool, len(discovered))
	for _, spec := range registered {
		registeredKinds[spec.Kind] = true
	}
	for _, spec := range discovered {
		discoveredKinds[spec.Kind] = true
	}
	var missing, extra []string
	for kind := range registeredKinds {
		if !discoveredKinds[kind] {
			missing = append(missing, kind)
		}
	}
	for kind := range discoveredKinds {
		if !registeredKinds[kind] {
			extra = append(extra, kind)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "registered but not discovered: "+strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		parts = append(parts, "discovered but not registered: "+strings.Join(extra, ", "))
	}
	return strings.Join(parts, "; ")
}

func livePatchSession(t *testing.T, factory *runtime.ExternalFactory) (string, *mcp.ClientSession) {
	t.Helper()
	root := t.TempDir()
	command := exec.Command("git", "-C", root, "init", "--quiet")
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("init Git workspace: %v: %s", err, data)
	}
	smith := service.New(service.Dependencies{ExternalFactory: factory, Workspace: workspace.New(t.TempDir()), TrackProject: func(string) error { return nil }})
	server, err := New(Dependencies{Service: smith, Projects: func() (*projects.Index, error) { return &projects.Index{Projects: []string{root}}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return root, connect(t, server)
}

func createProofPatch(t *testing.T, session *mcp.ClientSession, root string) {
	t.Helper()
	object := map[string]any{"type": "object"}
	design := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"design": map[string]any{"type": "string"}}, "required": []string{"design"}}
	review := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"accepted": map[string]any{"type": "boolean"}, "revision_request": map[string]any{"type": "string"}}, "required": []string{"accepted", "revision_request"}}
	receipt := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"written": map[string]any{"type": "boolean"}, "path": map[string]any{"type": "string"}}, "required": []string{"written", "path"}}
	message := func(id string, schema any) patch.Port {
		return patch.Port{ID: id, Kind: patch.EnvelopeMessage, Schema: schema}
	}
	runtimeNode := func(id, runtimeName, model, prompt string, output any) patch.Node {
		return patch.Node{ID: id, Kind: patch.NodeRuntime, Runtime: &patch.RuntimeReference{Runtime: runtimeName, Model: model, Profile: runtime.CapabilityReason}, Config: map[string]any{"prompt": prompt}, Inlets: []patch.Port{message("in", object)}, Outlets: []patch.Port{message("out", output)}}
	}
	designer := runtimeNode("designer", runtime.ClaudeRuntimeName, "claude-fable-5-1", "shape the supplied brief into one design", design)
	designer.Inlets[0].ID = "brief"
	writer := runtimeNode("workspace_writer", runtime.CodexRuntimeName, "gpt-5.6-sol", "create frontier-proof.txt in the granted workspace containing a one-line proof, then return the requested receipt", receipt)
	writer.Runtime.Profile = runtime.CapabilityWork
	writer.Config["ticket"] = "smith/frontier-proof"
	document := patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{
		designer,
		{ID: "critic_switch", Kind: patch.NodeBuiltin, Builtin: &patch.BuiltinReference{Type: "switch"}, Config: map[string]any{"route": "codex"}, Inlets: []patch.Port{message("in", object)}, Outlets: []patch.Port{message("codex", object), message("grok", object)}},
		runtimeNode("codex_critic", runtime.CodexRuntimeName, "gpt-5.6-sol", "accept the design and return the requested review", review),
		runtimeNode("grok_critic", runtime.GrokRuntimeName, "grok-4.5", "accept the design and return the requested review", review),
		{ID: "review_router", Kind: patch.NodeBuiltin, Builtin: &patch.BuiltinReference{Type: "router"}, Config: map[string]any{"field": "accepted", "equals": true, "true_outlet": "accepted", "false_outlet": "revise"}, Inlets: []patch.Port{message("in", object)}, Outlets: []patch.Port{message("accepted", object), message("revise", object)}},
		{ID: "write_gate", Kind: patch.NodeBuiltin, Builtin: &patch.BuiltinReference{Type: "human_gate"}, Config: map[string]any{"prompt": "allow this result to cross into workspace-writing work", "approved_outlet": "approved", "rejected_outlet": "rejected"}, Inlets: []patch.Port{message("in", object)}, Outlets: []patch.Port{message("approved", object), message("rejected", object)}},
		writer,
	}, Cords: []patch.Cord{
		proofCord("design_to_switch", "designer", "out", "critic_switch", "in"),
		proofCord("switch_to_codex", "critic_switch", "codex", "codex_critic", "in"),
		proofCord("switch_to_grok", "critic_switch", "grok", "grok_critic", "in"),
		proofCord("codex_to_router", "codex_critic", "out", "review_router", "in"),
		proofCord("grok_to_router", "grok_critic", "out", "review_router", "in"),
		proofCord("accepted_to_gate", "review_router", "accepted", "write_gate", "in"),
		proofCord("gate_to_writer", "write_gate", "approved", "workspace_writer", "in"),
		proofCord("revision_feedback", "review_router", "revise", "designer", "brief"),
	}}
	callOK(t, session, "patch_create", map[string]any{"patch": root, "document": document}, nil)
}

func proofCord(id, fromNode, fromPort, toNode, toPort string) patch.Cord {
	return patch.Cord{ID: id, From: patch.Endpoint{Node: fromNode, Port: fromPort}, To: patch.Endpoint{Node: toNode, Port: toPort}, Delivery: patch.DeliveryPolicy{Mode: "enqueue"}}
}

func waitForGate(t *testing.T, session *mcp.ClientSession, root, runID string) service.GateRequest {
	t.Helper()
	deadline := livePatchDeadline(2 * time.Second)
	for time.Now().Before(deadline) {
		var result struct {
			Requests []service.GateRequest `json:"requests"`
		}
		callOK(t, session, "patch_gate_list", map[string]any{"patch": root, "run_id": runID}, &result)
		if len(result.Requests) > 0 {
			return result.Requests[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	var state patchrun.State
	callOK(t, session, "patch_state", map[string]any{"patch": root, "run_id": runID}, &state)
	var page patchrun.EventPage
	callOK(t, session, "patch_events", map[string]any{"patch": root, "run_id": runID, "after": 0, "limit": 1000}, &page)
	t.Logf("state at gate timeout: %#v", state)
	for _, event := range page.Events {
		t.Logf("event %d %s node=%s error=%s", event.Sequence, event.Type, event.NodeID, event.Error)
	}
	t.Fatal("timed out waiting for human gate")
	return service.GateRequest{}
}

func waitForNoGate(t *testing.T, session *mcp.ClientSession, root, runID string) {
	t.Helper()
	deadline := livePatchDeadline(2 * time.Second)
	for time.Now().Before(deadline) {
		var result struct {
			Requests []service.GateRequest `json:"requests"`
		}
		callOK(t, session, "patch_gate_list", map[string]any{"patch": root, "run_id": runID}, &result)
		if len(result.Requests) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("gate decision did not settle")
}

func waitForPatchIdle(t *testing.T, session *mcp.ClientSession, root, runID string) {
	t.Helper()
	deadline := livePatchDeadline(3 * time.Second)
	for time.Now().Before(deadline) {
		var state patchrun.State
		callOK(t, session, "patch_state", map[string]any{"patch": root, "run_id": runID}, &state)
		queued := 0
		for _, size := range state.Queues {
			queued += size
		}
		if len(state.Active) == 0 && queued == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("patch did not become idle")
}

func livePatchDeadline(fast time.Duration) time.Time {
	if os.Getenv("SMITH_LIVE_FRONTIER_PATCH") == "1" {
		return time.Now().Add(15 * time.Minute)
	}
	return time.Now().Add(fast)
}

func patchEventPresent(events []patchrun.Event, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func readAllPatchEventsMCP(t *testing.T, session *mcp.ClientSession, root, runID string, limit int) []patchrun.Event {
	t.Helper()
	var result []patchrun.Event
	var cursor uint64
	for {
		var page patchrun.EventPage
		callOK(t, session, "patch_events", map[string]any{"patch": root, "run_id": runID, "after": cursor, "limit": limit}, &page)
		for _, event := range page.Events {
			if event.Sequence <= cursor {
				t.Fatalf("event cursor regressed from %d to %d", cursor, event.Sequence)
			}
			cursor = event.Sequence
			result = append(result, event)
		}
		if !page.HasMore {
			return result
		}
		if page.NextCursor != cursor {
			t.Fatalf("next cursor = %d, want %d", page.NextCursor, cursor)
		}
	}
}

func TestLiveFrontierPatchSubscriptions(t *testing.T) {
	if os.Getenv("SMITH_LIVE_FRONTIER_PATCH") != "1" {
		t.Skip("set SMITH_LIVE_FRONTIER_PATCH=1 to spend Fable, Codex, and Grok subscription calls")
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CODEX_API_KEY", "XAI_API_KEY", "GROK_DEPLOYMENT_KEY"} {
		t.Setenv(key, "")
	}
	root, session := livePatchSession(t, runtime.DefaultExternalFactory())
	defer func() { _ = session.Close() }()
	createProofPatch(t, session, root)
	if _, err := os.Stat(filepath.Join(root, patch.FileName)); err != nil {
		t.Fatal(err)
	}
	var started service.PatchStartResult
	callOK(t, session, "patch_start", map[string]any{
		"patch":          root,
		"options":        patchrun.Options{MaxParallel: 3, MaxHops: 8, DefaultQueue: patchrun.QueuePolicy{Capacity: 8, Overflow: patchrun.OverflowReject}},
		"writable_roots": []string{root},
	}, &started)
	currentRevision := started.State.TopologyRevision
	for index, route := range []string{"grok", "codex"} {
		callOK(t, session, "patch_control", map[string]any{"patch": root, "run_id": started.RunID, "action": "pause"}, nil)
		var changed patchrun.TopologyChangeResult
		callOK(t, session, "patch_operate", map[string]any{
			"patch": root, "run_id": started.RunID, "expected_topology_revision": currentRevision,
			"actor": "live-proof", "source": "mcp", "removal": "reject",
			"operations": []patch.Operation{{Type: "configure_node", NodeID: "critic_switch", Config: &patch.NodeConfiguration{
				Kind: patch.NodeBuiltin, Builtin: &patch.BuiltinReference{Type: "switch"}, Values: map[string]any{"route": route},
			}}},
		}, &changed)
		currentRevision = changed.After.TopologyRevision
		callOK(t, session, "patch_send", map[string]any{"patch": root, "run_id": started.RunID, "node_id": "designer", "port_id": "brief", "kind": "message", "payload": map[string]any{"brief": "live frontier pass", "pass": index + 1}}, nil)
		callOK(t, session, "patch_control", map[string]any{"patch": root, "run_id": started.RunID, "action": "resume"}, nil)
		gate := waitForGate(t, session, root, started.RunID)
		callOK(t, session, "patch_gate_decide", map[string]any{"patch": root, "run_id": started.RunID, "request_id": gate.RequestID, "approved": index == 0, "reason": "live proof decision"}, nil)
		waitForNoGate(t, session, root, started.RunID)
		waitForPatchIdle(t, session, root, started.RunID)
	}
	if _, err := os.Stat(filepath.Join(root, "frontier-proof.txt")); err != nil {
		t.Fatalf("approved live gate did not produce workspace artifact: %v", err)
	}
	var page patchrun.EventPage
	callOK(t, session, "patch_events", map[string]any{"patch": root, "run_id": started.RunID, "after": 0, "limit": 1000}, &page)
	for _, eventType := range []string{patchrun.EventRuntimeCompleted, patchrun.EventGateResolved, patchrun.EventGateRejected, patchrun.EventTopologyCommitted} {
		if !patchEventPresent(page.Events, eventType) {
			t.Fatalf("live proof missing %s", eventType)
		}
	}
	callOK(t, session, "patch_control", map[string]any{"patch": root, "run_id": started.RunID, "action": "stop"}, nil)
}
