package mcpserver

import (
	"context"
	"testing"

	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/projects"
	"github.com/boxsie/smith/internal/service"
)

func TestMCPGateOperatorJournalStates(t *testing.T) {
	root, session := livePatchSession(t, nil)
	defer session.Close()
	object := map[string]any{"type": "object"}
	callOK(t, session, "patch_create", map[string]any{"patch": root, "document": patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{{
		ID: "approval", Kind: patch.NodeBuiltin, Builtin: &patch.BuiltinReference{Type: "human_gate"}, Config: map[string]any{"prompt": "review fixture"},
		Inlets: []patch.Port{{ID: "in", Kind: patch.EnvelopeMessage, Schema: object}},
	}}}}, nil)
	var started service.PatchStartResult
	callOK(t, session, "patch_start", map[string]any{"patch": root, "options": patchrun.Options{}}, &started)
	defer callOK(t, session, "patch_control", map[string]any{"patch": root, "run_id": started.RunID, "action": "stop"}, nil)
	args := map[string]any{"patch": root, "run_id": started.RunID}
	var snapshot service.GateSnapshot
	callOK(t, session, "patch_gate_list", args, &snapshot)
	if len(snapshot.Requests) != 0 || len(snapshot.Decisions) != 0 {
		t.Fatalf("empty=%+v", snapshot)
	}
	readerService := service.New(service.Dependencies{})
	readerServer, err := New(Dependencies{Service: readerService, Projects: func() (*projects.Index, error) { return &projects.Index{Projects: []string{root}}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	reader := connect(t, readerServer)
	defer reader.Close()
	for index, approved := range []bool{true, false} {
		callOK(t, session, "patch_send", map[string]any{"patch": root, "run_id": started.RunID, "node_id": "approval", "port_id": "in", "kind": "message", "payload": map[string]any{"fixture": true}}, nil)
		gate := waitForGate(t, session, root, started.RunID)
		callOK(t, reader, "patch_gate_list", args, &snapshot)
		if len(snapshot.Requests) != 1 || snapshot.Requests[0].CanDecide || snapshot.Requests[0].RequestedAt.IsZero() {
			t.Fatalf("observer=%+v", snapshot)
		}
		decision := map[string]any{"patch": root, "run_id": started.RunID, "request_id": gate.RequestID, "approved": approved, "reason": " \t"}
		if got := callErrorCode(t, session, "patch_gate_decide", decision); got != "gate_reason_required" {
			t.Fatalf("blank reason=%s", got)
		}
		decision["reason"] = "mcp operator reason"
		if got := callErrorCode(t, reader, "patch_gate_decide", decision); got != "gate_unavailable" {
			t.Fatalf("observer decision=%s", got)
		}
		callOK(t, session, "patch_gate_decide", decision, nil)
		if got := callErrorCode(t, session, "patch_gate_decide", decision); got != "gate_stale" {
			t.Fatalf("duplicate=%s", got)
		}
		callOK(t, reader, "patch_gate_list", args, &snapshot)
		want := "rejected"
		if approved {
			want = "approved"
		}
		if len(snapshot.Requests) != 0 || len(snapshot.Decisions) != index+1 || snapshot.Decisions[0].State != want || snapshot.Decisions[0].Reason != "mcp operator reason" {
			t.Fatalf("receipt=%+v", snapshot)
		}
	}
	// Read tools did not adopt the owning controller's execution handle.
	if _, err := readerService.RecoverPatch(context.Background(), started.RunID, service.PatchStartRequest{Root: root}); err == nil {
		t.Fatal("reader stole the live run lease")
	}
}
