package task

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverTree_Simple(t *testing.T) {
	root, err := DiscoverTree("testdata/t004-simple")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(root.Children) != 2 {
		t.Fatalf("children count = %d, want 2", len(root.Children))
	}
	if root.Children[0].ID != "01-child" {
		t.Errorf("child[0].ID = %q, want %q", root.Children[0].ID, "01-child")
	}
	if root.Children[1].ID != "02-child" {
		t.Errorf("child[1].ID = %q, want %q", root.Children[1].ID, "02-child")
	}
	if root.Children[0].Parent != root {
		t.Error("child[0].Parent should be root")
	}
}

func TestDiscoverTree_DeepNesting(t *testing.T) {
	root, err := DiscoverTree("testdata/t004-deep")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(root.Children) != 1 {
		t.Fatalf("root children = %d, want 1", len(root.Children))
	}
	child := root.Children[0]
	if child.ID != "a" {
		t.Errorf("child.ID = %q, want %q", child.ID, "a")
	}
	if len(child.Children) != 1 {
		t.Fatalf("grandchildren = %d, want 1", len(child.Children))
	}
	grandchild := child.Children[0]
	if grandchild.ID != "a/b" {
		t.Errorf("grandchild.ID = %q, want %q", grandchild.ID, "a/b")
	}
	if grandchild.Parent != child {
		t.Error("grandchild.Parent should be child")
	}
}

func TestDiscoverTree_NoTaskMDIgnored(t *testing.T) {
	root, err := DiscoverTree("testdata/t004-no-taskmd-child")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(root.Children) != 1 {
		t.Fatalf("children count = %d, want 1", len(root.Children))
	}
	if root.Children[0].ID != "has-task" {
		t.Errorf("child.ID = %q, want %q", root.Children[0].ID, "has-task")
	}
}

func TestDiscoverTree_ReservedWhen(t *testing.T) {
	_, err := DiscoverTree("testdata/t004-reserved-when")
	if !errors.Is(err, ErrReservedPath) {
		t.Errorf("err = %v, want ErrReservedPath", err)
	}
}

func TestDiscoverTree_ReservedLoop(t *testing.T) {
	_, err := DiscoverTree("testdata/t004-reserved-loop")
	if !errors.Is(err, ErrReservedPath) {
		t.Errorf("err = %v, want ErrReservedPath", err)
	}
}

func TestDiscoverTree_ModuleAndTaskMDCoexistence(t *testing.T) {
	_, err := DiscoverTree("testdata/t004-reserved-module")
	if !errors.Is(err, ErrModuleAndTaskMD) {
		t.Errorf("err = %v, want ErrModuleAndTaskMD", err)
	}
}

func TestDiscoverTree_ReservedDirectories(t *testing.T) {
	// Git does not preserve empty directories. Build the exact reserved shape
	// instead of relying on directories left in a long-lived working checkout.
	for _, name := range []string{"on-fail", "memory", "fixtures", "context/reference", "context/ephemeral", "context/inherited"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "task.md"), []byte("Root."), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := DiscoverTree(root); err != nil {
				t.Fatalf("unreserved tree: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(name)), 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := DiscoverTree(root); !errors.Is(err, ErrReservedPath) {
				t.Fatalf("reserved %s: got %v, want ErrReservedPath", name, err)
			}
		})
	}
}

func TestDiscoverTree_Symlink(t *testing.T) {
	tmp := t.TempDir()

	// Create a valid task tree with a symlink
	if err := os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a real file and a symlink to it
	realFile := filepath.Join(tmp, "real.txt")
	if err := os.WriteFile(realFile, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(tmp, "link.txt")
	if err := os.Symlink(realFile, linkPath); err != nil {
		t.Fatal(err)
	}

	_, err := DiscoverTree(tmp)
	if !errors.Is(err, ErrSymlink) {
		t.Errorf("err = %v, want ErrSymlink", err)
	}
}

func TestDiscoverTree_SymlinkNestedInContextStatic(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create context/static/ with a nested symlink
	staticDir := filepath.Join(tmp, "context", "static")
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		t.Fatal(err)
	}
	realFile := filepath.Join(staticDir, "real.txt")
	if err := os.WriteFile(realFile, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(staticDir, "link.txt")
	if err := os.Symlink(realFile, linkPath); err != nil {
		t.Fatal(err)
	}

	_, err := DiscoverTree(tmp)
	if !errors.Is(err, ErrSymlink) {
		t.Errorf("err = %v, want ErrSymlink for nested symlink in context/static/", err)
	}
}

func TestDiscoverTree_MisplacedTaskMD(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Place a task.md inside a non-subtasks directory
	miscDir := filepath.Join(tmp, "misc")
	if err := os.MkdirAll(miscDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(miscDir, "task.md"), []byte("Misplaced."), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := DiscoverTree(tmp)
	if !errors.Is(err, ErrMisplacedTask) {
		t.Errorf("err = %v, want ErrMisplacedTask", err)
	}
}

func TestDiscoverTree_MisplacedTaskMDDeeplyNested(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Place a task.md deep inside a non-subtasks path
	deepDir := filepath.Join(tmp, "misc", "nested", "deep")
	if err := os.MkdirAll(deepDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deepDir, "task.md"), []byte("Misplaced."), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := DiscoverTree(tmp)
	if !errors.Is(err, ErrMisplacedTask) {
		t.Errorf("err = %v, want ErrMisplacedTask", err)
	}
}

func TestDiscoverTree_SymlinkInsideOutput(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test"), 0644); err != nil {
		t.Fatal(err)
	}

	outputDir := filepath.Join(tmp, "output")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatal(err)
	}
	realFile := filepath.Join(outputDir, "real.txt")
	if err := os.WriteFile(realFile, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realFile, filepath.Join(outputDir, "link.txt")); err != nil {
		t.Fatal(err)
	}

	_, err := DiscoverTree(tmp)
	if !errors.Is(err, ErrSymlink) {
		t.Errorf("err = %v, want ErrSymlink for symlink inside output/", err)
	}
}

func TestDiscoverTree_MisplacedTaskMDInsideOutput(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test"), 0644); err != nil {
		t.Fatal(err)
	}

	outputDir := filepath.Join(tmp, "output")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "task.md"), []byte("Stray."), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := DiscoverTree(tmp)
	if !errors.Is(err, ErrMisplacedTask) {
		t.Errorf("err = %v, want ErrMisplacedTask for task.md inside output/", err)
	}
}

func TestDiscoverTree_SmithDirExcluded(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create .smith/ with task.md inside proposals — must not be discovered
	proposalDir := filepath.Join(tmp, ".smith", "proposals", "abc", "files")
	if err := os.MkdirAll(proposalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proposalDir, "task.md"), []byte("Proposal task."), 0644); err != nil {
		t.Fatal(err)
	}

	// Create .smith/lib/ with task.md — must not be discovered
	libDir := filepath.Join(tmp, ".smith", "lib", "planner")
	if err := os.MkdirAll(libDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "task.md"), []byte("Lib task."), 0644); err != nil {
		t.Fatal(err)
	}

	root, err := DiscoverTree(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v (should ignore .smith/)", err)
	}
	if len(root.Children) != 0 {
		t.Errorf("children = %d, want 0 (nothing from .smith/ should be discovered)", len(root.Children))
	}
}

func TestDiscoverTree_NestedSmithDirExcluded(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a subtask with a .smith/ dir inside it
	subtaskDir := filepath.Join(tmp, "subtasks", "01-child")
	if err := os.MkdirAll(subtaskDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subtaskDir, "task.md"), []byte("Child."), 0644); err != nil {
		t.Fatal(err)
	}

	// .smith/ inside the subtask dir with a task.md deep inside
	nestedSmith := filepath.Join(subtaskDir, ".smith", "nested")
	if err := os.MkdirAll(nestedSmith, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nestedSmith, "task.md"), []byte("Hidden."), 0644); err != nil {
		t.Fatal(err)
	}

	root, err := DiscoverTree(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v (should ignore nested .smith/)", err)
	}
	if len(root.Children) != 1 {
		t.Fatalf("children = %d, want 1", len(root.Children))
	}
	if root.Children[0].ID != "01-child" {
		t.Errorf("child.ID = %q, want %q", root.Children[0].ID, "01-child")
	}
}

func TestDiscoverTree_SmithDirUnderSubtasksExcluded(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create subtasks/.smith/ with task.md — must not be discovered as a task
	smithDir := filepath.Join(tmp, "subtasks", ".smith")
	if err := os.MkdirAll(smithDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(smithDir, "task.md"), []byte("Hidden."), 0644); err != nil {
		t.Fatal(err)
	}

	root, err := DiscoverTree(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v (should ignore .smith under subtasks/)", err)
	}
	if len(root.Children) != 0 {
		t.Errorf("children = %d, want 0 (.smith under subtasks/ should be skipped)", len(root.Children))
	}
}

// --- Module-aware discovery tests ---

// setupModuleTest creates a project with a module reference pointing to a lib module.
// Returns projectDir.
func setupModuleTest(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()

	// Create lib module at project-local .smith/lib/mymod/
	modDir := filepath.Join(tmp, ".smith", "lib", "mymod")
	os.MkdirAll(modDir, 0o755)
	os.WriteFile(filepath.Join(modDir, "task.md"), []byte("Do the module thing"), 0o644)

	return tmp
}

func TestDiscoverTree_ModuleChildRef(t *testing.T) {
	tmp := setupModuleTest(t)

	// Create root task.
	os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root task"), 0o644)
	os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test-model"), 0o644)

	// Create child with module.yaml.
	childDir := filepath.Join(tmp, "subtasks", "01-mod")
	os.MkdirAll(childDir, 0o755)
	os.WriteFile(filepath.Join(childDir, "module.yaml"), []byte("source: mymod\n"), 0o644)

	root, err := DiscoverTree(tmp)
	if err != nil {
		t.Fatalf("DiscoverTree: %v", err)
	}
	if len(root.Children) != 1 {
		t.Fatalf("children = %d, want 1", len(root.Children))
	}

	child := root.Children[0]
	if child.ModuleRef == nil {
		t.Fatal("child.ModuleRef is nil")
	}
	if child.ModuleRef.Source != "mymod" {
		t.Errorf("ModuleRef.Source = %q, want %q", child.ModuleRef.Source, "mymod")
	}
	if child.SourcePath == "" {
		t.Error("child.SourcePath is empty")
	}
	if child.Path != filepath.Join(tmp, "subtasks", "01-mod") {
		t.Errorf("child.Path = %q, want referencing dir", child.Path)
	}
	if child.Body != "Do the module thing" {
		t.Errorf("child.Body = %q, want module body", child.Body)
	}
}

func TestDiscoverTree_ModuleWithSubtasks(t *testing.T) {
	tmp := t.TempDir()

	// Create lib module with subtasks.
	modDir := filepath.Join(tmp, ".smith", "lib", "pipeline")
	os.MkdirAll(modDir, 0o755)
	os.WriteFile(filepath.Join(modDir, "task.md"), []byte("Pipeline root"), 0o644)

	subDir := filepath.Join(modDir, "subtasks", "01-step")
	os.MkdirAll(subDir, 0o755)
	os.WriteFile(filepath.Join(subDir, "task.md"), []byte("Step one"), 0o644)

	// Create project root referencing the module.
	os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Project root"), 0o644)
	os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test-model"), 0o644)

	childDir := filepath.Join(tmp, "subtasks", "01-pipe")
	os.MkdirAll(childDir, 0o755)
	os.WriteFile(filepath.Join(childDir, "module.yaml"), []byte("source: pipeline\n"), 0o644)

	root, err := DiscoverTree(tmp)
	if err != nil {
		t.Fatalf("DiscoverTree: %v", err)
	}
	if len(root.Children) != 1 {
		t.Fatalf("root children = %d, want 1", len(root.Children))
	}

	pipeTask := root.Children[0]
	if len(pipeTask.Children) != 1 {
		t.Fatalf("module children = %d, want 1", len(pipeTask.Children))
	}

	stepTask := pipeTask.Children[0]
	if stepTask.Body != "Step one" {
		t.Errorf("step body = %q, want %q", stepTask.Body, "Step one")
	}
	// Runtime path should be under the project, not the lib.
	wantPath := filepath.Join(tmp, "subtasks", "01-pipe", "subtasks", "01-step")
	if stepTask.Path != wantPath {
		t.Errorf("step.Path = %q, want %q", stepTask.Path, wantPath)
	}
	// Source path should be in the lib.
	if stepTask.SourcePath != filepath.Join(modDir, "subtasks", "01-step") {
		t.Errorf("step.SourcePath = %q, want lib path", stepTask.SourcePath)
	}
}

func TestDiscoverTree_ModuleOverrideAgent(t *testing.T) {
	tmp := setupModuleTest(t)

	// Create root.
	os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root task"), 0o644)
	os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test-model"), 0o644)

	// Create child with module.yaml + local agent.md override.
	childDir := filepath.Join(tmp, "subtasks", "01-mod")
	os.MkdirAll(childDir, 0o755)
	os.WriteFile(filepath.Join(childDir, "module.yaml"), []byte("source: mymod\n"), 0o644)
	os.WriteFile(filepath.Join(childDir, "agent.md"), []byte("model: override-model"), 0o644)

	root, err := DiscoverTree(tmp)
	if err != nil {
		t.Fatalf("DiscoverTree: %v", err)
	}

	child := root.Children[0]
	if child.Agent == nil || child.Agent.Model != "override-model" {
		t.Errorf("agent.Model = %v, want override-model", child.Agent)
	}
}

func TestDiscoverTree_ModuleUserSubtasksError(t *testing.T) {
	tmp := setupModuleTest(t)

	os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root task"), 0o644)
	os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test-model"), 0o644)

	// Create child with module.yaml + user-authored subtasks.
	childDir := filepath.Join(tmp, "subtasks", "01-mod")
	os.MkdirAll(childDir, 0o755)
	os.WriteFile(filepath.Join(childDir, "module.yaml"), []byte("source: mymod\n"), 0o644)

	userSubDir := filepath.Join(childDir, "subtasks", "user-task")
	os.MkdirAll(userSubDir, 0o755)
	os.WriteFile(filepath.Join(userSubDir, "task.md"), []byte("user task"), 0o644)

	_, err := DiscoverTree(tmp)
	if !errors.Is(err, ErrModuleUserSubtasks) {
		t.Errorf("err = %v, want ErrModuleUserSubtasks", err)
	}
}

func TestDiscoverTree_ModuleRunnerManagedSubtasksOK(t *testing.T) {
	tmp := setupModuleTest(t)

	os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root task"), 0o644)
	os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test-model"), 0o644)

	// Create child with module.yaml + runner-managed subtasks (only output/).
	childDir := filepath.Join(tmp, "subtasks", "01-mod")
	os.MkdirAll(childDir, 0o755)
	os.WriteFile(filepath.Join(childDir, "module.yaml"), []byte("source: mymod\n"), 0o644)

	outputDir := filepath.Join(childDir, "subtasks", "01-step", "output")
	os.MkdirAll(outputDir, 0o755)
	os.WriteFile(filepath.Join(outputDir, "result.md"), []byte("cached result"), 0o644)

	_, err := DiscoverTree(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v (runner-managed subtasks should be allowed)", err)
	}
}

func TestDiscoverTree_ModuleRootLevel(t *testing.T) {
	tmp := t.TempDir()

	// Create lib module.
	modDir := filepath.Join(tmp, ".smith", "lib", "rootmod")
	os.MkdirAll(modDir, 0o755)
	os.WriteFile(filepath.Join(modDir, "task.md"), []byte("Module root task"), 0o644)
	os.WriteFile(filepath.Join(modDir, "agent.md"), []byte("model: test-model"), 0o644)

	// Root has only module.yaml, no task.md.
	os.WriteFile(filepath.Join(tmp, "module.yaml"), []byte("source: rootmod\n"), 0o644)

	root, err := DiscoverTree(tmp)
	if err != nil {
		t.Fatalf("DiscoverTree: %v", err)
	}
	if root.Body != "Module root task" {
		t.Errorf("root.Body = %q, want module body", root.Body)
	}
	if root.SourcePath == "" {
		t.Error("root.SourcePath is empty, should be set for module")
	}
}

func TestDiscoverTree_ModulePathTraversal(t *testing.T) {
	tmp := t.TempDir()

	os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root task"), 0o644)
	os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test-model"), 0o644)

	childDir := filepath.Join(tmp, "subtasks", "01-bad")
	os.MkdirAll(childDir, 0o755)
	os.WriteFile(filepath.Join(childDir, "module.yaml"), []byte("source: ../../../etc\n"), 0o644)

	_, err := DiscoverTree(tmp)
	if err == nil {
		t.Fatal("expected error for path traversal in module source")
	}
}

func TestDiscoverTree_NestedModuleRef(t *testing.T) {
	tmp := t.TempDir()

	// Create outer module with a subtask that is itself a module reference.
	outerDir := filepath.Join(tmp, ".smith", "lib", "outer")
	os.MkdirAll(outerDir, 0o755)
	os.WriteFile(filepath.Join(outerDir, "task.md"), []byte("Outer module"), 0o644)

	nestedSubDir := filepath.Join(outerDir, "subtasks", "01-inner")
	os.MkdirAll(nestedSubDir, 0o755)
	os.WriteFile(filepath.Join(nestedSubDir, "module.yaml"), []byte("source: inner\n"), 0o644)

	// Create inner module.
	innerDir := filepath.Join(tmp, ".smith", "lib", "inner")
	os.MkdirAll(innerDir, 0o755)
	os.WriteFile(filepath.Join(innerDir, "task.md"), []byte("Inner module"), 0o644)

	// Create project referencing outer.
	os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root task"), 0o644)
	os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test-model"), 0o644)

	childDir := filepath.Join(tmp, "subtasks", "01-outer")
	os.MkdirAll(childDir, 0o755)
	os.WriteFile(filepath.Join(childDir, "module.yaml"), []byte("source: outer\n"), 0o644)

	root, err := DiscoverTree(tmp)
	if err != nil {
		t.Fatalf("DiscoverTree: %v", err)
	}

	outerTask := root.Children[0]
	if len(outerTask.Children) != 1 {
		t.Fatalf("outer children = %d, want 1", len(outerTask.Children))
	}
	innerTask := outerTask.Children[0]
	if innerTask.Body != "Inner module" {
		t.Errorf("inner body = %q, want %q", innerTask.Body, "Inner module")
	}
	if innerTask.ModuleRef == nil || innerTask.ModuleRef.Source != "inner" {
		t.Error("inner should be a module reference to 'inner'")
	}
}

func TestDiscoverTree_LibReservedPathRejected(t *testing.T) {
	tmp := t.TempDir()

	// Create lib module with a reserved file (when.md).
	modDir := filepath.Join(tmp, ".smith", "lib", "badmod")
	os.MkdirAll(modDir, 0o755)
	os.WriteFile(filepath.Join(modDir, "task.md"), []byte("Bad module"), 0o644)
	os.WriteFile(filepath.Join(modDir, "when.md"), []byte("trigger"), 0o644)

	os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root task"), 0o644)
	os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test-model"), 0o644)

	childDir := filepath.Join(tmp, "subtasks", "01-bad")
	os.MkdirAll(childDir, 0o755)
	os.WriteFile(filepath.Join(childDir, "module.yaml"), []byte("source: badmod\n"), 0o644)

	_, err := DiscoverTree(tmp)
	if !errors.Is(err, ErrReservedPath) {
		t.Errorf("err = %v, want ErrReservedPath for lib module with when.md", err)
	}
}

func TestDiscoverTree_ModuleUnresolvable(t *testing.T) {
	tmp := t.TempDir()

	os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root task"), 0o644)
	os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test-model"), 0o644)

	childDir := filepath.Join(tmp, "subtasks", "01-bad")
	os.MkdirAll(childDir, 0o755)
	os.WriteFile(filepath.Join(childDir, "module.yaml"), []byte("source: nonexistent\n"), 0o644)

	_, err := DiscoverTree(tmp)
	if err == nil {
		t.Fatal("expected error for unresolvable module")
	}
}

func TestDiscoverTree_ModuleStaticContextMerge(t *testing.T) {
	tmp := t.TempDir()

	// Create lib module with context/static/.
	modDir := filepath.Join(tmp, ".smith", "lib", "ctxmod")
	os.MkdirAll(filepath.Join(modDir, "context", "static"), 0o755)
	os.WriteFile(filepath.Join(modDir, "task.md"), []byte("Module task"), 0o644)
	os.WriteFile(filepath.Join(modDir, "context", "static", "a.md"), []byte("module context a"), 0o644)

	// Create root.
	os.WriteFile(filepath.Join(tmp, "task.md"), []byte("Root task"), 0o644)
	os.WriteFile(filepath.Join(tmp, "agent.md"), []byte("model: test-model"), 0o644)

	// Create child with module.yaml + local context/static/.
	childDir := filepath.Join(tmp, "subtasks", "01-ctx")
	os.MkdirAll(filepath.Join(childDir, "context", "static"), 0o755)
	os.WriteFile(filepath.Join(childDir, "module.yaml"), []byte("source: ctxmod\n"), 0o644)
	os.WriteFile(filepath.Join(childDir, "context", "static", "b.md"), []byte("local context b"), 0o644)

	root, err := DiscoverTree(tmp)
	if err != nil {
		t.Fatalf("DiscoverTree: %v", err)
	}

	child := root.Children[0]
	if len(child.StaticContext) != 2 {
		t.Fatalf("StaticContext len = %d, want 2", len(child.StaticContext))
	}
	// Module files first, then local.
	if child.StaticContext[0].RelPath != "a.md" {
		t.Errorf("first context file = %q, want a.md (from module)", child.StaticContext[0].RelPath)
	}
	if child.StaticContext[1].RelPath != "b.md" {
		t.Errorf("second context file = %q, want b.md (local override)", child.StaticContext[1].RelPath)
	}
}

// Integration test: DiscoverTree + ResolveAgentInheritance + ValidateTree pipeline
func TestFullLoadingPipeline(t *testing.T) {
	root, err := DiscoverTree("testdata/t004-deep")
	if err != nil {
		t.Fatalf("DiscoverTree: %v", err)
	}

	if err := ResolveAgentInheritance(root); err != nil {
		t.Fatalf("ResolveAgentInheritance: %v", err)
	}

	errs := ValidateTree(root)
	if len(errs) != 0 {
		t.Fatalf("ValidateTree: %v", errs)
	}

	// Verify the full pipeline produced correct results
	if root.EffectiveAgent.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("root model = %q", root.EffectiveAgent.Model)
	}
	child := root.Children[0]
	if child.EffectiveAgent.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("child inherited model = %q", child.EffectiveAgent.Model)
	}
	grandchild := child.Children[0]
	if grandchild.EffectiveAgent.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("grandchild inherited model = %q", grandchild.EffectiveAgent.Model)
	}
	if *grandchild.EffectiveAgent.Temperature != 0.2 {
		t.Errorf("grandchild temp = %v, want 0.2 default", *grandchild.EffectiveAgent.Temperature)
	}

	// Phase 2: BuildGraph + ValidateInputTypes
	g, err := BuildGraph(root)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}

	// root → a → a/b = 3 levels
	if len(g.Plan) != 3 {
		t.Errorf("execution levels = %d, want 3", len(g.Plan))
	}

	errs = ValidateInputTypes(root, g)
	if len(errs) != 0 {
		t.Fatalf("ValidateInputTypes: %v", errs)
	}
}
