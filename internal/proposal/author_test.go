package proposal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// setupAuthorTest creates a minimal valid task tree in targetDir and staged
// proposal files in proposalDir, returning both paths.
func setupAuthorTest(t *testing.T) (proposalDir, targetDir string) {
	t.Helper()

	targetDir = t.TempDir()
	proposalDir = t.TempDir()

	// Create staged files in the proposal.
	filesDir := filepath.Join(proposalDir, "files")
	os.MkdirAll(filesDir, 0o755)
	os.WriteFile(filepath.Join(filesDir, "task.md"), []byte("---\n---\nRoot task"), 0o644)
	os.WriteFile(filepath.Join(filesDir, "agent.md"), []byte("model: anthropic/claude-sonnet-4-6"), 0o644)

	return proposalDir, targetDir
}

func TestAuthorNewProject(t *testing.T) {
	proposalDir, targetDir := setupAuthorTest(t)

	input := AuthorInput{
		ProposalDir: proposalDir,
		PlannerOutput: PlannerOutput{
			Summary: "Creates a simple task tree",
			Operations: []PlannerOperation{
				{Op: "write", Path: "task.md"},
				{Op: "write", Path: "agent.md"},
			},
		},
		Provenance: ProvenanceInput{
			Goal:        "test",
			Model:       "test-model",
			ContentHash: "sha256:abc",
			ObservedState: ObservedState{
				Hash:  "sha256:def",
				Empty: true,
			},
		},
		TargetDir: targetDir,
	}

	p, err := Author(input)
	if err != nil {
		t.Fatalf("Author: %v", err)
	}

	if p.Manifest.ID != filepath.Base(proposalDir) {
		t.Errorf("ID = %q, want %q", p.Manifest.ID, filepath.Base(proposalDir))
	}
	if p.Summary != "Creates a simple task tree" {
		t.Errorf("Summary = %q", p.Summary)
	}
	if len(p.Manifest.Operations) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(p.Manifest.Operations))
	}

	// Source should have files/ prefix.
	if p.Manifest.Operations[0].Source != "files/task.md" {
		t.Errorf("Source = %q, want %q", p.Manifest.Operations[0].Source, "files/task.md")
	}

	// New files should have no base_hash.
	for _, op := range p.Manifest.Operations {
		if op.BaseHash != "" {
			t.Errorf("new file %q should not have base_hash, got %q", op.Path, op.BaseHash)
		}
	}

	// Manifest should be written to disk.
	if _, err := ReadManifest(proposalDir); err != nil {
		t.Errorf("manifest not written: %v", err)
	}
}

func TestAuthorExistingProject(t *testing.T) {
	proposalDir, targetDir := setupAuthorTest(t)

	// Create an existing file in the target.
	os.WriteFile(filepath.Join(targetDir, "task.md"), []byte("old content"), 0o644)
	existingHash, _ := HashFile(filepath.Join(targetDir, "task.md"))

	input := AuthorInput{
		ProposalDir: proposalDir,
		PlannerOutput: PlannerOutput{
			Summary: "Updates the root task",
			Operations: []PlannerOperation{
				{Op: "write", Path: "task.md"},
			},
		},
		Provenance: ProvenanceInput{
			Goal:        "update",
			Model:       "test-model",
			ContentHash: "sha256:abc",
			ObservedState: ObservedState{
				Hash:      "sha256:def",
				TaskCount: 1,
			},
		},
		TargetDir: targetDir,
	}

	p, err := Author(input)
	if err != nil {
		t.Fatalf("Author: %v", err)
	}

	// Existing file should have base_hash.
	if p.Manifest.Operations[0].BaseHash != existingHash {
		t.Errorf("BaseHash = %q, want %q", p.Manifest.Operations[0].BaseHash, existingHash)
	}
}

func TestAuthorMissingStagedFile(t *testing.T) {
	proposalDir := t.TempDir()
	targetDir := t.TempDir()
	os.MkdirAll(filepath.Join(proposalDir, "files"), 0o755)
	// Don't create the staged file.

	input := AuthorInput{
		ProposalDir: proposalDir,
		PlannerOutput: PlannerOutput{
			Summary: "test",
			Operations: []PlannerOperation{
				{Op: "write", Path: "task.md"},
			},
		},
		Provenance: ProvenanceInput{
			Goal:        "test",
			Model:       "test",
			ContentHash: "sha256:abc",
			ObservedState: ObservedState{
				Hash:  "sha256:def",
				Empty: true,
			},
		},
		TargetDir: targetDir,
	}

	_, err := Author(input)
	if err == nil {
		t.Error("expected error for missing staged file")
	}
}

func TestPlannerOperationContentJSONRoundTrip(t *testing.T) {
	op := PlannerOperation{
		Op:      "write",
		Path:    "task.md",
		Content: "---\n---\nHello world",
	}
	data, err := json.Marshal(op)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded PlannerOperation
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Content != op.Content {
		t.Errorf("Content = %q, want %q", decoded.Content, op.Content)
	}
}

func TestPlannerOperationEmptyContentOmitted(t *testing.T) {
	op := PlannerOperation{
		Op:   "write",
		Path: "task.md",
	}
	data, err := json.Marshal(op)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	if _, exists := raw["content"]; exists {
		t.Error("empty Content should be omitted from JSON")
	}
}

func TestPlannerOutputBackwardCompat(t *testing.T) {
	// Legacy planner output without Content field.
	legacyJSON := `{"summary":"test","operations":[{"op":"write","path":"task.md"}]}`
	var out PlannerOutput
	if err := json.Unmarshal([]byte(legacyJSON), &out); err != nil {
		t.Fatalf("unmarshal legacy output: %v", err)
	}
	if out.Operations[0].Content != "" {
		t.Errorf("Content should be empty for legacy output, got %q", out.Operations[0].Content)
	}
}

func TestDraftArtifactWithContent(t *testing.T) {
	artifact := `{
		"summary": "A meeting brief app",
		"operations": [
			{"op": "write", "path": "task.md", "content": "Root task body"},
			{"op": "write", "path": "agent.md", "content": "model: anthropic/claude-sonnet-4-6"},
			{"op": "delete", "path": "old.md"}
		]
	}`
	var out PlannerOutput
	if err := json.Unmarshal([]byte(artifact), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Summary != "A meeting brief app" {
		t.Errorf("Summary = %q", out.Summary)
	}
	if len(out.Operations) != 3 {
		t.Fatalf("expected 3 operations, got %d", len(out.Operations))
	}
	if out.Operations[0].Content != "Root task body" {
		t.Errorf("op 0 Content = %q", out.Operations[0].Content)
	}
	if out.Operations[1].Content != "model: anthropic/claude-sonnet-4-6" {
		t.Errorf("op 1 Content = %q", out.Operations[1].Content)
	}
	if out.Operations[2].Content != "" {
		t.Errorf("delete op Content should be empty, got %q", out.Operations[2].Content)
	}
}

func TestAuthorDeleteOperation(t *testing.T) {
	proposalDir := t.TempDir()
	targetDir := t.TempDir()
	os.MkdirAll(filepath.Join(proposalDir, "files"), 0o755)

	// Create valid task tree files so pre-validation doesn't fail.
	// The target has a file we want to delete plus valid task structure.
	os.WriteFile(filepath.Join(targetDir, "task.md"), []byte("---\n---\nRoot"), 0o644)
	os.WriteFile(filepath.Join(targetDir, "agent.md"), []byte("model: anthropic/claude-sonnet-4-6"), 0o644)
	os.WriteFile(filepath.Join(targetDir, "old-notes.md"), []byte("old notes"), 0o644)

	// We need staged files for the writes that maintain the valid tree.
	filesDir := filepath.Join(proposalDir, "files")
	os.WriteFile(filepath.Join(filesDir, "task.md"), []byte("---\n---\nRoot"), 0o644)
	os.WriteFile(filepath.Join(filesDir, "agent.md"), []byte("model: anthropic/claude-sonnet-4-6"), 0o644)

	existingHash, _ := HashFile(filepath.Join(targetDir, "old-notes.md"))

	input := AuthorInput{
		ProposalDir: proposalDir,
		PlannerOutput: PlannerOutput{
			Summary: "Removes old notes",
			Operations: []PlannerOperation{
				{Op: "write", Path: "task.md"},
				{Op: "write", Path: "agent.md"},
				{Op: "delete", Path: "old-notes.md"},
			},
		},
		Provenance: ProvenanceInput{
			Goal:        "clean up",
			Model:       "test",
			ContentHash: "sha256:abc",
			ObservedState: ObservedState{
				Hash:      "sha256:def",
				TaskCount: 1,
			},
		},
		TargetDir: targetDir,
	}

	p, err := Author(input)
	if err != nil {
		t.Fatalf("Author: %v", err)
	}

	// Find the delete operation.
	var deleteOp *Operation
	for i := range p.Manifest.Operations {
		if p.Manifest.Operations[i].Op == "delete" {
			deleteOp = &p.Manifest.Operations[i]
			break
		}
	}
	if deleteOp == nil {
		t.Fatal("expected a delete operation")
	}
	if deleteOp.BaseHash != existingHash {
		t.Errorf("delete BaseHash = %q, want %q", deleteOp.BaseHash, existingHash)
	}
	if deleteOp.Source != "" {
		t.Errorf("delete Source should be empty, got %q", deleteOp.Source)
	}
}
