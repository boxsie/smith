package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/patch"
	"github.com/boxsie/smith/internal/patchrun"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/workspace"
)

type blockingWorkspaceRuntime struct {
	started chan struct{}
	release chan struct{}

	mu          sync.Mutex
	invocations []runtime.Invocation
	active      int
	maxActive   int
}

func (r *blockingWorkspaceRuntime) Invoke(ctx context.Context, invocation runtime.Invocation, sink runtime.InvocationSink) error {
	r.mu.Lock()
	r.invocations = append(r.invocations, invocation)
	r.active++
	if r.active > r.maxActive {
		r.maxActive = r.active
	}
	call := len(r.invocations)
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.active--; r.mu.Unlock() }()
	if call == 1 {
		if err := os.WriteFile(filepath.Join(invocation.Workspace.Root, "first-body.txt"), []byte("owned\n"), 0o600); err != nil {
			return err
		}
		close(r.started)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.release:
		}
	}
	return sink.Complete(ctx, &runtime.ExternalResult{Text: "done", Provenance: runtime.Provenance{Adapter: invocation.Runtime, RequestedModel: invocation.Model, BillingBasis: runtime.BillingSubscription}})
}

func (r *blockingWorkspaceRuntime) Admit(_ context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
	return runtime.ContainmentAdmission{RequestedProfile: request.Profile, Mechanism: "test_containment", EffectiveLimits: request.Limits, Enforced: true}, nil
}

func TestPatchWorkBodiesAreSerializedAndReceiverGetsExactHandoff(t *testing.T) {
	root := serviceGitFixture(t)
	body := &blockingWorkspaceRuntime{started: make(chan struct{}), release: make(chan struct{})}
	manager := workspace.New(t.TempDir())
	svc := New(Dependencies{
		ExternalFactory: &runtime.ExternalFactory{Runtimes: map[string]runtime.ExternalRuntime{runtime.CodexRuntimeName: body}},
		Workspace:       manager,
	})
	svc.setPatchWriteGrants(root, "run", []string{root})
	node := workspaceRuntimeNode(root)
	firstInvocation := patchrun.Invocation{
		RunID: "run", ID: "invocation-a", PatchRoot: root, TopologyRevision: "topology-a", Node: node,
		Report: func(patchrun.NodeEvent) error { return nil },
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := svc.runPatchRuntime(context.Background(), firstInvocation)
		firstDone <- err
	}()
	select {
	case <-body.started:
	case <-time.After(3 * time.Second):
		t.Fatal("first model body did not start")
	}
	secondInvocation := firstInvocation
	secondInvocation.ID = "invocation-b"
	if _, err := svc.runPatchRuntime(context.Background(), secondInvocation); !errors.Is(err, workspace.ErrOwned) {
		t.Fatalf("concurrent body error = %v", err)
	}
	close(body.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	body.mu.Lock()
	if len(body.invocations) != 1 || body.maxActive != 1 {
		t.Fatalf("runtime entries = %d, max concurrent = %d", len(body.invocations), body.maxActive)
	}
	body.mu.Unlock()

	inspection, err := manager.Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Owner == nil || inspection.Owner.Status != workspace.StatusReleased || !inspection.Owner.Current.Dirty {
		t.Fatalf("released ownership = %#v", inspection)
	}
	if inspection.Owner.Runtime != runtime.CodexRuntimeName || inspection.Owner.Model != "frontier" || inspection.Owner.SessionMode != runtime.SessionFresh {
		t.Fatalf("owner runtime/session identity = %#v", inspection.Owner)
	}
	handoff, err := manager.CreateHandoff(workspace.HandoffRequest{
		Root: root, ExpectedOwnerID: inspection.Owner.ID, NextRole: "reviewer",
		Tests: []string{"go test ./..."}, Evidence: []string{"first-body.txt"}, Doubts: []string{"live provider remains"},
	})
	if err != nil {
		t.Fatal(err)
	}
	handoffJSON, err := json.Marshal(handoff)
	if err != nil {
		t.Fatal(err)
	}
	node.Config["handoff_id"] = handoff.ID
	receiver := firstInvocation
	receiver.ID = "invocation-c"
	receiver.Node = node
	if _, err := svc.runPatchRuntime(context.Background(), receiver); err != nil {
		t.Fatal(err)
	}
	body.mu.Lock()
	defer body.mu.Unlock()
	if len(body.invocations) != 2 || body.maxActive != 1 {
		t.Fatalf("runtime entries = %d, max concurrent = %d", len(body.invocations), body.maxActive)
	}
	prompt := body.invocations[1].Messages[0].Text
	if !strings.Contains(prompt, "workspace handoff (authoritative json):\n"+string(handoffJSON)) {
		t.Fatalf("receiver prompt omitted exact handoff:\n%s", prompt)
	}
	if len(body.invocations[1].Context) != 1 || body.invocations[1].Context[0].Source != "workspace-handoff" || body.invocations[1].Context[0].SHA256 == "" {
		t.Fatalf("receiver context provenance = %#v", body.invocations[1].Context)
	}
}

func workspaceRuntimeNode(root string) patch.Node {
	return patch.Node{
		ID: "worker", Kind: patch.NodeRuntime,
		Runtime: &patch.RuntimeReference{Runtime: runtime.CodexRuntimeName, Model: "frontier", Profile: runtime.CapabilityWork},
		Config:  map[string]any{"prompt": "work on the checkout", "workspace": root, "ticket": "smith/ownership"},
		Outlets: []patch.Port{{ID: "out", Kind: patch.EnvelopeMessage, Schema: map[string]any{"type": "string"}}},
	}
}

func serviceGitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.name", "smith test"},
		{"config", "user.email", "smith@example.invalid"},
	} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if data, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, data)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked.txt"}, {"commit", "--quiet", "-m", "baseline"}} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if data, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, data)
		}
	}
	return root
}
