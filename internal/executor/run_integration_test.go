package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/task"
)

// runWithManifest sets up a run directory, redirects paths, writes the initial manifest,
// executes, and returns the final manifest.
func runWithManifest(t *testing.T, dir string, mock runtime.Provider, noCache bool) (run.Manifest, string) {
	t.Helper()
	root, graph := loadAndBuild(t, dir)

	runID := run.NewRunID()
	runDir := run.RunDir(dir, runID)
	runtimeDir := run.RuntimeDir(runDir)
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("create run dir: %v", err)
	}

	task.RedirectPaths(root, dir, runtimeDir)

	var taskIDs []string
	task.WalkTree(root, func(tk *task.Task) {
		taskIDs = append(taskIDs, tk.ID)
	})
	manifest := run.NewManifest(runID, dir, noCache, nil, taskIDs)
	if err := run.WriteManifest(run.ManifestPath(runDir), manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	cfg := Config{
		Factory:   mockFactory(mock),
		NoCache:   noCache,
		RunID:     runID,
		RunDir:    runDir,
		CacheRoot: run.CacheRoot(dir),
	}

	_, err := Execute(context.Background(), root, graph, cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	m, err := run.ReadManifest(run.ManifestPath(runDir))
	if err != nil {
		t.Fatalf("read final manifest: %v", err)
	}
	return m, runDir
}

func TestRunLifecycle_FullRun(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Root task.",
		"agent.md": "model: mock/static",
	})

	mock := &runtime.MockProvider{Default: "Root output."}
	m, runDir := runWithManifest(t, dir, mock, false)

	// Manifest should be finalized.
	if m.Status != "success" {
		t.Errorf("manifest status = %q, want success", m.Status)
	}
	if m.CompletedAt == "" {
		t.Error("manifest completed_at is empty")
	}
	if len(m.Tasks) != 1 {
		t.Fatalf("manifest tasks = %d, want 1", len(m.Tasks))
	}
	if m.Tasks[0].Status != "success" {
		t.Errorf("task status = %q, want success", m.Tasks[0].Status)
	}

	// Artifacts should be in the run directory, not the source tree.
	runtimeDir := run.RuntimeDir(runDir)
	if _, err := os.Stat(filepath.Join(runtimeDir, "output", "result.md")); err != nil {
		t.Errorf("expected result.md in run dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "output", "result.md")); !os.IsNotExist(err) {
		t.Error("source tree should not have output/result.md")
	}
}

func TestRunLifecycle_CacheReuse(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Root task.",
		"agent.md": "model: mock/static",
	})

	mock := &runtime.MockProvider{Default: "Root output."}

	// Run 1: should execute.
	m1, _ := runWithManifest(t, dir, mock, false)
	if m1.Tasks[0].Status != "success" {
		t.Fatalf("run 1 task status = %q, want success", m1.Tasks[0].Status)
	}

	// Run 2: should get cache hit from shared cache.
	m2, _ := runWithManifest(t, dir, mock, false)
	if m2.Tasks[0].Status != "cached" {
		t.Errorf("run 2 task status = %q, want cached", m2.Tasks[0].Status)
	}
}

func TestRunLifecycle_ManifestFailure(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md": "---\noutput:\n  type: json\n---\nReturn JSON.",
		"agent.md": "model: mock/static",
		"schema.md": `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`,
	})

	mock := &runtime.MockProvider{
		Default: "not valid json at all",
	}
	// Can't use runWithManifest because it fatals on error.
	// Set up manually.
	root, graph := loadAndBuild(t, dir)
	runID := run.NewRunID()
	runDir := run.RunDir(dir, runID)
	runtimeDir := run.RuntimeDir(runDir)
	os.MkdirAll(runtimeDir, 0o755)
	task.RedirectPaths(root, dir, runtimeDir)

	var taskIDs []string
	task.WalkTree(root, func(tk *task.Task) {
		taskIDs = append(taskIDs, tk.ID)
	})
	manifest := run.NewManifest(runID, dir, false, nil, taskIDs)
	run.WriteManifest(run.ManifestPath(runDir), manifest)

	cfg := Config{
		Factory:   mockFactory(mock),
		RunID:     runID,
		RunDir:    runDir,
		CacheRoot: run.CacheRoot(dir),
	}
	result, _ := Execute(context.Background(), root, graph, cfg)

	// Read finalized manifest.
	m, err := run.ReadManifest(run.ManifestPath(runDir))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	if m.Status != "failed" {
		t.Errorf("manifest status = %q, want failed", m.Status)
	}
	if result != nil && result.Success {
		t.Error("expected result.Success = false")
	}
}

func TestRunLifecycle_RunsList(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Root task.",
		"agent.md": "model: mock/static",
	})

	mock := &runtime.MockProvider{Default: "Output."}

	// Create two runs.
	runWithManifest(t, dir, mock, true)
	runWithManifest(t, dir, mock, true)

	manifests, err := run.ListRuns(dir)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(manifests) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(manifests))
	}
	// Newest first.
	if manifests[0].RunID < manifests[1].RunID {
		t.Error("expected newest-first ordering")
	}
}

func TestRunLifecycle_Prune(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Root task.",
		"agent.md": "model: mock/static",
	})

	mock := &runtime.MockProvider{Default: "Output."}

	// Create two runs.
	runWithManifest(t, dir, mock, true)
	runWithManifest(t, dir, mock, true)

	// Prune keeping 1.
	removed, kept, err := run.PruneRuns(dir, run.PruneOpts{Keep: 1})
	if err != nil {
		t.Fatalf("PruneRuns: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if kept != 1 {
		t.Errorf("kept = %d, want 1", kept)
	}

	// Verify only 1 run remains.
	manifests, _ := run.ListRuns(dir)
	if len(manifests) != 1 {
		t.Errorf("expected 1 run after prune, got %d", len(manifests))
	}
}

func TestRunLifecycle_PruneDry(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Root task.",
		"agent.md": "model: mock/static",
	})

	mock := &runtime.MockProvider{Default: "Output."}
	runWithManifest(t, dir, mock, true)
	runWithManifest(t, dir, mock, true)

	// Dry prune should not actually remove.
	removed, _, err := run.PruneRuns(dir, run.PruneOpts{Keep: 1, Dry: true})
	if err != nil {
		t.Fatalf("PruneRuns dry: %v", err)
	}
	if removed != 1 {
		t.Errorf("dry removed = %d, want 1", removed)
	}

	// Both runs should still exist.
	manifests, _ := run.ListRuns(dir)
	if len(manifests) != 2 {
		t.Errorf("expected 2 runs after dry prune, got %d", len(manifests))
	}
}

func TestRunLifecycle_DryRunSeesSharedCache(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Root task.",
		"agent.md": "model: mock/static",
	})

	mock := &runtime.MockProvider{Default: "Output."}

	// Run once to populate shared cache.
	runWithManifest(t, dir, mock, false)

	// Dry run against the SOURCE tree (not redirected) should still see cache hit
	// via the shared cache, even though source tree has no output/ files.
	root, graph := loadAndBuild(t, dir)
	result := DryRun(root, graph, DryRunOpts{
		CacheRoot: run.CacheRoot(dir),
	})
	if len(result.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(result.Tasks))
	}
	if !result.Tasks[0].Cached {
		t.Error("expected dry-run to report cached from shared cache")
	}
}

func TestRunLifecycle_ManifestRunningState(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Root task.",
		"agent.md": "model: mock/static",
	})

	// Use a mock that lets us read the manifest mid-execution.
	var midRunManifest run.Manifest
	var midRunErr error

	mock := &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			// Read manifest while task is "running".
			runDir, _ := run.LatestRunDir(dir)
			midRunManifest, midRunErr = run.ReadManifest(run.ManifestPath(runDir))
			return &runtime.Response{Content: "done"}
		},
	}

	runWithManifest(t, dir, mock, true)

	if midRunErr != nil {
		t.Fatalf("failed to read manifest mid-execution: %v", midRunErr)
	}
	if len(midRunManifest.Tasks) == 0 {
		t.Fatal("manifest has no tasks")
	}
	if midRunManifest.Tasks[0].Status != "running" {
		t.Errorf("mid-run task status = %q, want running", midRunManifest.Tasks[0].Status)
	}
}

func TestRunLifecycle_DryRunShellCache(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "---\n---\necho hello",
		"agent.md": "model: shell",
	})

	// Run once to populate shared cache.
	mock := &runtime.MockProvider{Default: "unused"}
	runWithManifest(t, dir, mock, false)

	// Dry-run against source tree should see cache hit via shared cache.
	root, graph := loadAndBuild(t, dir)
	result := DryRun(root, graph, DryRunOpts{
		CacheRoot: run.CacheRoot(dir),
	})
	if len(result.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(result.Tasks))
	}
	if !result.Tasks[0].Cached {
		t.Error("expected dry-run to report shell task as cached from shared cache")
	}
}

func TestRunLifecycle_TwoPhaseStaysRunningDuringChildren(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":                        "Root two-phase task.",
		"agent.md":                       "model: mock/static",
		"return.md":                      "Synthesize child outputs.",
		"subtasks/01-child/task.md":      "Child task.",
		"subtasks/01-child/agent.md":     "model: mock/static",
	})

	// Use a mock that checks manifest status when the child executes.
	var childRunManifest run.Manifest
	var childRunErr error
	callCount := 0

	mock := &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			callCount++
			if callCount == 2 {
				// This is the child task executing. Check manifest for root task status.
				runDir, _ := run.LatestRunDir(dir)
				childRunManifest, childRunErr = run.ReadManifest(run.ManifestPath(runDir))
			}
			return &runtime.Response{Content: "output " + fmt.Sprintf("%d", callCount)}
		},
	}

	runWithManifest(t, dir, mock, true)

	if childRunErr != nil {
		t.Fatalf("failed to read manifest during child: %v", childRunErr)
	}

	// Find root task in manifest (TaskID = "").
	for _, mt := range childRunManifest.Tasks {
		if mt.TaskID == "" {
			if mt.Status != "running" {
				t.Errorf("root task during child execution: status = %q, want running", mt.Status)
			}
			return
		}
	}
	t.Error("root task not found in manifest")
}
