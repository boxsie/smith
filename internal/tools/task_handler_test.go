package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/task"
)

// mockSubExecute creates a SubExecuteFunc that writes a result file to simulate execution.
func mockSubExecute(outputContent string, outputType string) SubExecuteFunc {
	return func(ctx context.Context, root *task.Task, graph *task.Graph, cfg SubExecConfig) error {
		// Write output to the root task's Path (which has been redirected to the invocation dir).
		outputDir := filepath.Join(root.Path, "output")
		os.MkdirAll(outputDir, 0o755)

		switch outputType {
		case "json":
			return os.WriteFile(filepath.Join(outputDir, "result.json"), []byte(outputContent), 0o644)
		case "markdown":
			return os.WriteFile(filepath.Join(outputDir, "result.md"), []byte(outputContent), 0o644)
		}
		return nil
	}
}

func setupTaskTool(t *testing.T, subExec SubExecuteFunc) (*TaskToolHandler, string) {
	t.Helper()
	root := t.TempDir()

	// Create a source task tree.
	sourceDir := filepath.Join(root, "source-task")
	os.MkdirAll(sourceDir, 0o755)
	os.WriteFile(filepath.Join(sourceDir, "task.md"), []byte("Process the input"), 0o644)
	os.WriteFile(filepath.Join(sourceDir, "agent.md"), []byte("model: mock/static"), 0o644)

	// Create tool definition.
	toolDir := filepath.Join(root, "tools", "my.task.tool")
	writeToolDir(t, toolDir, map[string]string{
		"tool.yaml":         "description: task tool\ntype: task\nsource: ../source-task\n",
		"input.schema.json": `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`,
	})

	def, err := ParseToolDir(toolDir)
	if err != nil {
		t.Fatalf("parse tool dir: %v", err)
	}
	def.ID = "my.task.tool"
	def.ResolvedSource = sourceDir

	// Build a minimal registry for sub-executions.
	subRegistry := NewRegistry()
	subRegistry.Register("project.read", &ProjectRead{})
	subRegistry.Register("project.list", &ProjectList{})
	subRegistry.Register("project.find", &ProjectFind{})
	subRegistry.Register("proposal.write", &ProposalWrite{})

	cfg := TaskToolConfig{
		SubExecute:   subExec,
		ProjectRoot:  root,
		Scope:        map[string]string{ScopeRoot: root},
		Adapter:      subRegistry,
		ResolvedDefs: subRegistry.Definitions(),
	}

	handler, err := NewTaskToolHandler(def, cfg)
	if err != nil {
		t.Fatalf("new task handler: %v", err)
	}
	return handler, root
}

func TestTaskHandler_JSONOutput(t *testing.T) {
	handler, root := setupTaskTool(t, mockSubExecute(`{"name":"Alice"}`, "json"))

	scope := map[string]string{ScopeRoot: root}
	out, err := handler.Execute(context.Background(), json.RawMessage(`{"id":"123"}`), scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]string
	json.Unmarshal(out, &result)
	if result["name"] != "Alice" {
		t.Errorf("got %v", result)
	}
}

func TestTaskHandler_MarkdownOutput(t *testing.T) {
	handler, root := setupTaskTool(t, mockSubExecute("# Hello World", "markdown"))

	scope := map[string]string{ScopeRoot: root}
	out, err := handler.Execute(context.Background(), json.RawMessage(`{"id":"123"}`), scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]string
	json.Unmarshal(out, &result)
	if result["content"] != "# Hello World" {
		t.Errorf("got %v", result)
	}
}

func TestTaskHandler_InputValidation(t *testing.T) {
	handler, root := setupTaskTool(t, mockSubExecute(`{}`, "json"))

	scope := map[string]string{ScopeRoot: root}
	_, err := handler.Execute(context.Background(), json.RawMessage(`{"wrong":"field"}`), scope)
	if err == nil || !strings.Contains(err.Error(), "input validation") {
		t.Fatalf("expected input validation error, got: %v", err)
	}
}

func TestTaskHandler_DepthLimitExceeded(t *testing.T) {
	handler, root := setupTaskTool(t, mockSubExecute(`{}`, "json"))

	ctx := WithDepth(context.Background(), MaxToolDepth)
	scope := map[string]string{ScopeRoot: root}
	_, err := handler.Execute(ctx, json.RawMessage(`{"id":"123"}`), scope)
	if err == nil || !strings.Contains(err.Error(), "depth limit") {
		t.Fatalf("expected depth limit error, got: %v", err)
	}
}

func TestTaskHandler_DepthIncrement(t *testing.T) {
	var capturedDepth int
	subExec := func(ctx context.Context, root *task.Task, graph *task.Graph, cfg SubExecConfig) error {
		capturedDepth = Depth(ctx)
		// Write output.
		outputDir := filepath.Join(root.Path, "output")
		os.MkdirAll(outputDir, 0o755)
		return os.WriteFile(filepath.Join(outputDir, "result.json"), []byte(`{}`), 0o644)
	}
	handler, root := setupTaskTool(t, subExec)

	ctx := WithDepth(context.Background(), 1)
	scope := map[string]string{ScopeRoot: root}
	_, err := handler.Execute(ctx, json.RawMessage(`{"id":"123"}`), scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedDepth != 2 {
		t.Errorf("sub-execution depth = %d, want 2", capturedDepth)
	}
}

func TestTaskHandler_InvocationDir(t *testing.T) {
	handler, root := setupTaskTool(t, mockSubExecute(`{}`, "json"))

	scope := map[string]string{ScopeRoot: root}
	_, err := handler.Execute(context.Background(), json.RawMessage(`{"id":"123"}`), scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify invocation artifacts exist.
	toolRunsDir := filepath.Join(root, ".smith", "tool-runs", "my.task.tool")
	entries, err := os.ReadDir(toolRunsDir)
	if err != nil {
		t.Fatalf("read tool-runs dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(entries))
	}

	inputFile := filepath.Join(toolRunsDir, entries[0].Name(), "input.json")
	data, err := os.ReadFile(inputFile)
	if err != nil {
		t.Fatalf("read input.json: %v", err)
	}
	if string(data) != `{"id":"123"}` {
		t.Errorf("input.json = %s", data)
	}
}

func TestTaskHandler_JsonRunInput(t *testing.T) {
	var capturedRunInput string
	subExec := func(ctx context.Context, root *task.Task, graph *task.Graph, cfg SubExecConfig) error {
		for _, e := range cfg.RunInput {
			if e.Name == "_json" {
				capturedRunInput = e.Value
			}
		}
		outputDir := filepath.Join(root.Path, "output")
		os.MkdirAll(outputDir, 0o755)
		return os.WriteFile(filepath.Join(outputDir, "result.json"), []byte(`{}`), 0o644)
	}
	handler, root := setupTaskTool(t, subExec)

	scope := map[string]string{ScopeRoot: root}
	_, err := handler.Execute(context.Background(), json.RawMessage(`{"id":"test-456"}`), scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedRunInput != `{"id":"test-456"}` {
		t.Errorf("_json run input = %q", capturedRunInput)
	}
}

func TestTaskHandler_TaskFailure(t *testing.T) {
	failExec := func(ctx context.Context, root *task.Task, graph *task.Graph, cfg SubExecConfig) error {
		return fmt.Errorf("subtask failed")
	}
	handler, root := setupTaskTool(t, failExec)

	scope := map[string]string{ScopeRoot: root}
	_, err := handler.Execute(context.Background(), json.RawMessage(`{"id":"123"}`), scope)
	if err == nil || !strings.Contains(err.Error(), "subtask failed") {
		t.Fatalf("expected task failure error, got: %v", err)
	}
}

func TestTaskHandler_Definition(t *testing.T) {
	handler, _ := setupTaskTool(t, mockSubExecute(`{}`, "json"))

	def := handler.Definition()
	if def.ID != "my.task.tool" {
		t.Errorf("ID = %q", def.ID)
	}
	if def.Description != "task tool" {
		t.Errorf("Description = %q", def.Description)
	}
}
