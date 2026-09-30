package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
)

func startOperatorGate(t *testing.T, root string) (*Service, string) {
	t.Helper()
	svc := New(Dependencies{TrackProject: func(string) error { return nil }})
	object := map[string]any{"type": "object"}
	_, err := svc.CreatePatch(root, patch.Document{Version: patch.FormatVersion, Nodes: []patch.Node{{
		ID: "approval", Kind: patch.NodeBuiltin, Builtin: &patch.BuiltinReference{Type: "human_gate"},
		Config: map[string]any{"prompt": "allow the signal"}, Inlets: []patch.Port{{ID: "in", Kind: patch.EnvelopeMessage, Schema: object}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.StartPatch(context.Background(), PatchStartRequest{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	return svc, run.RunID
}

func waitOperatorGate(t *testing.T, svc *Service, root, run string) GateRecord {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := svc.InspectGates(root, run)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Requests) == 1 && snapshot.Requests[0].CanDecide {
			return snapshot.Requests[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("gate never became durably visible and decidable")
	return GateRecord{}
}

func sendOperatorGate(t *testing.T, svc *Service, root, run string) GateRecord {
	t.Helper()
	_, err := svc.SendPatch(context.Background(), root, run, "approval", "in", patch.EnvelopeMessage, json.RawMessage(`{"ticket_id":"fixture","changes":["no workspace edits"],"checks_passed":true,"accepted":true,"review_feedback":"ready"}`))
	if err != nil {
		t.Fatal(err)
	}
	return waitOperatorGate(t, svc, root, run)
}

func TestOperatorGateDecisionsAndReadOnlyRestart(t *testing.T) {
	root := t.TempDir()
	svc, run := startOperatorGate(t, root)
	closed := false
	defer func() {
		if !closed {
			_, _ = svc.ControlPatch(context.Background(), root, run, "stop")
		}
	}()
	if _, err := svc.InspectGates(root, "missing"); err == nil {
		t.Fatal("missing run reported as an empty gate list")
	}
	empty, err := svc.InspectGates(root, run)
	if err != nil || len(empty.Requests) != 0 || len(empty.Decisions) != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
	for _, approved := range []bool{true, false} {
		gate := sendOperatorGate(t, svc, root, run)
		if gate.RequestedAt.IsZero() || gate.Prompt != "allow the signal" || !json.Valid(gate.Payload) {
			t.Fatalf("gate=%+v", gate)
		}
		for _, reason := range []string{"", " \n\t"} {
			if err := svc.DecideGate(root, run, gate.RequestID, approved, reason); !errors.Is(err, ErrGateReason) {
				t.Fatalf("blank reason=%v", err)
			}
		}
		fresh := New(Dependencies{})
		before, _ := os.ReadFile(patchrun.EventsPath(patchrun.RunDir(root, run)))
		view, err := fresh.InspectGates(root, run)
		if err != nil || len(view.Requests) != 1 || view.Requests[0].CanDecide || !bytes.Equal(view.Requests[0].Payload, gate.Payload) {
			t.Fatalf("reader view=%+v err=%v", view, err)
		}
		if err := fresh.DecideGate(root, run, gate.RequestID, approved, "reader has no authority"); !errors.Is(err, ErrGateUnavailable) {
			t.Fatalf("reader decision=%v", err)
		}
		after, _ := os.ReadFile(patchrun.EventsPath(patchrun.RunDir(root, run)))
		if !bytes.Equal(before, after) || len(fresh.patches) != 0 || len(fresh.patchGrants) != 0 {
			t.Fatal("read-only gate discovery changed execution or authority")
		}
		if err := svc.DecideGate(root, run, gate.RequestID, approved, "operator reason"); err != nil {
			t.Fatal(err)
		}
		if err := svc.DecideGate(root, run, gate.RequestID, approved, "duplicate reason"); !errors.Is(err, ErrGateStale) {
			t.Fatalf("duplicate=%v", err)
		}
		view, err = fresh.InspectGates(root, run)
		want := "rejected"
		if approved {
			want = "approved"
		}
		if err != nil || len(view.Requests) != 0 || view.Decisions[0].State != want || view.Decisions[0].Reason != "operator reason" || view.Decisions[0].DecidedAt == nil {
			t.Fatalf("receipt=%+v err=%v", view, err)
		}
	}
	gate := sendOperatorGate(t, svc, root, run)
	svc.patchesMu.Lock()
	scheduler := svc.patches[patchKey(root, run)]
	svc.patchesMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := scheduler.Close(ctx); err != nil {
		t.Fatal(err)
	}
	closed = true
	fresh := New(Dependencies{})
	suspended, err := fresh.InspectGates(root, run)
	if err != nil || len(suspended.Requests) != 1 || suspended.Requests[0].CanDecide {
		t.Fatalf("suspended=%+v err=%v", suspended, err)
	}
	if _, err := fresh.RecoverPatch(context.Background(), run, PatchStartRequest{Root: root}); err != nil {
		t.Fatal(err)
	}
	defer fresh.ControlPatch(context.Background(), root, run, "stop")
	recovered := waitOperatorGate(t, fresh, root, run)
	if recovered.RequestID != gate.RequestID || !recovered.RequestedAt.Equal(gate.RequestedAt) || recovered.InvocationID == gate.InvocationID {
		t.Fatalf("recovered=%+v original=%+v", recovered, gate)
	}
	if _, err := fresh.ControlPatch(context.Background(), root, run, "stop"); err != nil {
		t.Fatal(err)
	}
	view, err := fresh.InspectGates(root, run)
	if err != nil || len(view.Requests) != 0 || view.Decisions[0].State != "cancelled" {
		t.Fatalf("stopped=%+v err=%v", view, err)
	}
}

func TestOperatorGateSurvivesHardControllerDeath(t *testing.T) {
	if root := os.Getenv("SMITH_OPERATOR_GATE_CHILD_ROOT"); root != "" {
		svc, run := startOperatorGate(t, root)
		sendOperatorGate(t, svc, root, run)
		fmt.Println(run)
		select {}
	}
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOperatorGateSurvivesHardControllerDeath$")
	command.Env = append(os.Environ(), "SMITH_OPERATOR_GATE_CHILD_ROOT="+root)
	pipe, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	scanner := bufio.NewScanner(pipe)
	if !scanner.Scan() {
		t.Fatal("child did not report its pending run")
	}
	run := scanner.Text()
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	fresh := New(Dependencies{})
	before, err := os.ReadFile(patchrun.EventsPath(patchrun.RunDir(root, run)))
	if err != nil {
		t.Fatal(err)
	}
	view, err := fresh.InspectGates(root, run)
	if err != nil || len(view.Requests) != 1 || view.Requests[0].CanDecide {
		t.Fatalf("orphan=%+v err=%v", view, err)
	}
	gate := view.Requests[0]
	if err := fresh.DecideGate(root, run, gate.RequestID, true, "no implicit recovery"); !errors.Is(err, ErrGateUnavailable) {
		t.Fatalf("orphan decision=%v", err)
	}
	after, _ := os.ReadFile(patchrun.EventsPath(patchrun.RunDir(root, run)))
	if !bytes.Equal(before, after) || len(fresh.patches) != 0 {
		t.Fatal("orphan discovery mutated the journal")
	}
	if _, err := fresh.RecoverPatch(context.Background(), run, PatchStartRequest{Root: root}); err != nil {
		t.Fatal(err)
	}
	defer fresh.ControlPatch(context.Background(), root, run, "stop")
	recovered := waitOperatorGate(t, fresh, root, run)
	if recovered.RequestID != gate.RequestID || !recovered.RequestedAt.Equal(gate.RequestedAt) {
		t.Fatalf("recovered=%+v original=%+v", recovered, gate)
	}
	if err := fresh.DecideGate(root, run, gate.RequestID, true, "explicitly recovered builtin fixture"); err != nil {
		t.Fatal(err)
	}
}
