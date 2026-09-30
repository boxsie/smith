package proposal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestGenerateID(t *testing.T) {
	id := GenerateID()
	// Format: YYYYMMDD-HHMMSS-<6 hex chars>
	pattern := `^\d{8}-\d{6}-[0-9a-f]{6}$`
	matched, err := regexp.MatchString(pattern, id)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Errorf("GenerateID() = %q, does not match pattern %s", id, pattern)
	}

	// IDs should be unique.
	id2 := GenerateID()
	if id == id2 {
		t.Errorf("GenerateID() produced duplicate IDs: %q", id)
	}
}

func TestProposalDir(t *testing.T) {
	got := ProposalDir("/project", "20260318-103000-abc123")
	want := filepath.Join("/project", ".smith", "proposals", "20260318-103000-abc123")
	if got != want {
		t.Errorf("ProposalDir() = %q, want %q", got, want)
	}
}

func TestFilesDir(t *testing.T) {
	got := FilesDir("/project/.smith/proposals/abc")
	want := filepath.Join("/project/.smith/proposals/abc", "files")
	if got != want {
		t.Errorf("FilesDir() = %q, want %q", got, want)
	}
}

func TestManifestRoundtrip(t *testing.T) {
	dir := t.TempDir()

	original := &Manifest{
		ID:        "20260318-103000-abc123",
		Goal:      "test goal",
		CreatedAt: "2026-03-18T10:30:00Z",
		Planner: PlannerInfo{
			Model:        "anthropic/claude-sonnet-4-6",
			Temperature:  0.2,
			ResolvedFrom: "~/.smith/lib/planner",
			ContentHash:  "sha256:abc123",
		},
		ObservedState: ObservedState{
			Hash:      "sha256:def456",
			TaskCount: 3,
			Empty:     false,
		},
		ModulesResolved: []ModuleRecord{},
		Operations: []Operation{
			{Op: "write", Path: "task.md", Source: "files/task.md"},
			{Op: "delete", Path: "old.md", BaseHash: "sha256:old123"},
		},
	}

	if err := WriteManifest(dir, original); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	loaded, err := ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	// Compare via JSON to avoid struct comparison issues.
	origJSON, _ := json.Marshal(original)
	loadedJSON, _ := json.Marshal(loaded)
	if string(origJSON) != string(loadedJSON) {
		t.Errorf("roundtrip mismatch:\n  original: %s\n  loaded:   %s", origJSON, loadedJSON)
	}
}

func TestManifestOptionalFields(t *testing.T) {
	dir := t.TempDir()

	tokensIn := 1000
	m := &Manifest{
		ID:              "test-id",
		Goal:            "test",
		CreatedAt:       "2026-03-18T10:30:00Z",
		Planner:         PlannerInfo{Model: "test", ContentHash: "sha256:abc"},
		ObservedState:   ObservedState{Hash: "sha256:def", TaskCount: 0, Empty: true},
		ModulesResolved: []ModuleRecord{},
		Operations:      []Operation{},
	}
	m.Planner.TokensIn = &tokensIn

	if err := WriteManifest(dir, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	// Read the raw JSON to check omitempty behavior.
	data, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var raw map[string]any
	json.Unmarshal(data, &raw)

	planner := raw["planner"].(map[string]any)
	if _, ok := planner["tokens_in"]; !ok {
		t.Error("expected tokens_in to be present")
	}
	if _, ok := planner["tokens_out"]; ok {
		t.Error("expected tokens_out to be omitted (nil)")
	}
	if _, ok := planner["cost_usd"]; ok {
		t.Error("expected cost_usd to be omitted (nil)")
	}
}

func TestSummaryRoundtrip(t *testing.T) {
	dir := t.TempDir()
	content := "This proposal creates a research pipeline."

	if err := WriteSummary(dir, content); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}

	got, err := ReadSummary(dir)
	if err != nil {
		t.Fatalf("ReadSummary: %v", err)
	}
	if got != content {
		t.Errorf("summary roundtrip: got %q, want %q", got, content)
	}
}

func TestListProposals(t *testing.T) {
	root := t.TempDir()
	proposalsDir := filepath.Join(root, ".smith", "proposals")
	os.MkdirAll(filepath.Join(proposalsDir, "20260318-103000-aaa"), 0o755)
	os.MkdirAll(filepath.Join(proposalsDir, "20260318-103001-bbb"), 0o755)
	// Non-directory should be ignored.
	os.WriteFile(filepath.Join(proposalsDir, "not-a-proposal"), []byte(""), 0o644)

	ids, err := ListProposals(root)
	if err != nil {
		t.Fatalf("ListProposals: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 proposals, got %d", len(ids))
	}
	if ids[0] != "20260318-103000-aaa" || ids[1] != "20260318-103001-bbb" {
		t.Errorf("unexpected proposal IDs: %v", ids)
	}
}

func TestListProposalsEmpty(t *testing.T) {
	root := t.TempDir()
	ids, err := ListProposals(root)
	if err != nil {
		t.Fatalf("ListProposals: %v", err)
	}
	if ids != nil {
		t.Errorf("expected nil for missing proposals dir, got %v", ids)
	}
}

func TestValidateManifestFiles(t *testing.T) {
	dir := t.TempDir()
	filesDir := filepath.Join(dir, "files")
	os.MkdirAll(filepath.Join(filesDir, "subtasks", "01-gather"), 0o755)
	os.WriteFile(filepath.Join(filesDir, "task.md"), []byte("task"), 0o644)
	os.WriteFile(filepath.Join(filesDir, "subtasks", "01-gather", "task.md"), []byte("subtask"), 0o644)

	ops := []Operation{
		{Op: "write", Path: "task.md", Source: "files/task.md"},
		{Op: "write", Path: "subtasks/01-gather/task.md", Source: "files/subtasks/01-gather/task.md"},
		{Op: "delete", Path: "old.md"}, // delete ops have no source
	}

	if err := ValidateManifestFiles(dir, ops); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidateManifestFilesMissing(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "files"), 0o755)

	ops := []Operation{
		{Op: "write", Path: "task.md", Source: "files/task.md"},
	}

	if err := ValidateManifestFiles(dir, ops); err == nil {
		t.Error("expected error for missing source file")
	}
}

func TestValidateOperationPaths(t *testing.T) {
	targetDir := t.TempDir()
	proposalDir := t.TempDir()
	filesDir := filepath.Join(proposalDir, "files")
	os.MkdirAll(filesDir, 0o755)
	os.WriteFile(filepath.Join(filesDir, "task.md"), []byte("task"), 0o644)

	// Valid operation.
	ops := []Operation{
		{Op: "write", Path: "task.md", Source: "files/task.md"},
	}
	if err := ValidateOperationPaths(targetDir, proposalDir, ops); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidateOperationPathsTraversal(t *testing.T) {
	targetDir := t.TempDir()
	proposalDir := t.TempDir()

	ops := []Operation{
		{Op: "write", Path: "../../../etc/passwd", Source: "files/task.md"},
	}
	if err := ValidateOperationPaths(targetDir, proposalDir, ops); err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestValidateOperationPathsSmith(t *testing.T) {
	targetDir := t.TempDir()
	proposalDir := t.TempDir()
	filesDir := filepath.Join(proposalDir, "files")
	os.MkdirAll(filesDir, 0o755)
	os.WriteFile(filepath.Join(filesDir, "task.md"), []byte("task"), 0o644)

	ops := []Operation{
		{Op: "write", Path: ".smith/lib/evil/task.md", Source: "files/task.md"},
	}
	if err := ValidateOperationPaths(targetDir, proposalDir, ops); err == nil {
		t.Error("expected error for .smith/ path")
	}
}

func TestValidateOperationPathsEmptyPath(t *testing.T) {
	ops := []Operation{
		{Op: "write", Path: "", Source: "files/task.md"},
	}
	if err := ValidateOperationPaths(t.TempDir(), t.TempDir(), ops); err == nil {
		t.Error("expected error for empty path")
	}
}

func TestValidateOperationPathsEmptySource(t *testing.T) {
	targetDir := t.TempDir()
	proposalDir := t.TempDir()

	ops := []Operation{
		{Op: "write", Path: "task.md", Source: ""},
	}
	if err := ValidateOperationPaths(targetDir, proposalDir, ops); err == nil {
		t.Error("expected error for empty source on write op")
	}
}

func TestValidateOperationPathsSourceEscapesFiles(t *testing.T) {
	targetDir := t.TempDir()
	proposalDir := t.TempDir()
	filesDir := filepath.Join(proposalDir, "files")
	os.MkdirAll(filesDir, 0o755)

	// Create manifest.json at proposal root (not in files/).
	os.WriteFile(filepath.Join(proposalDir, "manifest.json"), []byte("{}"), 0o644)

	tests := []struct {
		name   string
		source string
	}{
		{"manifest via traversal", "files/../manifest.json"},
		{"summary via traversal", "files/../summary.md"},
		{"bare manifest", "manifest.json"},
		{"source outside files prefix", "summary.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ops := []Operation{
				{Op: "write", Path: "task.md", Source: tt.source},
			}
			if err := ValidateOperationPaths(targetDir, proposalDir, ops); err == nil {
				t.Errorf("expected error for source %q escaping files/", tt.source)
			}
		})
	}
}

func TestValidateOperationPathsSourceInsideFiles(t *testing.T) {
	targetDir := t.TempDir()
	proposalDir := t.TempDir()
	filesDir := filepath.Join(proposalDir, "files")
	os.MkdirAll(filepath.Join(filesDir, "subtasks"), 0o755)
	os.WriteFile(filepath.Join(filesDir, "task.md"), []byte("task"), 0o644)
	os.WriteFile(filepath.Join(filesDir, "subtasks", "task.md"), []byte("sub"), 0o644)

	ops := []Operation{
		{Op: "write", Path: "task.md", Source: "files/task.md"},
		{Op: "write", Path: "subtasks/task.md", Source: "files/subtasks/task.md"},
	}
	if err := ValidateOperationPaths(targetDir, proposalDir, ops); err != nil {
		t.Errorf("expected no error for valid sources, got: %v", err)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()

	m := &Manifest{
		ID:              "test-load",
		Goal:            "test loading",
		CreatedAt:       "2026-03-18T10:30:00Z",
		Planner:         PlannerInfo{Model: "test", ContentHash: "sha256:abc"},
		ObservedState:   ObservedState{Hash: "sha256:def", TaskCount: 0, Empty: true},
		ModulesResolved: []ModuleRecord{},
		Operations:      []Operation{},
	}
	WriteManifest(dir, m)
	WriteSummary(dir, "Test summary")

	p, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Manifest.ID != "test-load" {
		t.Errorf("ID = %q, want %q", p.Manifest.ID, "test-load")
	}
	if p.Summary != "Test summary" {
		t.Errorf("Summary = %q, want %q", p.Summary, "Test summary")
	}
}
