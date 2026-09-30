package proposal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreValidateValidProposal(t *testing.T) {
	proposalDir := t.TempDir()
	targetDir := t.TempDir()

	// Staged files for a valid task tree.
	filesDir := filepath.Join(proposalDir, "files")
	os.MkdirAll(filesDir, 0o755)
	os.WriteFile(filepath.Join(filesDir, "task.md"), []byte("---\n---\nRoot task"), 0o644)
	os.WriteFile(filepath.Join(filesDir, "agent.md"), []byte("model: anthropic/claude-sonnet-4-6"), 0o644)

	ops := []Operation{
		{Op: "write", Path: "task.md", Source: "files/task.md"},
		{Op: "write", Path: "agent.md", Source: "files/agent.md"},
	}

	result, err := PreValidate(targetDir, proposalDir, ops)
	if err != nil {
		t.Fatalf("PreValidate: %v", err)
	}
	if result.Status != "pass" {
		t.Errorf("expected status 'pass', got %q", result.Status)
		for _, e := range result.Errors {
			t.Logf("  error: %s", e)
		}
	}
}

func TestPreValidateInvalidProposal(t *testing.T) {
	proposalDir := t.TempDir()
	targetDir := t.TempDir()

	// Staged file: task.md without an agent.md at root (missing model).
	filesDir := filepath.Join(proposalDir, "files")
	os.MkdirAll(filesDir, 0o755)
	os.WriteFile(filepath.Join(filesDir, "task.md"), []byte("---\n---\nRoot task"), 0o644)

	ops := []Operation{
		{Op: "write", Path: "task.md", Source: "files/task.md"},
	}

	result, err := PreValidate(targetDir, proposalDir, ops)
	if err != nil {
		t.Fatalf("PreValidate: %v", err)
	}
	if result.Status != "fail" {
		t.Errorf("expected status 'fail', got %q", result.Status)
	}
	if len(result.Errors) == 0 {
		t.Error("expected validation errors")
	}
}

func TestPreValidateInvalidModelProposal(t *testing.T) {
	proposalDir := t.TempDir()
	targetDir := t.TempDir()

	filesDir := filepath.Join(proposalDir, "files")
	os.MkdirAll(filesDir, 0o755)
	os.WriteFile(filepath.Join(filesDir, "task.md"), []byte("---\n---\nRoot task"), 0o644)
	os.WriteFile(filepath.Join(filesDir, "agent.md"), []byte("model: claude-opus-4-5"), 0o644)

	ops := []Operation{
		{Op: "write", Path: "task.md", Source: "files/task.md"},
		{Op: "write", Path: "agent.md", Source: "files/agent.md"},
	}

	result, err := PreValidate(targetDir, proposalDir, ops)
	if err != nil {
		t.Fatalf("PreValidate: %v", err)
	}
	if result.Status != "fail" {
		t.Fatalf("expected status 'fail', got %q", result.Status)
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected validation errors")
	}
}

func TestPreValidateExistingTarget(t *testing.T) {
	proposalDir := t.TempDir()
	targetDir := t.TempDir()

	// Existing valid task tree in target.
	os.WriteFile(filepath.Join(targetDir, "task.md"), []byte("---\n---\nExisting root"), 0o644)
	os.WriteFile(filepath.Join(targetDir, "agent.md"), []byte("model: anthropic/claude-sonnet-4-6"), 0o644)

	// Proposal adds a subtask.
	filesDir := filepath.Join(proposalDir, "files")
	os.MkdirAll(filepath.Join(filesDir, "subtasks", "01-gather"), 0o755)
	os.WriteFile(filepath.Join(filesDir, "subtasks", "01-gather", "task.md"), []byte("---\n---\nGather data"), 0o644)

	ops := []Operation{
		{Op: "write", Path: "subtasks/01-gather/task.md", Source: "files/subtasks/01-gather/task.md"},
	}

	result, err := PreValidate(targetDir, proposalDir, ops)
	if err != nil {
		t.Fatalf("PreValidate: %v", err)
	}
	if result.Status != "pass" {
		t.Errorf("expected 'pass', got %q", result.Status)
		for _, e := range result.Errors {
			t.Logf("  error: %s", e)
		}
	}
}

func TestPreValidateDeleteOperation(t *testing.T) {
	proposalDir := t.TempDir()
	targetDir := t.TempDir()

	// Existing target with extra file.
	os.WriteFile(filepath.Join(targetDir, "task.md"), []byte("---\n---\nRoot"), 0o644)
	os.WriteFile(filepath.Join(targetDir, "agent.md"), []byte("model: anthropic/claude-sonnet-4-6"), 0o644)
	os.WriteFile(filepath.Join(targetDir, "notes.md"), []byte("notes"), 0o644)

	// Delete notes.md (target should still be valid without it).
	ops := []Operation{
		{Op: "delete", Path: "notes.md"},
	}

	result, err := PreValidate(targetDir, proposalDir, ops)
	if err != nil {
		t.Fatalf("PreValidate: %v", err)
	}
	if result.Status != "pass" {
		t.Errorf("expected 'pass', got %q", result.Status)
	}
}

func TestPreValidateOperationOrder(t *testing.T) {
	proposalDir := t.TempDir()
	targetDir := t.TempDir()

	// Write a file then delete it — file should be absent after replay.
	filesDir := filepath.Join(proposalDir, "files")
	os.MkdirAll(filesDir, 0o755)
	os.WriteFile(filepath.Join(filesDir, "temp.md"), []byte("temporary"), 0o644)
	// Also write a valid task tree.
	os.WriteFile(filepath.Join(filesDir, "task.md"), []byte("---\n---\nRoot"), 0o644)
	os.WriteFile(filepath.Join(filesDir, "agent.md"), []byte("model: anthropic/claude-sonnet-4-6"), 0o644)

	ops := []Operation{
		{Op: "write", Path: "task.md", Source: "files/task.md"},
		{Op: "write", Path: "agent.md", Source: "files/agent.md"},
		{Op: "write", Path: "temp.md", Source: "files/temp.md"},
		{Op: "delete", Path: "temp.md"},
	}

	result, err := PreValidate(targetDir, proposalDir, ops)
	if err != nil {
		t.Fatalf("PreValidate: %v", err)
	}
	if result.Status != "pass" {
		t.Errorf("expected 'pass', got %q", result.Status)
	}
}

func TestCopyDirExcludesProposals(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Create structure with .smith/proposals/ and .smith/lib/.
	os.MkdirAll(filepath.Join(src, ".smith", "proposals", "abc"), 0o755)
	os.WriteFile(filepath.Join(src, ".smith", "proposals", "abc", "manifest.json"), []byte("{}"), 0o644)
	os.MkdirAll(filepath.Join(src, ".smith", "lib", "planner"), 0o755)
	os.WriteFile(filepath.Join(src, ".smith", "lib", "planner", "task.md"), []byte("planner task"), 0o644)
	os.WriteFile(filepath.Join(src, "task.md"), []byte("root"), 0o644)
	os.MkdirAll(filepath.Join(src, "output"), 0o755)
	os.WriteFile(filepath.Join(src, "output", "result.md"), []byte("result"), 0o644)

	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	// task.md should be copied.
	if _, err := os.Stat(filepath.Join(dst, "task.md")); err != nil {
		t.Error("task.md should be copied")
	}

	// .smith/lib/ should be copied.
	if _, err := os.Stat(filepath.Join(dst, ".smith", "lib", "planner", "task.md")); err != nil {
		t.Error(".smith/lib/ should be copied for module resolution")
	}

	// .smith/proposals/ should NOT be copied.
	if _, err := os.Stat(filepath.Join(dst, ".smith", "proposals")); err == nil {
		t.Error(".smith/proposals/ should be excluded")
	}

	// output/ should NOT be copied.
	if _, err := os.Stat(filepath.Join(dst, "output")); err == nil {
		t.Error("output/ should be excluded")
	}
}

func TestCopyDirExcludesCache(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Create structure with .smith/cache/ and .smith/lib/.
	os.MkdirAll(filepath.Join(src, ".smith", "cache", "planner", "abc123"), 0o755)
	os.WriteFile(filepath.Join(src, ".smith", "cache", "planner", "abc123", "result.json"), []byte("{}"), 0o644)
	os.MkdirAll(filepath.Join(src, ".smith", "lib", "planner"), 0o755)
	os.WriteFile(filepath.Join(src, ".smith", "lib", "planner", "task.md"), []byte("planner task"), 0o644)
	os.WriteFile(filepath.Join(src, "task.md"), []byte("root"), 0o644)

	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	// task.md should be copied.
	if _, err := os.Stat(filepath.Join(dst, "task.md")); err != nil {
		t.Error("task.md should be copied")
	}

	// .smith/lib/ should be copied.
	if _, err := os.Stat(filepath.Join(dst, ".smith", "lib", "planner", "task.md")); err != nil {
		t.Error(".smith/lib/ should be copied for module resolution")
	}

	// .smith/cache/ should NOT be copied.
	if _, err := os.Stat(filepath.Join(dst, ".smith", "cache")); err == nil {
		t.Error(".smith/cache/ should be excluded")
	}
}
