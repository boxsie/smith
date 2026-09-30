package validate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/runtime"
)

type validExternalRuntime struct{}

func (validExternalRuntime) Invoke(ctx context.Context, _ runtime.Invocation, sink runtime.InvocationSink) error {
	return sink.Complete(ctx, &runtime.ExternalResult{Text: "ok"})
}

func (validExternalRuntime) Admit(_ context.Context, request runtime.ContainmentRequest) (runtime.ContainmentAdmission, error) {
	return runtime.ContainmentAdmission{RequestedProfile: request.Profile, Mechanism: "test_containment", EffectiveLimits: request.Limits, Enforced: true}, nil
}

func TestValidate_ValidSimple(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Do something useful",
		"agent.md": "model: mock/static",
	})

	result := Validate(dir)
	if len(result.Errs) > 0 {
		t.Fatalf("expected no errors, got %v", result.Errs)
	}
	if result.Root == nil {
		t.Fatal("expected root task")
	}
	if result.Graph == nil {
		t.Fatal("expected graph")
	}
}

func TestValidate_NoTaskMD(t *testing.T) {
	dir := t.TempDir()
	result := Validate(dir)
	if len(result.Errs) == 0 {
		t.Fatal("expected errors for missing task.md")
	}
}

func TestValidate_NoModel(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md": "Do something",
	})

	result := Validate(dir)
	if len(result.Errs) == 0 {
		t.Fatal("expected error for missing model")
	}
}

func TestValidate_WithSubtasks(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":                    "Root task",
		"agent.md":                   "model: mock/static",
		"subtasks/01-first/task.md":  "First child",
		"subtasks/02-second/task.md": "Second child",
	})

	result := Validate(dir)
	if len(result.Errs) > 0 {
		t.Fatalf("expected no errors, got %v", result.Errs)
	}
	if len(result.Graph.Plan) == 0 {
		t.Fatal("expected non-empty execution plan")
	}
}

func TestValidate_Cycle(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":            "Root",
		"agent.md":           "model: mock/static",
		"subtasks/a/task.md": "---\ndepends_on:\n  - b\n---\nTask A",
		"subtasks/b/task.md": "---\ndepends_on:\n  - a\n---\nTask B",
	})

	result := Validate(dir)
	if len(result.Errs) == 0 {
		t.Fatal("expected cycle error")
	}
}

func TestValidate_ReservedPath(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Root",
		"agent.md": "model: mock/static",
		"when.md":  "trigger",
	})

	result := Validate(dir)
	if len(result.Errs) == 0 {
		t.Fatal("expected error for reserved path")
	}
}

func TestValidate_JSONWithoutSchema(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "---\noutput:\n  type: json\n---\nProduce JSON",
		"agent.md": "model: mock/static",
	})

	result := Validate(dir)
	if len(result.Errs) == 0 {
		t.Fatal("expected error for json output without schema")
	}
}

// --- T136: Validation consolidation tests ---

func TestValidate_ModelShellAccepted(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "echo hello",
		"agent.md": "model: shell",
	})

	result := Validate(dir)
	if len(result.Errs) > 0 {
		t.Fatalf("model: shell should pass validation, got %v", result.Errs)
	}
}

func TestValidate_InvalidModelFormatRejected(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Do something",
		"agent.md": "model: claude-opus-4-5",
	})

	result := Validate(dir)
	if len(result.Errs) == 0 {
		t.Fatal("expected invalid model format to fail validation")
	}
	if !strings.Contains(result.Errs[0].Error(), "invalid model") {
		t.Fatalf("expected invalid model error, got %v", result.Errs)
	}
}

func TestValidate_ExternalRuntimeUsesSeparateResolver(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Do something",
		"agent.md": "runtime: frontier-test\nmodel: gpt-5.6\n",
	})

	rejected := Validate(dir)
	if len(rejected.Errs) == 0 || !strings.Contains(rejected.Errs[0].Error(), `invalid runtime "frontier-test"`) {
		t.Fatalf("default validation errors = %v", rejected.Errs)
	}
	accepted := ValidateWithFactories(dir, runtime.DefaultFactory(), &runtime.ExternalFactory{
		Runtimes: map[string]runtime.ExternalRuntime{"frontier-test": validExternalRuntime{}},
	})
	if len(accepted.Errs) != 0 {
		t.Fatalf("injected external validation errors = %v", accepted.Errs)
	}
}

func TestValidate_InvalidChildModelRejected(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":                    "Root",
		"agent.md":                   "model: anthropic/claude-sonnet-4-6",
		"subtasks/01-child/task.md":  "Child",
		"subtasks/01-child/agent.md": "model: claude-opus-4-5",
	})

	result := Validate(dir)
	if len(result.Errs) == 0 {
		t.Fatal("expected invalid child model to fail validation")
	}
	found := false
	for _, err := range result.Errs {
		if strings.Contains(err.Error(), `task "01-child": invalid model`) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected child invalid model error, got %v", result.Errs)
	}
}

func TestValidate_CacheAutoAccepted(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "---\ncache: auto\n---\nDo something",
		"agent.md": "model: mock/static",
	})

	result := Validate(dir)
	if len(result.Errs) > 0 {
		t.Fatalf("cache: auto should pass, got %v", result.Errs)
	}
}

func TestValidate_CacheNeverAccepted(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "---\ncache: never\n---\nDo something",
		"agent.md": "model: mock/static",
	})

	result := Validate(dir)
	if len(result.Errs) > 0 {
		t.Fatalf("cache: never should pass, got %v", result.Errs)
	}
}

func TestValidate_CacheInvalidRejected(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "---\ncache: always\n---\nDo something",
		"agent.md": "model: mock/static",
	})

	result := Validate(dir)
	if len(result.Errs) == 0 {
		t.Fatal("cache: always should fail validation")
	}
}

func TestValidate_ModuleYAMLAccepted(t *testing.T) {
	dir := t.TempDir()

	// Create lib module.
	modDir := filepath.Join(dir, ".smith", "lib", "testmod")
	os.MkdirAll(modDir, 0o755)
	os.WriteFile(filepath.Join(modDir, "task.md"), []byte("Module task"), 0o644)

	// Root task with module child.
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("Root task"), 0o644)
	os.WriteFile(filepath.Join(dir, "agent.md"), []byte("model: mock/static"), 0o644)

	childDir := filepath.Join(dir, "subtasks", "01-mod")
	os.MkdirAll(childDir, 0o755)
	os.WriteFile(filepath.Join(childDir, "module.yaml"), []byte("source: testmod\n"), 0o644)

	result := Validate(dir)
	if len(result.Errs) > 0 {
		t.Fatalf("module.yaml task should pass, got %v", result.Errs)
	}
}

func TestValidate_ModuleAndTaskMDRejected(t *testing.T) {
	dir := t.TempDir()

	// Root with both module.yaml and task.md.
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("Root task"), 0o644)
	os.WriteFile(filepath.Join(dir, "agent.md"), []byte("model: mock/static"), 0o644)

	childDir := filepath.Join(dir, "subtasks", "01-bad")
	os.MkdirAll(childDir, 0o755)
	os.WriteFile(filepath.Join(childDir, "task.md"), []byte("Normal task"), 0o644)
	os.WriteFile(filepath.Join(childDir, "module.yaml"), []byte("source: foo\n"), 0o644)

	result := Validate(dir)
	if len(result.Errs) == 0 {
		t.Fatal("module.yaml + task.md should fail validation")
	}
}

func TestValidate_SmithDirNotDiscovered(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Root task",
		"agent.md": "model: mock/static",
	})

	// Put a task.md inside .smith/ — should not be discovered.
	smithDir := filepath.Join(dir, ".smith", "proposals", "123")
	os.MkdirAll(smithDir, 0o755)
	os.WriteFile(filepath.Join(smithDir, "task.md"), []byte("Hidden task"), 0o644)

	result := Validate(dir)
	if len(result.Errs) > 0 {
		t.Fatalf("task.md in .smith/ should be ignored, got %v", result.Errs)
	}
}

func TestValidate_UnresolvableToolID(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Do something",
		"agent.md": "model: mock/static",
		"tools.md": "- nonexistent.tool\n",
	})

	result := Validate(dir)
	if len(result.Errs) == 0 {
		t.Fatal("expected validation error for unresolvable tool")
	}
	found := false
	for _, e := range result.Errs {
		if strings.Contains(e.Error(), "nonexistent.tool") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected error mentioning nonexistent.tool, got: %v", result.Errs)
	}
}

func TestValidate_AppToolResolves(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":                         "Do something",
		"agent.md":                        "model: mock/static",
		"tools.md":                        "- my.tool\n- project.read\n",
		"tools/my.tool/tool.yaml":         "description: test\ntype: shell\n",
		"tools/my.tool/input.schema.json": `{"type":"object"}`,
		"tools/my.tool/run.sh":            "#!/bin/sh\necho '{}'",
	})

	result := Validate(dir)
	if len(result.Errs) > 0 {
		t.Fatalf("expected no errors, got %v", result.Errs)
	}
	if result.ResolvedTools == nil {
		t.Fatal("expected ResolvedTools to be populated")
	}
	if _, ok := result.ResolvedTools.AppDefs["my.tool"]; !ok {
		t.Fatal("expected my.tool in ResolvedTools")
	}
	// Built-in project.read should not be in ResolvedTools (it's built-in only).
	if _, ok := result.ResolvedTools.AppDefs["project.read"]; ok {
		t.Fatal("project.read should not be in ResolvedTools (it's built-in)")
	}
}

func TestValidate_NoToolsDirNoRegression(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":  "Do something",
		"agent.md": "model: mock/static",
		"tools.md": "- project.read\n",
	})

	result := Validate(dir)
	if len(result.Errs) > 0 {
		t.Fatalf("expected no errors, got %v", result.Errs)
	}
}

func TestValidate_UnknownNativeToolRejected(t *testing.T) {
	dir := setupTree(t, map[string]string{
		"task.md":                               "Do something",
		"agent.md":                              "model: mock/static",
		"tools.md":                              "- custom.native\n",
		"tools/custom.native/tool.yaml":         "description: native\ntype: native\n",
		"tools/custom.native/input.schema.json": `{"type":"object"}`,
	})

	result := Validate(dir)
	if len(result.Errs) == 0 {
		t.Fatal("expected validation error for unknown native tool")
	}
	found := false
	for _, e := range result.Errs {
		if strings.Contains(e.Error(), "native") && strings.Contains(e.Error(), "custom.native") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected native validation error, got %v", result.Errs)
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
