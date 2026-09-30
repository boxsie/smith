package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/capability"
	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
)

type fakeDriverControl struct {
	connects        int
	closes          int
	recovery        bool
	recoverCalls    int
	workspaceFixes  int
	calls           []string
	document        *patch.Description
	createdDocument patch.Document
	checks          []CommandCheck
	events          []patchEvent
	gate            *GateRequest
	extraGates      []GateRequest
	gateListMisses  int
	reissueGate     bool
	decidedRequest  string
	status          string
	runID           string
}

type fakeDriverSession struct {
	control    *fakeDriverControl
	generation int
	closed     bool
}

func (f *fakeDriverControl) connector(context.Context) (ToolSession, error) {
	f.connects++
	return &fakeDriverSession{control: f, generation: f.connects}, nil
}

func (s *fakeDriverSession) Close() error {
	if !s.closed {
		s.closed = true
		s.control.closes++
	}
	return nil
}

func (s *fakeDriverSession) Call(_ context.Context, name string, arguments, target any) error {
	f := s.control
	f.calls = append(f.calls, name)
	values, _ := arguments.(map[string]any)
	switch name {
	case "workspace_inspect":
		inspection := workspaceInspection{}
		if s.generation > 1 {
			inspection.RecoveryNeeded = true
			inspection.Owner = &struct {
				ID string `json:"id"`
			}{ID: "dead-owner"}
		}
		return assignDriverTarget(target, inspection)
	case "workspace_recover":
		f.workspaceFixes++
		return nil
	case "patch_template_get":
		if raw, ok := values["checks"]; ok {
			if err := remarshalDriverValue(raw, &f.checks); err != nil {
				return err
			}
		}
		document, err := Load(TicketCompletion, Options{Checks: CanonicalChecks()})
		if err != nil {
			return err
		}
		return assignDriverTarget(target, map[string]any{"name": TicketCompletion, "document": document})
	case "patch_inspect":
		if f.document == nil {
			return errors.New("read patch: not found")
		}
		return assignDriverTarget(target, f.document)
	case "patch_create":
		document, ok := values["document"].(patch.Document)
		if !ok {
			return fmtDriverType("patch document", values["document"])
		}
		f.createdDocument = document
		root, _ := values["patch"].(string)
		created, err := patch.Create(root, document)
		if err != nil {
			return err
		}
		f.document = created
		return assignDriverTarget(target, f.document)
	case "patch_start":
		f.status = string(patchrun.StatusRunning)
		return assignDriverTarget(target, patchStart{RunID: f.runID, State: patchState{Status: f.status}})
	case "patch_send":
		if f.recovery {
			f.appendEvent(patchEvent{Type: patchrun.EventWorkspaceAcquired, NodeID: "codex_work", InvocationID: f.runID + "/work"})
		} else {
			f.setGate()
		}
		return nil
	case "patch_recover":
		f.recoverCalls++
		if f.reissueGate && f.gate != nil {
			recovered := *f.gate
			recovered.RequestID += "-recovered"
			f.gate = &recovered
			f.gateListMisses = 1
		}
		if f.recovery && f.recoverCalls == 1 {
			f.appendEvent(patchEvent{Type: patchrun.EventCheckStarted, NodeID: "deterministic_checks", InvocationID: f.runID + "/checks"})
		}
		if f.recovery && f.recoverCalls == 2 {
			f.setGate()
		}
		return assignDriverTarget(target, patchStart{RunID: f.runID, State: patchState{Status: f.status}})
	case "patch_events":
		after, _ := values["after"].(uint64)
		page := eventPage{NextCursor: after}
		for _, event := range f.events {
			if event.Sequence > after {
				page.Events = append(page.Events, event)
				page.NextCursor = event.Sequence
			}
		}
		return assignDriverTarget(target, page)
	case "patch_gate_list":
		requests := []GateRequest(nil)
		if f.gateListMisses > 0 {
			f.gateListMisses--
		} else if f.gate != nil {
			requests = append(requests, *f.gate)
			requests = append(requests, f.extraGates...)
		}
		return assignDriverTarget(target, map[string]any{"requests": requests})
	case "patch_gate_decide":
		if f.gate == nil || values["request_id"] != f.gate.RequestID {
			return errors.New("gate is not pending")
		}
		f.decidedRequest, _ = values["request_id"].(string)
		f.gate = nil
		proofID := f.runID + "/proof"
		decision, _ := json.Marshal(map[string]any{
			"inputs": map[string]any{"baton": map[string]any{"ticket_id": "ticket-42", "review_acceptance_checks": []string{"go test ./..."}, "checks_passed": true, "check_run": map[string]any{"passed": true}}},
			"output": map[string]any{"accepted": true, "review_feedback": "accepted"},
		})
		f.appendEvent(patchEvent{Type: patchrun.EventNodeObserved, NodeID: "fable_review", InvocationID: f.runID + "/review", Reason: "runtime decision recorded", Data: decision})
		reviewSequence := f.lastSequence()
		completed := capability.Event{Type: "capability.completed", Package: "tickets_please", Tool: "complete_ticket", TicketID: "ticket-42"}
		completedData, _ := json.Marshal(completed)
		f.appendEvent(patchEvent{Type: patchrun.EventCapabilityCompleted, NodeID: "ticket_close", InvocationID: f.runID + "/close", Data: completedData})
		proofData, _ := json.Marshal(map[string]any{"source_node": "ticket_close", "lineage_invocation_ids": []string{f.runID + "/close"}, "decision_sequence": reviewSequence, "observations": []any{struct {
			capability.Event
			Sequence uint64 `json:"sequence"`
		}{completed, f.lastSequence()}}})
		f.appendEvent(patchEvent{Type: patchrun.EventNodeObserved, NodeID: completionProofNode, InvocationID: proofID, Reason: "required capability calls observed", Data: proofData})
		f.appendEvent(patchEvent{Type: patchrun.EventInvocationCompleted, InvocationID: proofID})
		return nil
	case "patch_state":
		return assignDriverTarget(target, patchState{Status: f.status, LastSequence: f.lastSequence()})
	case "patch_control":
		if values["action"] != "drain" {
			return errors.New("unexpected patch control")
		}
		f.appendEvent(patchEvent{Type: patchrun.EventPatchDrainStarted})
		f.appendEvent(patchEvent{Type: patchrun.EventPatchCompleted})
		f.status = string(patchrun.StatusCompleted)
		return assignDriverTarget(target, patchState{Status: f.status, LastSequence: f.lastSequence()})
	default:
		return errors.New("unexpected tool " + name)
	}
}

func TestDriverDecideWaitsForAndRebindsRecoveredGate(t *testing.T) {
	control := &fakeDriverControl{runID: "run-rebound", reissueGate: true}
	evidence := t.TempDir()
	driver := Driver{Connect: control.connector, PollInterval: time.Microsecond}
	root := t.TempDir()
	result, err := driver.Run(context.Background(), RunOptions{
		Root: root, ProjectSlug: "smith", PhaseID: "issues", TicketID: "smith/123",
		ClaudeModel: "claude-fable-5-1", CodexModel: "gpt-5.6-sol", ReviewModel: "claude-opus-5",
		ResearchRoute: "direct", ReviewRoute: "fable", EvidenceBaseDir: evidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Gate == nil {
		t.Fatal("run did not return a gate")
	}
	originalID := result.Gate.RequestID
	completed, err := driver.Decide(context.Background(), DecisionOptions{
		ContinueOptions: ContinueOptions{Root: root, ProjectSlug: "smith", RunID: control.runID, EvidenceBaseDir: evidence},
		RequestID:       originalID, Approved: false, Reason: "redirect the implementation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != string(patchrun.StatusCompleted) {
		t.Fatalf("decision result = %#v", completed)
	}
	if control.decidedRequest != originalID+"-recovered" {
		t.Fatalf("decided request = %q", control.decidedRequest)
	}
}

func TestDriverDecideRejectsAmbiguousRecoveredGate(t *testing.T) {
	control := &fakeDriverControl{runID: "run-ambiguous", reissueGate: true}
	evidence := t.TempDir()
	driver := Driver{Connect: control.connector, PollInterval: time.Microsecond}
	root := t.TempDir()
	result, err := driver.Run(context.Background(), RunOptions{
		Root: root, ProjectSlug: "smith", PhaseID: "issues", TicketID: "smith/123",
		ClaudeModel: "claude-fable-5-1", CodexModel: "gpt-5.6-sol", ReviewModel: "claude-opus-5",
		ResearchRoute: "direct", ReviewRoute: "fable", EvidenceBaseDir: evidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	duplicate := *result.Gate
	duplicate.RequestID = "another-pending-gate"
	control.extraGates = []GateRequest{duplicate}
	_, err = driver.Decide(context.Background(), DecisionOptions{
		ContinueOptions: ContinueOptions{Root: root, ProjectSlug: "smith", RunID: control.runID, EvidenceBaseDir: evidence},
		RequestID:       result.Gate.RequestID, Approved: true, Reason: "must not cross the wrong gate",
	})
	if err == nil || !strings.Contains(err.Error(), "refusing an ambiguous decision") {
		t.Fatalf("ambiguous decision error = %v", err)
	}
	if control.decidedRequest != "" {
		t.Fatalf("unexpected decision against %q", control.decidedRequest)
	}
}

func (f *fakeDriverControl) appendEvent(event patchEvent) {
	event.Sequence = f.lastSequence() + 1
	f.events = append(f.events, event)
}

func (f *fakeDriverControl) lastSequence() uint64 {
	if len(f.events) == 0 {
		return 0
	}
	return f.events[len(f.events)-1].Sequence
}

func (f *fakeDriverControl) setGate() {
	if f.gate != nil {
		return
	}
	f.gate = &GateRequest{RequestID: f.runID + "/gate", NodeID: "human_gate", Prompt: "approve", Payload: json.RawMessage(`{"accepted":true}`)}
	f.appendEvent(patchEvent{Type: patchrun.EventGateRequested, NodeID: "human_gate", InvocationID: f.gate.RequestID})
}

func TestDriverProvesRecoveryDecidesGateAndDrains(t *testing.T) {
	control := &fakeDriverControl{recovery: true, runID: "run-1"}
	evidence := t.TempDir()
	driver := Driver{Connect: control.connector, PollInterval: time.Microsecond}
	root := t.TempDir()
	result, err := driver.Run(context.Background(), RunOptions{
		Root: root, ProjectSlug: "smith", PhaseID: "issues", TicketID: "smith/123",
		ClaudeModel: "claude-fable-5-1", CodexModel: "gpt-5.6-sol", ReviewModel: "claude-opus-5",
		ResearchRoute: "direct", ReviewRoute: "fable", ProveRecovery: true, EvidenceBaseDir: evidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "waiting_for_human" || result.Gate == nil || result.Gate.RequestID != "run-1/gate" {
		t.Fatalf("run result = %#v", result)
	}
	if control.connects != 3 || control.recoverCalls != 2 || control.workspaceFixes != 2 {
		t.Fatalf("recovery = connects %d patch %d workspace %d", control.connects, control.recoverCalls, control.workspaceFixes)
	}
	if _, err := os.Stat(result.EvidencePath); err != nil {
		t.Fatalf("gate evidence: %v", err)
	}
	assertCanonicalDriverDocument(t, control)

	completed, err := driver.Decide(context.Background(), DecisionOptions{
		ContinueOptions: ContinueOptions{Root: root, ProjectSlug: "smith", RunID: "run-1", EvidenceBaseDir: evidence},
		RequestID:       "run-1/gate", Approved: true, Reason: "reviewed the evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != string(patchrun.StatusCompleted) || completed.LastSequence != control.lastSequence() {
		t.Fatalf("decision result = %#v", completed)
	}
	if !containsDriverCall(control.calls, "patch_gate_decide") || !containsDriverCall(control.calls, "patch_control") {
		t.Fatalf("calls = %v", control.calls)
	}
	drain, done := uint64(0), uint64(0)
	for _, event := range control.events {
		if event.Type == patchrun.EventPatchDrainStarted {
			drain = event.Sequence
		}
		if event.Type == patchrun.EventPatchCompleted {
			done = event.Sequence
		}
	}
	if drain == 0 || done <= drain {
		t.Fatalf("terminal events = drain %d completed %d", drain, done)
	}
}

func TestDriverRejectsStaleExistingPatchBeforeStarting(t *testing.T) {
	control := &fakeDriverControl{runID: "run-stale", document: &patch.Description{Version: patch.FormatVersion, Revision: "sha256:stale"}}
	driver := Driver{Connect: control.connector, PollInterval: time.Microsecond}
	_, err := driver.Run(context.Background(), RunOptions{
		Root: t.TempDir(), ProjectSlug: "smith", TicketID: "smith/123",
		ClaudeModel: "claude-fable-5-1", CodexModel: "gpt-5.6-sol", ReviewModel: "claude-opus-5",
		ResearchRoute: "direct", ReviewRoute: "fable",
	})
	if err == nil || !strings.Contains(err.Error(), "does not match freshly parameterized template") {
		t.Fatalf("stale patch error = %v", err)
	}
	if containsDriverCall(control.calls, "patch_start") || containsDriverCall(control.calls, "patch_create") {
		t.Fatalf("stale patch was mutated or started: %v", control.calls)
	}
}

func TestDriverAcceptsSemanticallyIdenticalCanonicalPatch(t *testing.T) {
	root := t.TempDir()
	source, err := Load(TicketCompletion, Options{Checks: CanonicalChecks()})
	if err != nil {
		t.Fatal(err)
	}
	var document patch.Document
	if err := remarshalDriverValue(source, &document); err != nil {
		t.Fatal(err)
	}
	for index := range document.Nodes {
		node := &document.Nodes[index]
		if node.Runtime == nil {
			continue
		}
		switch node.Runtime.Runtime {
		case "claude":
			node.Runtime.Model = "claude-fable-5-1"
		case "codex":
			node.Runtime.Model = "gpt-5.6-sol"
		}
		if node.ID == "fable_review" {
			node.Runtime.Model = "claude-opus-5"
		}
	}
	created, err := patch.Create(root, document)
	if err != nil {
		t.Fatal(err)
	}
	control := &fakeDriverControl{runID: "run-existing", document: created}
	driver := Driver{Connect: control.connector, PollInterval: time.Microsecond}
	result, err := driver.Run(context.Background(), RunOptions{
		Root: root, ProjectSlug: "smith", TicketID: "smith/123",
		ClaudeModel: "claude-fable-5-1", CodexModel: "gpt-5.6-sol", ReviewModel: "claude-opus-5",
		ResearchRoute: "direct", ReviewRoute: "fable", EvidenceBaseDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "waiting_for_human" || containsDriverCall(control.calls, "patch_create") {
		t.Fatalf("existing canonical result = %#v calls %v", result, control.calls)
	}
}

func assertCanonicalDriverDocument(t *testing.T, control *fakeDriverControl) {
	t.Helper()
	if len(control.checks) != 4 || control.checks[3].ID != "changed-lint" || !strings.Contains(strings.Join(control.checks[3].Args, " "), "--new-from-rev=HEAD") {
		t.Fatalf("configured checks = %#v", control.checks)
	}
	var codex patch.Node
	for _, node := range control.createdDocument.Nodes {
		if node.ID == "codex_work" {
			codex = node
		}
		if node.ID == "fable_review" && (node.Runtime == nil || node.Runtime.Model != "claude-opus-5") {
			t.Fatalf("review model = %#v", node.Runtime)
		}
	}
	if codex.Runtime == nil || codex.Runtime.Model != "gpt-5.6-sol" {
		t.Fatalf("codex model = %#v", codex.Runtime)
	}
	attempts, ok := codex.Config["attempts"].(map[string]any)
	if !ok || attempts["restart"] != "on_failure" || numericDriverValue(attempts["max_attempts"]) != 2 {
		t.Fatalf("codex attempts = %#v", codex.Config["attempts"])
	}
	reasons, ok := attempts["retryable_reasons"].([]any)
	if !ok || len(reasons) != 1 || reasons[0] != "protocol_failure" {
		t.Fatalf("retryable reasons = %#v", attempts["retryable_reasons"])
	}
}

func assignDriverTarget(target, value any) error {
	if target == nil {
		return nil
	}
	return remarshalDriverValue(value, target)
}

func remarshalDriverValue(value, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func fmtDriverType(name string, value any) error {
	return fmt.Errorf("%s has unexpected type %T", name, value)
}

func numericDriverValue(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case float64:
		return int(number)
	default:
		return 0
	}
}

func containsDriverCall(calls []string, wanted string) bool {
	for _, call := range calls {
		if call == wanted {
			return true
		}
	}
	return false
}
