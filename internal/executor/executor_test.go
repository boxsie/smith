package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/output"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/task"
	"github.com/boxsie/smith/internal/tools"
)

// mockFactory returns a Factory that always returns the given provider.
func mockFactory(p runtime.Provider) *runtime.Factory {
	return &runtime.Factory{
		Override: func(model string) (runtime.Provider, error) {
			return p, nil
		},
	}
}

// setupTree creates a temp directory with the given file structure.
func setupTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for path, content := range files {
		fullPath := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// loadAndBuild discovers a tree, resolves agents, and builds the graph.
func loadAndBuild(t *testing.T, dir string) (*task.Task, *task.Graph) {
	t.Helper()
	root, err := task.DiscoverTree(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if err := task.ResolveAgentInheritance(root); err != nil {
		t.Fatalf("resolve agents: %v", err)
	}
	graph, err := task.BuildGraph(root)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	return root, graph
}

func TestExecute_SingleTask(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Summarize this.",
		"agent.md": "model: mock/static",
	})

	root, graph := loadAndBuild(t, dir)
	mock := &runtime.MockProvider{Default: "This is the summary."}

	result, err := Execute(context.Background(), root, graph, Config{
		Factory: mockFactory(mock),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got failures: %+v", result.Tasks)
	}
	if len(result.Tasks) != 1 {
		t.Fatalf("expected 1 task result, got %d", len(result.Tasks))
	}
	if result.Tasks[0].Status != "success" {
		t.Fatalf("expected success status, got %q", result.Tasks[0].Status)
	}

	// Verify output files.
	content, err := os.ReadFile(filepath.Join(dir, "output", "result.md"))
	if err != nil {
		t.Fatalf("read result.md: %v", err)
	}
	if string(content) != "This is the summary." {
		t.Fatalf("unexpected result.md: %q", string(content))
	}

	// .hash should exist.
	if _, err := os.Stat(filepath.Join(dir, "output", ".hash")); err != nil {
		t.Fatalf("expected .hash: %v", err)
	}

	// .running should be removed.
	if _, err := os.Stat(filepath.Join(dir, "output", ".running")); !os.IsNotExist(err) {
		t.Fatal("expected .running to be removed")
	}

	// .metrics.json should exist.
	if _, err := os.Stat(filepath.Join(dir, "output", ".metrics.json")); err != nil {
		t.Fatalf("expected .metrics.json: %v", err)
	}
}

func TestExecute_Pipeline(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":                    "Root task",
		"agent.md":                   "model: mock/static",
		"subtasks/01-first/task.md":  "First step",
		"subtasks/02-second/task.md": "Second step",
		"subtasks/03-third/task.md":  "Third step",
	})

	root, graph := loadAndBuild(t, dir)
	mock := &runtime.MockProvider{Default: "done"}

	result, err := Execute(context.Background(), root, graph, Config{
		Factory: mockFactory(mock),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		for _, tr := range result.Tasks {
			if tr.Err != nil {
				t.Logf("task %q failed: %v", tr.TaskID, tr.Err)
			}
		}
		t.Fatal("expected success")
	}

	// All 4 tasks (root + 3 children) should have results.
	if len(result.Tasks) != 4 {
		t.Fatalf("expected 4 task results, got %d", len(result.Tasks))
	}

	// Verify all output files exist.
	for _, sub := range []string{"", "subtasks/01-first", "subtasks/02-second", "subtasks/03-third"} {
		path := filepath.Join(dir, sub, "output", "result.md")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected result.md at %s: %v", path, err)
		}
	}
}

func TestExecute_CacheHit(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Cached task",
		"agent.md": "model: mock/static",
	})

	root, graph := loadAndBuild(t, dir)
	mock := &runtime.MockProvider{Default: "first run"}

	// First run.
	result, err := Execute(context.Background(), root, graph, Config{
		Factory: mockFactory(mock),
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !result.Success {
		t.Fatal("first run failed")
	}

	// Second run — should be cached.
	mock2 := &runtime.MockProvider{Default: "second run"}
	root2, graph2 := loadAndBuild(t, dir)
	result2, err := Execute(context.Background(), root2, graph2, Config{
		Factory: mockFactory(mock2),
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !result2.Success {
		t.Fatal("second run failed")
	}
	if result2.Tasks[0].Status != "cached" {
		t.Fatalf("expected cached, got %q", result2.Tasks[0].Status)
	}

	// Provider should not have been called on second run.
	if len(mock2.Calls) != 0 {
		t.Fatalf("expected 0 provider calls on cache hit, got %d", len(mock2.Calls))
	}

	// Original content should be preserved.
	content, err := os.ReadFile(filepath.Join(dir, "output", "result.md"))
	if err != nil {
		t.Fatalf("read result.md: %v", err)
	}
	if string(content) != "first run" {
		t.Fatalf("expected cached content, got %q", content)
	}
}

func TestExecute_FailedTaskSkipsDependents(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":                    "Root",
		"agent.md":                   "model: mock/static",
		"subtasks/01-first/task.md":  "First step",
		"subtasks/02-second/task.md": "Second step",
	})

	root, graph := loadAndBuild(t, dir)

	// Use an erroring provider for 01-first.
	factory := &runtime.Factory{
		Override: func(model string) (runtime.Provider, error) {
			return &errorOnContentProvider{failOn: "First step"}, nil
		},
	}

	result, err := Execute(context.Background(), root, graph, Config{
		Factory: factory,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure")
	}

	statuses := make(map[string]string)
	for _, tr := range result.Tasks {
		statuses[tr.TaskID] = tr.Status
	}

	if statuses[""] != "success" {
		t.Fatalf("root should succeed, got %q", statuses[""])
	}
	if statuses["01-first"] != "failed" {
		t.Fatalf("01-first should fail, got %q", statuses["01-first"])
	}
	if statuses["02-second"] != "skipped" {
		t.Fatalf("02-second should be skipped, got %q", statuses["02-second"])
	}
}

// errorOnContentProvider returns an error when the prompt contains failOn.
type errorOnContentProvider struct {
	failOn string
}

func (p *errorOnContentProvider) Execute(_ context.Context, req *runtime.Request) (*runtime.Response, error) {
	for _, msg := range req.Messages {
		if msg.Role == "user" && strings.Contains(msg.Text, p.failOn) {
			return nil, fmt.Errorf("simulated provider error")
		}
	}
	return &runtime.Response{Content: "ok"}, nil
}

func TestExecute_RootWithParallelChildren(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":                "Root",
		"agent.md":               "model: mock/static",
		"subtasks/alpha/task.md": "Alpha",
		"subtasks/beta/task.md":  "Beta",
	})

	root, graph := loadAndBuild(t, dir)
	mock := &runtime.MockProvider{Default: "done"}

	result, err := Execute(context.Background(), root, graph, Config{
		Factory: mockFactory(mock),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if len(result.Tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(result.Tasks))
	}
}

func TestExecute_ToolCallTask(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Use the tool",
		"agent.md": "model: mock/static",
		"tools.md": "- test.tool",
	})

	root, graph := loadAndBuild(t, dir)

	// Provider that issues a tool call on first request, then returns final text.
	callNum := 0
	mock := &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			callNum++
			if callNum == 1 {
				return &runtime.Response{
					ToolCalls: []runtime.ToolCall{{
						ID:     "call_1",
						ToolID: "test.tool",
						Input:  json.RawMessage(`{"query": "hello"}`),
					}},
				}
			}
			return &runtime.Response{Content: "tool result processed"}
		},
	}

	adapter := &tools.FakeAdapter{
		Handlers: map[string]func(json.RawMessage) (json.RawMessage, error){
			"test.tool": func(input json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"result": "world"}`), nil
			},
		},
	}

	result, err := Execute(context.Background(), root, graph, Config{
		Factory: mockFactory(mock),
		Adapter: adapter,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		for _, tr := range result.Tasks {
			if tr.Err != nil {
				t.Logf("task %q: %v", tr.TaskID, tr.Err)
			}
		}
		t.Fatal("expected success")
	}

	content, err := os.ReadFile(filepath.Join(dir, "output", "result.md"))
	if err != nil {
		t.Fatalf("read result.md: %v", err)
	}
	if string(content) != "tool result processed" {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestExecute_JSONTask(t *testing.T) {
	schema := `{"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"]}`
	dir := setupTree(t, map[string]string{
		"task.md":   "---\noutput:\n  type: json\n---\nProduce JSON",
		"agent.md":  "model: mock/static",
		"schema.md": schema,
	})

	root, graph := loadAndBuild(t, dir)
	mock := &runtime.MockProvider{Default: `{"name": "test"}`}

	result, err := Execute(context.Background(), root, graph, Config{
		Factory: mockFactory(mock),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		for _, tr := range result.Tasks {
			if tr.Err != nil {
				t.Logf("task %q: %v", tr.TaskID, tr.Err)
			}
		}
		t.Fatal("expected success")
	}

	// result.json should exist.
	data, err := os.ReadFile(filepath.Join(dir, "output", "result.json"))
	if err != nil {
		t.Fatalf("read result.json: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse result.json: %v", err)
	}
	if parsed["name"] != "test" {
		t.Fatalf("unexpected json: %v", parsed)
	}
}

func TestExecute_JSONTaskInvalid(t *testing.T) {
	schema := `{"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"]}`
	dir := setupTree(t, map[string]string{
		"task.md":   "---\noutput:\n  type: json\n---\nProduce JSON",
		"agent.md":  "model: mock/static",
		"schema.md": schema,
	})

	root, graph := loadAndBuild(t, dir)
	// Response without required field.
	mock := &runtime.MockProvider{Default: `{"wrong": "field"}`}

	result, err := Execute(context.Background(), root, graph, Config{
		Factory: mockFactory(mock),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for invalid JSON")
	}
	if result.Tasks[0].Status != "failed" {
		t.Fatalf("expected failed, got %q", result.Tasks[0].Status)
	}

	// .hash should NOT exist.
	if _, err := os.Stat(filepath.Join(dir, "output", ".hash")); !os.IsNotExist(err) {
		t.Fatal("expected no .hash for failed json task")
	}
}

func TestExecute_StaleRunningRecovery(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Task with stale running",
		"agent.md": "model: mock/static",
	})

	// Write a stale .running marker.
	outDir := filepath.Join(dir, "output")
	os.MkdirAll(outDir, 0o755)
	os.WriteFile(filepath.Join(outDir, ".running"), []byte(`{"task":"","started_at":"2026-01-01T00:00:00Z"}`), 0o644)

	root, graph := loadAndBuild(t, dir)
	mock := &runtime.MockProvider{Default: "recovered"}

	result, err := Execute(context.Background(), root, graph, Config{
		Factory: mockFactory(mock),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success after stale .running recovery")
	}

	// .running should be cleaned up.
	if _, err := os.Stat(filepath.Join(outDir, ".running")); !os.IsNotExist(err) {
		t.Fatal("expected .running to be removed after recovery")
	}
}

func TestExecute_MetricsWritten(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Metrics task",
		"agent.md": "model: mock/static",
	})

	root, graph := loadAndBuild(t, dir)
	mock := &runtime.MockProvider{Default: "done"}

	result, err := Execute(context.Background(), root, graph, Config{
		Factory: mockFactory(mock),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}

	data, err := os.ReadFile(filepath.Join(dir, "output", ".metrics.json"))
	if err != nil {
		t.Fatalf("read .metrics.json: %v", err)
	}

	var m output.Metrics
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse .metrics.json: %v", err)
	}
	if m.Status != "success" {
		t.Fatalf("expected success status in metrics, got %q", m.Status)
	}
	if m.Cached {
		t.Fatal("expected cached=false for executed task")
	}
}

func TestExecute_SameLevelFailureDoesNotBlockSiblings(t *testing.T) {
	// Two unprefixed siblings — if one fails, the other should still run.
	dir := setupTree(t, map[string]string{
		"task.md":                "Root",
		"agent.md":               "model: mock/static",
		"subtasks/alpha/task.md": "Alpha will fail",
		"subtasks/beta/task.md":  "Beta should succeed",
	})

	root, graph := loadAndBuild(t, dir)

	factory := &runtime.Factory{
		Override: func(model string) (runtime.Provider, error) {
			return &errorOnContentProvider{failOn: "Alpha"}, nil
		},
	}

	result, err := Execute(context.Background(), root, graph, Config{
		Factory: factory,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	statuses := make(map[string]string)
	for _, tr := range result.Tasks {
		statuses[tr.TaskID] = tr.Status
	}

	if statuses["alpha"] != "failed" {
		t.Fatalf("alpha should fail, got %q", statuses["alpha"])
	}
	if statuses["beta"] != "success" {
		t.Fatalf("beta should succeed even though alpha failed, got %q", statuses["beta"])
	}
}

func TestExecute_FailedTaskLeavesRunning(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Will fail",
		"agent.md": "model: mock/static",
	})

	root, graph := loadAndBuild(t, dir)
	factory := &runtime.Factory{
		Override: func(model string) (runtime.Provider, error) {
			return &errorOnContentProvider{failOn: "Will fail"}, nil
		},
	}

	result, err := Execute(context.Background(), root, graph, Config{
		Factory: factory,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure")
	}

	// .running should remain for debugging.
	if _, err := os.Stat(filepath.Join(dir, "output", ".running")); os.IsNotExist(err) {
		t.Fatal("expected .running to remain after failure")
	}

	// .hash should not exist.
	if _, err := os.Stat(filepath.Join(dir, "output", ".hash")); !os.IsNotExist(err) {
		t.Fatal("expected no .hash after failure")
	}
}

func TestDryRun(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":                    "Root",
		"agent.md":                   "model: mock/static",
		"subtasks/01-first/task.md":  "First",
		"subtasks/02-second/task.md": "Second",
	})

	root, graph := loadAndBuild(t, dir)
	result := DryRun(root, graph)

	if len(result.Tasks) != 3 {
		t.Fatalf("expected 3 tasks in dry run, got %d", len(result.Tasks))
	}

	// All should be uncached.
	for _, dt := range result.Tasks {
		if dt.Cached {
			t.Fatalf("expected uncached for %q", dt.TaskID)
		}
	}

	// No output files should be written.
	for _, sub := range []string{"", "subtasks/01-first", "subtasks/02-second"} {
		outDir := filepath.Join(dir, sub, "output")
		if _, err := os.Stat(outDir); !os.IsNotExist(err) {
			t.Fatalf("dry run should not create output/ at %s", outDir)
		}
	}
}

func TestDryRun_WithCache(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Cached task",
		"agent.md": "model: mock/static",
	})

	// Execute first to populate cache.
	root, graph := loadAndBuild(t, dir)
	mock := &runtime.MockProvider{Default: "done"}
	Execute(context.Background(), root, graph, Config{Factory: mockFactory(mock)})

	// Dry run should show cached.
	root2, graph2 := loadAndBuild(t, dir)
	result := DryRun(root2, graph2)
	if len(result.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(result.Tasks))
	}
	if !result.Tasks[0].Cached {
		t.Fatal("expected cached=true after previous run")
	}
}

func TestTaskStatus(t *testing.T) {
	dir := t.TempDir()

	// No output → pending.
	if s := TaskStatus(dir, "markdown", false); s != "pending" {
		t.Fatalf("expected pending, got %q", s)
	}

	// Create output dir with .running.
	outDir := filepath.Join(dir, "output")
	os.MkdirAll(outDir, 0o755)
	os.WriteFile(filepath.Join(outDir, ".running"), []byte("{}"), 0o644)
	if s := TaskStatus(dir, "markdown", false); s != "running" {
		t.Fatalf("expected running, got %q", s)
	}

	// Remove .running, add result.md without .hash → failed.
	os.Remove(filepath.Join(outDir, ".running"))
	os.WriteFile(filepath.Join(outDir, "result.md"), []byte("output"), 0o644)
	if s := TaskStatus(dir, "markdown", false); s != "failed" {
		t.Fatalf("expected failed, got %q", s)
	}

	// Add .hash → success.
	os.WriteFile(filepath.Join(outDir, ".hash"), []byte("abc"), 0o644)
	if s := TaskStatus(dir, "markdown", false); s != "success" {
		t.Fatalf("expected success, got %q", s)
	}
}

func TestExecute_TransitiveSkipPropagation(t *testing.T) {
	// 3-step sequential chain: 01 → 02 → 03.
	// If 01 fails, both 02 and 03 should be skipped (not just 02).
	dir := setupTree(t, map[string]string{
		"task.md":                    "Root",
		"agent.md":                   "model: mock/static",
		"subtasks/01-first/task.md":  "First will fail",
		"subtasks/02-second/task.md": "Second should be skipped",
		"subtasks/03-third/task.md":  "Third should also be skipped",
	})

	root, graph := loadAndBuild(t, dir)

	factory := &runtime.Factory{
		Override: func(model string) (runtime.Provider, error) {
			return &errorOnContentProvider{failOn: "First will fail"}, nil
		},
	}

	result, err := Execute(context.Background(), root, graph, Config{
		Factory: factory,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure")
	}

	statuses := make(map[string]string)
	for _, tr := range result.Tasks {
		statuses[tr.TaskID] = tr.Status
	}

	if statuses[""] != "success" {
		t.Fatalf("root should succeed, got %q", statuses[""])
	}
	if statuses["01-first"] != "failed" {
		t.Fatalf("01-first should fail, got %q", statuses["01-first"])
	}
	if statuses["02-second"] != "skipped" {
		t.Fatalf("02-second should be skipped, got %q", statuses["02-second"])
	}
	if statuses["03-third"] != "skipped" {
		t.Fatalf("03-third should be skipped (transitive), got %q", statuses["03-third"])
	}
}

func TestExecute_PreflightBadModel(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Root",
		"agent.md": "model: badprefix/model",
	})

	root, graph := loadAndBuild(t, dir)

	_, err := Execute(context.Background(), root, graph, Config{
		Factory: runtime.DefaultFactory(),
	})
	if err == nil {
		t.Fatal("expected preflight error for unsupported model")
	}
	if !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("expected preflight error, got: %v", err)
	}
}

func TestExecute_FailedTaskWritesMetrics(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Will fail",
		"agent.md": "model: mock/static",
	})

	root, graph := loadAndBuild(t, dir)
	factory := &runtime.Factory{
		Override: func(model string) (runtime.Provider, error) {
			return &errorOnContentProvider{failOn: "Will fail"}, nil
		},
	}

	result, err := Execute(context.Background(), root, graph, Config{
		Factory: factory,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure")
	}

	// .metrics.json should still be written for the failed task.
	data, err := os.ReadFile(filepath.Join(dir, "output", ".metrics.json"))
	if err != nil {
		t.Fatalf("expected .metrics.json for failed task: %v", err)
	}

	var m output.Metrics
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse .metrics.json: %v", err)
	}
	if m.Status != "failed" {
		t.Fatalf("expected failed status in metrics, got %q", m.Status)
	}
}

func TestExecute_NoCacheForcesRerun(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Rerun me",
		"agent.md": "model: mock/static",
	})

	mock := &runtime.MockProvider{Default: "first run"}
	root, graph := loadAndBuild(t, dir)

	// First run populates cache.
	result, err := Execute(context.Background(), root, graph, Config{
		Factory: mockFactory(mock),
	})
	if err != nil || !result.Success {
		t.Fatalf("first run: err=%v success=%v", err, result.Success)
	}

	// Second run with NoCache — should call provider again, not use cache.
	mock2 := &runtime.MockProvider{Default: "second run"}
	root2, graph2 := loadAndBuild(t, dir)
	result2, err := Execute(context.Background(), root2, graph2, Config{
		Factory: mockFactory(mock2),
		NoCache: true,
	})
	if err != nil || !result2.Success {
		t.Fatalf("second run: err=%v success=%v", err, result2.Success)
	}
	if result2.Tasks[0].Status != "success" {
		t.Fatalf("expected success (not cached), got %q", result2.Tasks[0].Status)
	}
	// Provider should have been called — verify by checking result content.
	content, _ := os.ReadFile(filepath.Join(dir, "output", "result.md"))
	if string(content) != "second run" {
		t.Fatalf("expected 'second run' output, got %q — NoCache did not force rerun", string(content))
	}
}

func TestDryRun_ReportsScope(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Scoped task",
		"agent.md": "model: mock/static",
		"tools.md": "- project.read\n",
	})

	root, graph := loadAndBuild(t, dir)
	scope := map[string]string{"root": "/target", "proposal_id": "test-123"}

	result := DryRun(root, graph, DryRunOpts{Scope: scope})
	if len(result.Tasks) == 0 {
		t.Fatal("expected at least one task in dry run")
	}
	// Root task has tools, so scope should be populated.
	if result.Tasks[0].Scope == nil {
		t.Fatal("expected scope in dry-run task, got nil")
	}
	if result.Tasks[0].Scope["root"] != "/target" {
		t.Errorf("expected scope root=/target, got %q", result.Tasks[0].Scope["root"])
	}
}
