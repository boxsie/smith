package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestWorkspaceLeaseSerializesOwnersAndReportsDirtyBaseline(t *testing.T) {
	root := gitFixture(t)
	if err := os.WriteFile(filepath.Join(root, "before.txt"), []byte("already dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t)
	first, err := manager.Acquire(ownerRequest(root, "run-a", "invocation-a"))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Owner.Baseline.Dirty || !reflect.DeepEqual(first.Owner.Baseline.Changed, []string{"before.txt"}) {
		t.Fatalf("dirty baseline = %#v", first.Owner.Baseline)
	}
	if _, err := manager.Acquire(ownerRequest(root, "run-b", "invocation-b")); !errors.Is(err, ErrOwned) {
		t.Fatalf("concurrent acquire error = %v", err)
	}
	inspection, err := manager.Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.LeaseHeld || inspection.RecoveryNeeded || inspection.Owner == nil || inspection.Owner.ID != first.Owner.ID {
		t.Fatalf("live inspection = %#v", inspection)
	}
	if err := first.Release("completed", false); err != nil {
		t.Fatal(err)
	}
	second, err := manager.Acquire(ownerRequest(root, "run-b", "invocation-b"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Owner.ID == first.Owner.ID {
		t.Fatal("sequential owner reused an id")
	}
	if err := second.Release("cancelled", false); err != nil {
		t.Fatal(err)
	}
	inspection, err = manager.Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Owner.ReleaseReason != "cancelled" || inspection.Owner.Status != StatusReleased {
		t.Fatalf("released owner = %#v", inspection.Owner)
	}
}

func TestWorkspaceCrashRequiresExplicitRecovery(t *testing.T) {
	root := gitFixture(t)
	manager := testManager(t)
	crashed, err := manager.Acquire(ownerRequest(root, "run-a", "invocation-a"))
	if err != nil {
		t.Fatal(err)
	}
	if err := crashed.lock.Release(); err != nil {
		t.Fatal(err)
	}
	crashed.released = true

	inspection, err := manager.Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.LeaseHeld || !inspection.RecoveryNeeded || inspection.Owner.Status != StatusActive {
		t.Fatalf("crash inspection = %#v", inspection)
	}
	if _, err := manager.Acquire(ownerRequest(root, "run-b", "invocation-b")); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("post-crash acquire error = %v", err)
	}
	if _, err := manager.Recover(RecoverRequest{Root: root, ExpectedOwnerID: "stale", Action: StatusReleased, Reason: "checked"}); !errors.Is(err, ErrOwnerConflict) {
		t.Fatalf("stale recovery error = %v", err)
	}
	recovered, err := manager.Recover(RecoverRequest{Root: root, ExpectedOwnerID: crashed.Owner.ID, Action: StatusRevoked, Reason: "partial edits rejected after inspection"})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != StatusRevoked || recovered.ReleaseReason == "" {
		t.Fatalf("recovered owner = %#v", recovered)
	}
	next, err := manager.Acquire(ownerRequest(root, "run-b", "invocation-b"))
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Release("completed", false); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceHandoffIsExactSingleUseAndRejectsStaleState(t *testing.T) {
	root := gitFixture(t)
	manager := testManager(t)
	first, err := manager.Acquire(ownerRequest(root, "run-a", "invocation-a"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("proof\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.Release("completed", false); err != nil {
		t.Fatal(err)
	}
	request := HandoffRequest{
		Root: root, ID: "stable-handoff", ExpectedOwnerID: first.Owner.ID, NextRole: "reviewer",
		Tests: []string{"go test ./..."}, Evidence: []string{"result.txt contains proof"}, Doubts: []string{"needs live verification"},
	}
	handoff, err := manager.CreateHandoff(request)
	if err != nil {
		t.Fatal(err)
	}
	if handoff.InvocationID != "invocation-a" || handoff.TopologyRevision != "topology-a" || handoff.Ticket != "smith/ownership" || !reflect.DeepEqual(handoff.Current.Changed, []string{"result.txt"}) {
		t.Fatalf("handoff = %#v", handoff)
	}
	replayed, err := manager.CreateHandoff(request)
	if err != nil || !reflect.DeepEqual(replayed, handoff) {
		t.Fatalf("idempotent handoff = %#v, %v", replayed, err)
	}
	request.NextRole = "different"
	if _, err := manager.CreateHandoff(request); err == nil {
		t.Fatal("same handoff id accepted different content")
	}
	if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("changed after handoff\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nextRequest := ownerRequest(root, "run-b", "invocation-b")
	nextRequest.HandoffID = handoff.ID
	if _, err := manager.Acquire(nextRequest); !errors.Is(err, ErrStaleHandoff) {
		t.Fatalf("stale handoff acquire error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("proof\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := manager.Acquire(nextRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.Handoff, handoff) || next.Owner.HandoffID != handoff.ID {
		t.Fatalf("received handoff = %#v, want %#v", next.Handoff, handoff)
	}
	if err := next.Release("completed", false); err != nil {
		t.Fatal(err)
	}
	thirdRequest := ownerRequest(root, "run-c", "invocation-c")
	thirdRequest.HandoffID = handoff.ID
	if _, err := manager.Acquire(thirdRequest); err == nil {
		t.Fatal("consumed handoff was accepted twice")
	}
	reread, err := manager.ReadHandoff(root, handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reread, handoff) {
		t.Fatalf("immutable handoff changed: %#v", reread)
	}
}

func TestWorkspaceCleanLinkedWorktreeCanBeRemovedButDirtyOneIsPreserved(t *testing.T) {
	repository := gitFixture(t)
	manager := testManager(t)
	clean := filepath.Join(t.TempDir(), "clean-worktree")
	gitCommand(t, repository, "worktree", "add", "--quiet", "--detach", clean, "HEAD")
	lease, err := manager.Acquire(AcquireRequest{Root: clean, Mode: "isolated_worktree", RunID: "run", InvocationID: "clean", Body: "codex:clean", AllowedScope: []string{clean}})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release("completed", true); err != nil {
		t.Fatal(err)
	}
	if !lease.Owner.CleanedUp || lease.Owner.CleanupError != "" {
		t.Fatalf("clean cleanup = %#v", lease.Owner)
	}
	if _, err := os.Stat(clean); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean worktree still exists: %v", err)
	}

	dirty := filepath.Join(t.TempDir(), "dirty-worktree")
	gitCommand(t, repository, "worktree", "add", "--quiet", "--detach", dirty, "HEAD")
	dirtyLease, err := manager.Acquire(AcquireRequest{Root: dirty, Mode: "isolated_worktree", RunID: "run", InvocationID: "dirty", Body: "codex:dirty", AllowedScope: []string{dirty}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirty, "dirty.txt"), []byte("preserve me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dirtyLease.Release("completed", true); err != nil {
		t.Fatal(err)
	}
	if dirtyLease.Owner.CleanedUp || dirtyLease.Owner.CleanupError != "dirty worktree was preserved" {
		t.Fatalf("dirty cleanup = %#v", dirtyLease.Owner)
	}
	if _, err := os.Stat(filepath.Join(dirty, "dirty.txt")); err != nil {
		t.Fatalf("dirty worktree was not preserved: %v", err)
	}
}

func ownerRequest(root, runID, invocationID string) AcquireRequest {
	return AcquireRequest{
		Root: root, Mode: "root", RunID: runID, InvocationID: invocationID,
		TopologyRevision: "topology-a", NodeID: "worker", Body: "codex:frontier:worker",
		Runtime: "codex", Model: "frontier", SessionMode: "fresh",
		Ticket: "smith/ownership", AllowedScope: []string{root},
	}
}

func testManager(t *testing.T) *Manager {
	t.Helper()
	index := 0
	return &Manager{
		Root: t.TempDir(), Clock: func() time.Time { return time.Date(2026, 9, 3, 12, index, 0, 0, time.UTC) },
		NewID: func() string { index++; return "id-" + time.Date(2026, 9, 3, 12, index, 0, 0, time.UTC).Format("1504") },
	}
}

func gitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitCommand(t, root, "init", "--quiet")
	gitCommand(t, root, "config", "user.name", "smith test")
	gitCommand(t, root, "config", "user.email", "smith@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, root, "add", "tracked.txt")
	gitCommand(t, root, "commit", "--quiet", "-m", "baseline")
	return root
}

func gitCommand(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
}
