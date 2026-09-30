package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/input"
	"github.com/boxsie/smith/internal/output"
	"github.com/boxsie/smith/internal/prompt"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/task"
)

// --- T123: Shell dispatch ---

func TestShell_SimpleEcho(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "echo hello world",
		"agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
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
	if strings.TrimSpace(string(content)) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", string(content))
	}

	// .hash should exist.
	if _, err := os.Stat(filepath.Join(dir, "output", ".hash")); err != nil {
		t.Fatalf("expected .hash: %v", err)
	}

	// .running should be removed.
	if _, err := os.Stat(filepath.Join(dir, "output", ".running")); !os.IsNotExist(err) {
		t.Fatal("expected .running to be removed")
	}
}

func TestShell_NonZeroExit(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "echo 'failing' >&2; exit 1",
		"agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure")
	}
	if result.Tasks[0].Status != "failed" {
		t.Fatalf("expected failed, got %q", result.Tasks[0].Status)
	}
	// Stderr should be in the error.
	if result.Tasks[0].Err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(result.Tasks[0].Err.Error(), "failing") {
		t.Fatalf("expected stderr in error, got: %v", result.Tasks[0].Err)
	}
}

func TestShell_ProviderNotCalled(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "echo ok",
		"agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)

	// Factory that panics if called — shell should never touch it.
	factory := &runtime.Factory{
		Override: func(model string) (runtime.Provider, error) {
			t.Fatal("provider should not be called for shell tasks")
			return nil, nil
		},
	}

	result, err := Execute(context.Background(), root, graph, Config{
		Factory: factory,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

func TestShell_ToolsMDIgnored(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "echo ok",
		"agent.md": "model: shell",
		"tools.md": "- project.list\n- project.read",
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		for _, tr := range result.Tasks {
			if tr.Err != nil {
				t.Logf("task %q: %v", tr.TaskID, tr.Err)
			}
		}
		t.Fatal("expected success — tools.md should be ignored for shell tasks")
	}
}

func TestShell_InPipeline(t *testing.T) {
	// Shell task depends on an LLM task via numeric prefix ordering.
	dir := setupTree(t, map[string]string{
		"task.md":                    "Root",
		"agent.md":                   "model: mock/static",
		"subtasks/01-llm/task.md":    "LLM step",
		"subtasks/02-shell/task.md":  "echo processed",
		"subtasks/02-shell/agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	mock := &runtime.MockProvider{Default: "llm output"}

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

	// Shell task output should exist.
	content, err := os.ReadFile(filepath.Join(dir, "subtasks", "02-shell", "output", "result.md"))
	if err != nil {
		t.Fatalf("read shell result.md: %v", err)
	}
	if strings.TrimSpace(string(content)) != "processed" {
		t.Fatalf("unexpected shell output: %q", string(content))
	}
}

func TestShell_Metrics(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "echo metrics",
		"agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
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
	if m.Model != "shell" {
		t.Fatalf("expected model=shell, got %q", m.Model)
	}
	if m.TokensIn != 0 || m.TokensOut != 0 {
		t.Fatalf("expected zero tokens for shell, got in=%d out=%d", m.TokensIn, m.TokensOut)
	}
	if m.CostUSD != 0 {
		t.Fatalf("expected zero cost for shell, got %f", m.CostUSD)
	}
}

func TestShell_ContextTimeout(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "sleep 60\necho done",
		"agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	result, err := Execute(ctx, root, graph, Config{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure")
	}
	if result.Tasks[0].Err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(result.Tasks[0].Err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got: %v", result.Tasks[0].Err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout handling took too long: %v", elapsed)
	}
}

// --- T124: Shell environment variables ---

func TestShell_EnvInputVars(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  `echo "$SMITH_INPUT_PROJECT_NAME"`,
		"agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{
		RunInput: []input.Entry{
			{Name: "project_name", Value: "my-project"},
		},
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
	if strings.TrimSpace(string(content)) != "my-project" {
		t.Fatalf("expected 'my-project', got %q", string(content))
	}
}

func TestShell_EnvDepVars(t *testing.T) {
	// 01-gather produces output, 02-process reads it via SMITH_DEP_*.
	dir := setupTree(t, map[string]string{
		"task.md":                      "Root",
		"agent.md":                     "model: mock/static",
		"subtasks/01-gather/task.md":   "Gather data",
		"subtasks/02-process/task.md":  `cat "$SMITH_DEP_01_GATHER"`,
		"subtasks/02-process/agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	mock := &runtime.MockProvider{Default: "gathered data"}

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

	content, err := os.ReadFile(filepath.Join(dir, "subtasks", "02-process", "output", "result.md"))
	if err != nil {
		t.Fatalf("read result.md: %v", err)
	}
	if strings.TrimSpace(string(content)) != "gathered data" {
		t.Fatalf("expected 'gathered data', got %q", string(content))
	}
}

func TestShell_EnvParentOutput(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":                 "Root task",
		"agent.md":                "model: mock/static",
		"subtasks/child/task.md":  `cat "$SMITH_PARENT_OUTPUT"`,
		"subtasks/child/agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	mock := &runtime.MockProvider{Default: "parent content"}

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

	content, err := os.ReadFile(filepath.Join(dir, "subtasks", "child", "output", "result.md"))
	if err != nil {
		t.Fatalf("read result.md: %v", err)
	}
	if strings.TrimSpace(string(content)) != "parent content" {
		t.Fatalf("expected 'parent content', got %q", string(content))
	}
}

func TestShell_StdinInput(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "cat",
		"agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{
		RunInput: []input.Entry{
			{Name: "stdin", Value: "piped data"},
		},
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
	if string(content) != "piped data" {
		t.Fatalf("expected 'piped data', got %q", string(content))
	}
}

func TestShell_AmbientEnvInherited(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  `echo "$PATH" | head -c 1`,
		"agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		for _, tr := range result.Tasks {
			if tr.Err != nil {
				t.Logf("task %q: %v", tr.TaskID, tr.Err)
			}
		}
		t.Fatal("expected success — PATH should be inherited")
	}

	content, err := os.ReadFile(filepath.Join(dir, "output", "result.md"))
	if err != nil {
		t.Fatalf("read result.md: %v", err)
	}
	if strings.TrimSpace(string(content)) != "/" {
		t.Fatalf("expected PATH to start with /, got %q", string(content))
	}
}

func TestShell_StaticContextAccessible(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":                 `cat context/static/data.txt`,
		"agent.md":                "model: shell",
		"context/static/data.txt": "static file content",
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
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
	if strings.TrimSpace(string(content)) != "static file content" {
		t.Fatalf("expected 'static file content', got %q", string(content))
	}
}

// --- T125: Shell caching ---

func TestShell_CacheHit(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "echo cached",
		"agent.md": "model: shell",
	})

	// First run.
	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !result.Success {
		t.Fatal("first run failed")
	}
	if result.Tasks[0].Status != "success" {
		t.Fatalf("expected success, got %q", result.Tasks[0].Status)
	}

	// Second run — should be cached.
	root2, graph2 := loadAndBuild(t, dir)
	result2, err := Execute(context.Background(), root2, graph2, Config{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !result2.Success {
		t.Fatal("second run failed")
	}
	if result2.Tasks[0].Status != "cached" {
		t.Fatalf("expected cached, got %q", result2.Tasks[0].Status)
	}
}

func TestShell_CacheNever(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "---\ncache: never\n---\necho always",
		"agent.md": "model: shell",
	})

	// First run.
	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !result.Success {
		t.Fatal("first run failed")
	}

	// Second run — should NOT be cached due to cache: never.
	root2, graph2 := loadAndBuild(t, dir)
	result2, err := Execute(context.Background(), root2, graph2, Config{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !result2.Success {
		t.Fatal("second run failed")
	}
	if result2.Tasks[0].Status == "cached" {
		t.Fatal("expected not cached with cache: never")
	}
}

func TestShell_CacheIgnoresPersonaAndTemp(t *testing.T) {
	// Shell tasks should not be affected by persona/temperature changes in
	// inherited agent config. Changing these should NOT invalidate the cache.
	dir := setupTree(t, map[string]string{
		"task.md":  "echo stable",
		"agent.md": "model: shell\npersona: first persona\ntemperature: 0.5",
	})

	// First run.
	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !result.Success {
		t.Fatal("first run failed")
	}

	// Change persona and temperature.
	os.WriteFile(filepath.Join(dir, "agent.md"), []byte("model: shell\npersona: different persona\ntemperature: 0.9"), 0o644)

	// Second run — should still be cached because persona/temp are ignored for shell.
	root2, graph2 := loadAndBuild(t, dir)
	result2, err := Execute(context.Background(), root2, graph2, Config{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !result2.Success {
		t.Fatal("second run failed")
	}
	if result2.Tasks[0].Status != "cached" {
		t.Fatalf("expected cached (persona/temp change should not invalidate shell cache), got %q", result2.Tasks[0].Status)
	}
}

func TestShell_CacheInvalidatedByScriptChange(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "echo version1",
		"agent.md": "model: shell",
	})

	// First run.
	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !result.Success {
		t.Fatal("first run failed")
	}

	// Change script.
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("echo version2"), 0o644)

	// Second run — should NOT be cached.
	root2, graph2 := loadAndBuild(t, dir)
	result2, err := Execute(context.Background(), root2, graph2, Config{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !result2.Success {
		t.Fatal("second run failed")
	}
	if result2.Tasks[0].Status == "cached" {
		t.Fatal("expected cache miss after script change")
	}

	// Output should reflect the new script.
	content, err := os.ReadFile(filepath.Join(dir, "output", "result.md"))
	if err != nil {
		t.Fatalf("read result.md: %v", err)
	}
	if strings.TrimSpace(string(content)) != "version2" {
		t.Fatalf("expected 'version2', got %q", string(content))
	}
}

// --- T126: Shell JSON output ---

func TestShell_JSONOutput(t *testing.T) {
	schema := `{"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"]}`
	dir := setupTree(t, map[string]string{
		"task.md":   "---\noutput:\n  type: json\n---\necho '{\"name\": \"test\"}'",
		"agent.md":  "model: shell",
		"schema.md": schema,
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
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

func TestShell_JSONOutputInvalid(t *testing.T) {
	schema := `{"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"]}`
	dir := setupTree(t, map[string]string{
		"task.md":   "---\noutput:\n  type: json\n---\necho '{\"wrong\": \"field\"}'",
		"agent.md":  "model: shell",
		"schema.md": schema,
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for invalid JSON")
	}
	if result.Tasks[0].Status != "failed" {
		t.Fatalf("expected failed, got %q", result.Tasks[0].Status)
	}

	// result.md should be kept for debugging.
	if _, err := os.Stat(filepath.Join(dir, "output", "result.md")); err != nil {
		t.Fatal("expected result.md kept for debugging on JSON failure")
	}

	// result.json should NOT exist.
	if _, err := os.Stat(filepath.Join(dir, "output", "result.json")); !os.IsNotExist(err) {
		t.Fatal("expected no result.json for invalid JSON")
	}
}

func TestShell_JSONOutputMarkdownType(t *testing.T) {
	// Shell outputs JSON but output type is markdown — should only write result.md.
	dir := setupTree(t, map[string]string{
		"task.md":  `echo '{"name": "test"}'`,
		"agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}

	// result.md should exist.
	if _, err := os.Stat(filepath.Join(dir, "output", "result.md")); err != nil {
		t.Fatalf("expected result.md: %v", err)
	}

	// result.json should NOT exist.
	if _, err := os.Stat(filepath.Join(dir, "output", "result.json")); !os.IsNotExist(err) {
		t.Fatal("expected no result.json when output type is markdown")
	}
}

func TestShell_MultilineScript(t *testing.T) {
	script := "A=hello\nB=world\necho \"$A $B\""
	dir := setupTree(t, map[string]string{
		"task.md":  script,
		"agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
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
	if strings.TrimSpace(string(content)) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", string(content))
	}
}

func TestShell_WorkingDirectoryIsTaskPath(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "pwd",
		"agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}

	content, err := os.ReadFile(filepath.Join(dir, "output", "result.md"))
	if err != nil {
		t.Fatalf("read result.md: %v", err)
	}
	// The working directory should be the task's runtime path.
	if strings.TrimSpace(string(content)) != dir {
		t.Fatalf("expected working dir %q, got %q", dir, strings.TrimSpace(string(content)))
	}
}

func TestShell_SetEFlag(t *testing.T) {
	// -e flag should cause the script to fail on the first error.
	dir := setupTree(t, map[string]string{
		"task.md":  "false\necho should not reach here",
		"agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure due to -e flag")
	}
}

func TestShell_PreflightSkipsShellModel(t *testing.T) {
	// A tree with both shell and LLM tasks should not fail preflight
	// because "shell" is not a provider model.
	dir := setupTree(t, map[string]string{
		"task.md":                      "Root",
		"agent.md":                     "model: mock/static",
		"subtasks/shell-task/task.md":  "echo shell",
		"subtasks/shell-task/agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	mock := &runtime.MockProvider{Default: "ok"}

	result, err := Execute(context.Background(), root, graph, Config{
		Factory: mockFactory(mock),
	})
	if err != nil {
		t.Fatalf("preflight should not fail for shell model: %v", err)
	}
	if !result.Success {
		for _, tr := range result.Tasks {
			if tr.Err != nil {
				t.Logf("task %q: %v", tr.TaskID, tr.Err)
			}
		}
		t.Fatal("expected success")
	}
}

// Verify that the dep var uses the correct task ID normalization.
func TestShell_DepVarNormalization(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":                         "Root",
		"agent.md":                        "model: mock/static",
		"subtasks/01-gather-data/task.md": "Gather",
		"subtasks/02-process/task.md":     "env | grep SMITH_DEP",
		"subtasks/02-process/agent.md":    "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	mock := &runtime.MockProvider{Default: "data"}

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

	content, err := os.ReadFile(filepath.Join(dir, "subtasks", "02-process", "output", "result.md"))
	if err != nil {
		t.Fatalf("read result.md: %v", err)
	}
	// The dep ID is "01-gather-data", normalized to "01_GATHER_DATA".
	if !strings.Contains(string(content), "SMITH_DEP_01_GATHER_DATA=") {
		t.Fatalf("expected SMITH_DEP_01_GATHER_DATA, got %q", string(content))
	}
}

// --- Fix tests: stdout on failure, module context, source path cache ---

func TestShell_FailurePreservesStdout(t *testing.T) {
	// A script that produces output before failing should have that output
	// preserved in result.md for debugging.
	dir := setupTree(t, map[string]string{
		"task.md":  "echo before-fail\nexit 1",
		"agent.md": "model: shell",
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure")
	}
	if result.Tasks[0].Status != "failed" {
		t.Fatalf("expected failed, got %q", result.Tasks[0].Status)
	}

	// result.md should exist with the stdout captured before failure.
	content, err := os.ReadFile(filepath.Join(dir, "output", "result.md"))
	if err != nil {
		t.Fatalf("expected result.md to exist for debugging: %v", err)
	}
	if !strings.Contains(string(content), "before-fail") {
		t.Fatalf("expected 'before-fail' in result.md, got %q", string(content))
	}

	// .hash should NOT exist (task failed).
	if _, err := os.Stat(filepath.Join(dir, "output", ".hash")); !os.IsNotExist(err) {
		t.Fatal("expected no .hash for failed task")
	}
}

func TestShell_ContextDirEnvVar(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":                 `echo "$SMITH_CONTEXT_DIR"`,
		"agent.md":                "model: shell",
		"context/static/data.txt": "some data",
	})

	root, graph := loadAndBuild(t, dir)
	result, err := Execute(context.Background(), root, graph, Config{})
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
	expected := filepath.Join(dir, "context", "static")
	if strings.TrimSpace(string(content)) != expected {
		t.Fatalf("expected SMITH_CONTEXT_DIR=%q, got %q", expected, strings.TrimSpace(string(content)))
	}
}

func TestShell_ModuleContextSymlink(t *testing.T) {
	// Simulate a module-backed shell task where source != runtime.
	// The source has context/static/ but the runtime path does not.
	// The symlink should make relative paths work.
	sourceDir := t.TempDir()
	runtimeDir := t.TempDir()

	// Create source module with context/static/data.txt.
	os.MkdirAll(filepath.Join(sourceDir, "context", "static"), 0o755)
	os.WriteFile(filepath.Join(sourceDir, "context", "static", "data.txt"), []byte("module data"), 0o644)

	// Build task manually to set SourcePath with merged StaticContext.
	root := &task.Task{
		ID:             "",
		Path:           runtimeDir,
		SourcePath:     sourceDir,
		Body:           "cat context/static/data.txt",
		EffectiveAgent: task.AgentConfig{Model: "shell"},
		StaticContext: []prompt.StaticFile{
			{RelPath: "data.txt", Content: "module data"},
		},
	}
	graph := &task.Graph{
		Plan: task.ExecutionPlan{task.ExecutionLevel{{Task: root, Phase: "task", NodeID: task.TaskNodeID(root.ID)}}},
	}

	result, err := Execute(context.Background(), root, graph, Config{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		for _, tr := range result.Tasks {
			if tr.Err != nil {
				t.Logf("task %q: %v", tr.TaskID, tr.Err)
			}
		}
		t.Fatal("expected success — module context should be accessible via symlink")
	}

	content, err := os.ReadFile(filepath.Join(runtimeDir, "output", "result.md"))
	if err != nil {
		t.Fatalf("read result.md: %v", err)
	}
	if strings.TrimSpace(string(content)) != "module data" {
		t.Fatalf("expected 'module data', got %q", string(content))
	}

	// Symlink should be cleaned up after execution.
	if _, err := os.Lstat(filepath.Join(runtimeDir, "context")); !os.IsNotExist(err) {
		t.Fatal("expected context symlink to be cleaned up after execution")
	}
}

func TestShell_ModuleContextMerged(t *testing.T) {
	// Module source has context/static/mod.txt, referencing dir has
	// context/static/local.txt. Shell task should see both files via
	// context/static/ relative path (the RFC filesystem contract).
	sourceDir := t.TempDir()
	runtimeDir := t.TempDir()

	// Create source module context.
	os.MkdirAll(filepath.Join(sourceDir, "context", "static"), 0o755)
	os.WriteFile(filepath.Join(sourceDir, "context", "static", "mod.txt"), []byte("from module"), 0o644)

	// Create runtime override context.
	os.MkdirAll(filepath.Join(runtimeDir, "context", "static"), 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "context", "static", "local.txt"), []byte("from local"), 0o644)

	// Build task with merged StaticContext (as discovery would produce).
	// Uses relative paths — the contract from T124 and RFC 0002.
	root := &task.Task{
		ID:             "",
		Path:           runtimeDir,
		SourcePath:     sourceDir,
		Body:           `cat context/static/mod.txt && cat context/static/local.txt`,
		EffectiveAgent: task.AgentConfig{Model: "shell"},
		StaticContext: []prompt.StaticFile{
			{RelPath: "local.txt", Content: "from local"},
			{RelPath: "mod.txt", Content: "from module"},
		},
	}
	graph := &task.Graph{
		Plan: task.ExecutionPlan{task.ExecutionLevel{{Task: root, Phase: "task", NodeID: task.TaskNodeID(root.ID)}}},
	}

	result, err := Execute(context.Background(), root, graph, Config{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		for _, tr := range result.Tasks {
			if tr.Err != nil {
				t.Logf("task %q: %v", tr.TaskID, tr.Err)
			}
		}
		t.Fatal("expected success — merged context should be at context/static/ filesystem path")
	}

	content, err := os.ReadFile(filepath.Join(runtimeDir, "output", "result.md"))
	if err != nil {
		t.Fatalf("read result.md: %v", err)
	}
	out := string(content)
	if !strings.Contains(out, "from module") {
		t.Fatalf("expected 'from module' in output, got %q", out)
	}
	if !strings.Contains(out, "from local") {
		t.Fatalf("expected 'from local' in output, got %q", out)
	}

	// Source-only files should be cleaned up after execution.
	if _, err := os.Stat(filepath.Join(runtimeDir, "context", "static", "mod.txt")); !os.IsNotExist(err) {
		t.Fatal("expected source-only file mod.txt to be cleaned up after execution")
	}
	// Local override should still be present (not touched).
	if _, err := os.Stat(filepath.Join(runtimeDir, "context", "static", "local.txt")); err != nil {
		t.Fatal("expected local override local.txt to still exist after cleanup")
	}
}

func TestShell_CacheInvalidatedBySourcePathChange(t *testing.T) {
	// Two modules with identical bodies but different source paths.
	// Changing the source path should invalidate the shell cache.
	source1 := t.TempDir()
	source2 := t.TempDir()
	runtimeDir := t.TempDir()

	// Both modules have identical task bodies.
	body := `echo "hello from $SMITH_SOURCE_PATH"`
	for _, src := range []string{source1, source2} {
		os.WriteFile(filepath.Join(src, "task.md"), []byte(body), 0o644)
		os.WriteFile(filepath.Join(src, "agent.md"), []byte("model: shell"), 0o644)
	}

	// First run with source1.
	root1 := &task.Task{
		ID:             "",
		Path:           runtimeDir,
		SourcePath:     source1,
		Body:           body,
		EffectiveAgent: task.AgentConfig{Model: "shell"},
	}
	graph1 := &task.Graph{
		Plan: task.ExecutionPlan{task.ExecutionLevel{{Task: root1, Phase: "task", NodeID: task.TaskNodeID(root1.ID)}}},
	}
	result1, err := Execute(context.Background(), root1, graph1, Config{})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !result1.Success {
		t.Fatal("first run failed")
	}

	// Second run with source2 (different source path, same body).
	root2 := &task.Task{
		ID:             "",
		Path:           runtimeDir,
		SourcePath:     source2,
		Body:           body,
		EffectiveAgent: task.AgentConfig{Model: "shell"},
	}
	graph2 := &task.Graph{
		Plan: task.ExecutionPlan{task.ExecutionLevel{{Task: root2, Phase: "task", NodeID: task.TaskNodeID(root2.ID)}}},
	}
	result2, err := Execute(context.Background(), root2, graph2, Config{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !result2.Success {
		t.Fatal("second run failed")
	}
	// Should NOT be cached — source path changed.
	if result2.Tasks[0].Status == "cached" {
		t.Fatal("expected cache miss when source path changes")
	}
}
