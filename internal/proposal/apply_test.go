package proposal

import (
	"os"
	"path/filepath"
	"testing"
)

// setupApplyTest creates a proposal with manifest.json and staged files,
// plus a target directory. Returns (proposalDir, targetDir).
func setupApplyTest(t *testing.T) (string, string) {
	t.Helper()

	targetDir := t.TempDir()
	projectRoot := t.TempDir()

	// Create .smith/proposals/<id>/ structure.
	proposalID := "20260318-103000-abc123"
	proposalDir := filepath.Join(projectRoot, ".smith", "proposals", proposalID)
	filesDir := filepath.Join(proposalDir, "files")
	os.MkdirAll(filesDir, 0o755)

	// Staged files.
	os.WriteFile(filepath.Join(filesDir, "task.md"), []byte("---\n---\nApplied task"), 0o644)
	os.WriteFile(filepath.Join(filesDir, "agent.md"), []byte("model: anthropic/claude-sonnet-4-6"), 0o644)

	// Write manifest.
	m := &Manifest{
		ID:              proposalID,
		Goal:            "test",
		CreatedAt:       "2026-03-18T10:30:00Z",
		Planner:         PlannerInfo{Model: "test", ContentHash: "sha256:abc"},
		ObservedState:   ObservedState{Hash: "sha256:def", Empty: true},
		ModulesResolved: []ModuleRecord{},
		Operations: []Operation{
			{Op: "write", Path: "task.md", Source: "files/task.md"},
			{Op: "write", Path: "agent.md", Source: "files/agent.md"},
		},
	}
	WriteManifest(proposalDir, m)
	WriteSummary(proposalDir, "Test proposal")

	return proposalDir, targetDir
}

func TestApplyWritesFiles(t *testing.T) {
	proposalDir, targetDir := setupApplyTest(t)

	result, err := Apply(ApplyInput{
		ProposalDir: proposalDir,
		TargetDir:   targetDir,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(result.Applied) != 2 {
		t.Errorf("expected 2 applied operations, got %d", len(result.Applied))
	}

	// Verify files were written.
	data, err := os.ReadFile(filepath.Join(targetDir, "task.md"))
	if err != nil {
		t.Fatalf("read applied task.md: %v", err)
	}
	if string(data) != "---\n---\nApplied task" {
		t.Errorf("task.md content = %q", string(data))
	}

	// Validation should pass.
	if result.Validation == nil || result.Validation.Status != "pass" {
		t.Errorf("expected validation pass, got %v", result.Validation)
	}
}

func TestApplyDryRun(t *testing.T) {
	proposalDir, targetDir := setupApplyTest(t)

	result, err := Apply(ApplyInput{
		ProposalDir: proposalDir,
		TargetDir:   targetDir,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(result.Applied) != 2 {
		t.Errorf("expected 2 operations reported, got %d", len(result.Applied))
	}

	// Files should NOT be written.
	if _, err := os.Stat(filepath.Join(targetDir, "task.md")); err == nil {
		t.Error("dry run should not write files")
	}
}

func TestApplyConflictDetection(t *testing.T) {
	proposalDir, targetDir := setupApplyTest(t)

	// Create an existing file with different content.
	os.WriteFile(filepath.Join(targetDir, "task.md"), []byte("existing"), 0o644)
	existingHash, _ := HashFile(filepath.Join(targetDir, "task.md"))

	// Update manifest with base_hash.
	m, _ := ReadManifest(proposalDir)
	m.Operations[0].BaseHash = "sha256:different-from-existing"
	WriteManifest(proposalDir, m)

	result, err := Apply(ApplyInput{
		ProposalDir: proposalDir,
		TargetDir:   targetDir,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(result.Conflicts) == 0 {
		t.Fatal("expected conflicts")
	}
	if result.Conflicts[0].ActualHash != existingHash {
		t.Errorf("ActualHash = %q, want %q", result.Conflicts[0].ActualHash, existingHash)
	}

	// Files should NOT be written when conflicts exist.
	data, _ := os.ReadFile(filepath.Join(targetDir, "task.md"))
	if string(data) != "existing" {
		t.Error("file should not be modified when conflicts detected")
	}
}

func TestApplyForceSkipsConflicts(t *testing.T) {
	proposalDir, targetDir := setupApplyTest(t)

	// Create conflict.
	os.WriteFile(filepath.Join(targetDir, "task.md"), []byte("existing"), 0o644)
	m, _ := ReadManifest(proposalDir)
	m.Operations[0].BaseHash = "sha256:different"
	WriteManifest(proposalDir, m)

	result, err := Apply(ApplyInput{
		ProposalDir: proposalDir,
		TargetDir:   targetDir,
		Force:       true,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(result.Conflicts) != 0 {
		t.Error("force should skip conflict detection")
	}

	// File should be overwritten.
	data, _ := os.ReadFile(filepath.Join(targetDir, "task.md"))
	if string(data) != "---\n---\nApplied task" {
		t.Errorf("file not overwritten with force: %q", string(data))
	}
}

func TestApplyDeleteOperation(t *testing.T) {
	projectRoot := t.TempDir()
	targetDir := t.TempDir()

	// Create target with files.
	os.WriteFile(filepath.Join(targetDir, "task.md"), []byte("---\n---\nRoot"), 0o644)
	os.WriteFile(filepath.Join(targetDir, "agent.md"), []byte("model: anthropic/claude-sonnet-4-6"), 0o644)
	os.WriteFile(filepath.Join(targetDir, "notes.md"), []byte("notes"), 0o644)

	proposalDir := filepath.Join(projectRoot, ".smith", "proposals", "del-test")
	os.MkdirAll(proposalDir, 0o755)

	m := &Manifest{
		ID:              "del-test",
		Goal:            "delete notes",
		CreatedAt:       "2026-03-18T10:30:00Z",
		Planner:         PlannerInfo{Model: "test", ContentHash: "sha256:abc"},
		ObservedState:   ObservedState{Hash: "sha256:def"},
		ModulesResolved: []ModuleRecord{},
		Operations: []Operation{
			{Op: "delete", Path: "notes.md"},
		},
	}
	WriteManifest(proposalDir, m)

	result, err := Apply(ApplyInput{
		ProposalDir: proposalDir,
		TargetDir:   targetDir,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// File should be removed.
	if _, err := os.Stat(filepath.Join(targetDir, "notes.md")); !os.IsNotExist(err) {
		t.Error("expected notes.md to be deleted")
	}

	// Should still have applied operations.
	if len(result.Applied) != 1 {
		t.Errorf("expected 1 applied operation, got %d", len(result.Applied))
	}
}

func TestApplyPathTraversalRejected(t *testing.T) {
	projectRoot := t.TempDir()
	targetDir := t.TempDir()

	proposalDir := filepath.Join(projectRoot, ".smith", "proposals", "evil")
	filesDir := filepath.Join(proposalDir, "files")
	os.MkdirAll(filesDir, 0o755)
	os.WriteFile(filepath.Join(filesDir, "task.md"), []byte("evil"), 0o644)

	m := &Manifest{
		ID:              "evil",
		Goal:            "escape",
		CreatedAt:       "2026-03-18T10:30:00Z",
		Planner:         PlannerInfo{Model: "test", ContentHash: "sha256:abc"},
		ObservedState:   ObservedState{Hash: "sha256:def"},
		ModulesResolved: []ModuleRecord{},
		Operations: []Operation{
			{Op: "write", Path: "../../../etc/evil", Source: "files/task.md"},
		},
	}
	WriteManifest(proposalDir, m)

	_, err := Apply(ApplyInput{
		ProposalDir: proposalDir,
		TargetDir:   targetDir,
	})
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestApplySmithPathRejected(t *testing.T) {
	projectRoot := t.TempDir()
	targetDir := t.TempDir()

	proposalDir := filepath.Join(projectRoot, ".smith", "proposals", "evil2")
	filesDir := filepath.Join(proposalDir, "files")
	os.MkdirAll(filesDir, 0o755)
	os.WriteFile(filepath.Join(filesDir, "task.md"), []byte("evil"), 0o644)

	m := &Manifest{
		ID:              "evil2",
		Goal:            "escape",
		CreatedAt:       "2026-03-18T10:30:00Z",
		Planner:         PlannerInfo{Model: "test", ContentHash: "sha256:abc"},
		ObservedState:   ObservedState{Hash: "sha256:def"},
		ModulesResolved: []ModuleRecord{},
		Operations: []Operation{
			{Op: "write", Path: ".smith/lib/evil/task.md", Source: "files/task.md"},
		},
	}
	WriteManifest(proposalDir, m)

	_, err := Apply(ApplyInput{
		ProposalDir: proposalDir,
		TargetDir:   targetDir,
	})
	if err == nil {
		t.Error("expected error for .smith/ path")
	}
}

func TestFindApplyTarget(t *testing.T) {
	root := t.TempDir()
	proposalDir := filepath.Join(root, ".smith", "proposals", "abc")
	os.MkdirAll(proposalDir, 0o755)

	target, err := FindApplyTarget(proposalDir)
	if err != nil {
		t.Fatalf("FindApplyTarget: %v", err)
	}

	absRoot, _ := filepath.Abs(root)
	if target != absRoot {
		t.Errorf("target = %q, want %q", target, absRoot)
	}
}

func TestFindApplyTargetNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := FindApplyTarget(dir)
	if err == nil {
		t.Error("expected error when no .smith/ found")
	}
}
