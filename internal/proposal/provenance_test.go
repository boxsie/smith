package proposal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildManifest(t *testing.T) {
	input := ProvenanceInput{
		Goal:         "test goal",
		Model:        "anthropic/claude-sonnet-4-6",
		Temperature:  0.2,
		ResolvedFrom: "~/.smith/lib/planner",
		ContentHash:  "sha256:abc",
		ObservedState: ObservedState{
			Hash:      "sha256:def",
			TaskCount: 3,
			Empty:     false,
		},
		ModulesResolved: []ModuleRecord{
			{Name: "summarize", ResolvedFrom: "~/.smith/lib/summarize", ContentHash: "sha256:ghi"},
		},
		TokensIn:   1000,
		TokensOut:  500,
		CostUSD:    0.05,
		DurationMS: 3000,
	}

	ops := []Operation{
		{Op: "write", Path: "task.md", Source: "files/task.md"},
	}
	validation := &ValidationResult{Status: "pass", ValidatedAt: "2026-03-18T10:30:05Z"}

	m := BuildManifest("test-id", input, ops, validation)

	if m.ID != "test-id" {
		t.Errorf("ID = %q, want %q", m.ID, "test-id")
	}
	if m.Goal != "test goal" {
		t.Errorf("Goal = %q, want %q", m.Goal, "test goal")
	}
	if m.CreatedAt == "" {
		t.Error("CreatedAt should be set")
	}
	if m.Planner.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("Planner.Model = %q", m.Planner.Model)
	}
	if m.Planner.TokensIn == nil || *m.Planner.TokensIn != 1000 {
		t.Error("expected TokensIn = 1000")
	}
	if m.Planner.TokensOut == nil || *m.Planner.TokensOut != 500 {
		t.Error("expected TokensOut = 500")
	}
	if m.Planner.CostUSD == nil || *m.Planner.CostUSD != 0.05 {
		t.Error("expected CostUSD = 0.05")
	}
	if m.Planner.DurationMS == nil || *m.Planner.DurationMS != 3000 {
		t.Error("expected DurationMS = 3000")
	}
	if len(m.Operations) != 1 {
		t.Errorf("expected 1 operation, got %d", len(m.Operations))
	}
	if m.Validation == nil || m.Validation.Status != "pass" {
		t.Error("expected validation status 'pass'")
	}
	if len(m.ModulesResolved) != 1 {
		t.Errorf("expected 1 module, got %d", len(m.ModulesResolved))
	}
}

func TestBuildManifestOptionalFieldsOmitted(t *testing.T) {
	input := ProvenanceInput{
		Goal:        "test",
		Model:       "test",
		ContentHash: "sha256:abc",
		ObservedState: ObservedState{
			Hash:  "sha256:def",
			Empty: true,
		},
	}

	m := BuildManifest("id", input, []Operation{}, nil)

	if m.Planner.TokensIn != nil {
		t.Error("expected TokensIn to be nil")
	}
	if m.Planner.TokensOut != nil {
		t.Error("expected TokensOut to be nil")
	}
	if m.Planner.CostUSD != nil {
		t.Error("expected CostUSD to be nil")
	}
	if m.Planner.DurationMS != nil {
		t.Error("expected DurationMS to be nil")
	}
	if m.Validation != nil {
		t.Error("expected Validation to be nil")
	}
	if m.ModulesResolved == nil {
		t.Error("expected ModulesResolved to be non-nil empty slice")
	}
}

func TestBuildManifestWithStageInputs(t *testing.T) {
	input := ProvenanceInput{
		Goal:        "test",
		Model:       "anthropic/claude-sonnet-4-6",
		ContentHash: "sha256:abc",
		ObservedState: ObservedState{
			Hash:  "sha256:def",
			Empty: true,
		},
		TokensIn:   310,
		TokensOut:  190,
		CostUSD:    0.05,
		DurationMS: 5000,
		StageInputs: []StageInput{
			{TaskID: "root", Model: "anthropic/claude-sonnet-4-6", TokensIn: 50, TokensOut: 100, DurationMS: 1000},
			{TaskID: "01-distill", Model: "ollama/llama3", TokensIn: 30, TokensOut: 20, DurationMS: 500},
			{TaskID: "02-design", Model: "anthropic/claude-sonnet-4-6", TokensIn: 60, TokensOut: 40, CostUSD: 0.02, DurationMS: 1500},
			{TaskID: "03-draft", Model: "ollama/llama3", Cached: true},
			{TaskID: "04-review", Model: "anthropic/claude-sonnet-4-6", TokensIn: 90, TokensOut: 70, CostUSD: 0.03, DurationMS: 2000},
		},
	}

	m := BuildManifest("id", input, []Operation{}, nil)

	if len(m.Planner.Stages) != 5 {
		t.Fatalf("expected 5 stages, got %d", len(m.Planner.Stages))
	}

	// Top-level model is the root model.
	if m.Planner.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("top-level Model = %q", m.Planner.Model)
	}

	// Check per-stage data.
	s0 := m.Planner.Stages[0]
	if s0.TaskID != "root" || s0.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("stage 0: %+v", s0)
	}

	s1 := m.Planner.Stages[1]
	if s1.TaskID != "01-distill" || s1.Model != "ollama/llama3" {
		t.Errorf("stage 1: %+v", s1)
	}

	// Cached stage should have no cost.
	s3 := m.Planner.Stages[3]
	if !s3.Cached {
		t.Error("stage 3 should be cached")
	}
	if s3.CostUSD != nil {
		t.Error("cached stage should not have cost")
	}

	// Verify top-level aggregates remain unchanged.
	if m.Planner.TokensIn == nil || *m.Planner.TokensIn != 310 {
		t.Errorf("top-level TokensIn should be 310")
	}
}

func TestBuildManifestWithoutStageInputs(t *testing.T) {
	input := ProvenanceInput{
		Goal:        "test",
		Model:       "test",
		ContentHash: "sha256:abc",
		ObservedState: ObservedState{
			Hash:  "sha256:def",
			Empty: true,
		},
	}

	m := BuildManifest("id", input, []Operation{}, nil)

	if m.Planner.Stages != nil {
		t.Error("expected nil Stages when no StageInputs provided")
	}
}

func TestBuildManifestStageInputsBackwardCompat(t *testing.T) {
	// Verify that JSON marshalling of manifest without stages
	// produces no "stages" key (omitempty).
	input := ProvenanceInput{
		Goal:        "test",
		Model:       "test",
		ContentHash: "sha256:abc",
		ObservedState: ObservedState{Hash: "sha256:def", Empty: true},
	}
	m := BuildManifest("id", input, []Operation{}, nil)

	data, _ := json.Marshal(m)
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)

	planner := raw["planner"].(map[string]interface{})
	if _, exists := planner["stages"]; exists {
		t.Error("stages should be omitted from JSON when nil")
	}
}

func TestHashTaskTree(t *testing.T) {
	dir := t.TempDir()

	// Create a small task tree.
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("root task"), 0o644)
	os.MkdirAll(filepath.Join(dir, "subtasks", "01-gather"), 0o755)
	os.WriteFile(filepath.Join(dir, "subtasks", "01-gather", "task.md"), []byte("gather"), 0o644)

	hash1, err := HashTaskTree(dir)
	if err != nil {
		t.Fatalf("HashTaskTree: %v", err)
	}
	if hash1 == "" {
		t.Fatal("expected non-empty hash")
	}

	// Same content should produce the same hash.
	hash2, err := HashTaskTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hash1 != hash2 {
		t.Errorf("HashTaskTree not deterministic: %q != %q", hash1, hash2)
	}

	// Changing content should change the hash.
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("modified"), 0o644)
	hash3, _ := HashTaskTree(dir)
	if hash1 == hash3 {
		t.Error("expected different hash after content change")
	}
}

func TestHashTaskTreeExcludesOutput(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("task"), 0o644)

	hash1, _ := HashTaskTree(dir)

	// Adding output/ should not change the hash.
	os.MkdirAll(filepath.Join(dir, "output"), 0o755)
	os.WriteFile(filepath.Join(dir, "output", "result.md"), []byte("result"), 0o644)

	hash2, _ := HashTaskTree(dir)
	if hash1 != hash2 {
		t.Error("output/ should be excluded from hash")
	}
}

func TestHashTaskTreeIncludesSmithLib(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("task"), 0o644)

	hash1, _ := HashTaskTree(dir)

	// Adding .smith/lib/ SHOULD change the hash (project-local modules
	// affect validation/execution behavior).
	os.MkdirAll(filepath.Join(dir, ".smith", "lib", "planner"), 0o755)
	os.WriteFile(filepath.Join(dir, ".smith", "lib", "planner", "task.md"), []byte("planner"), 0o644)

	hash2, _ := HashTaskTree(dir)
	if hash1 == hash2 {
		t.Error(".smith/lib/ should be included in hash")
	}
}

func TestHashTaskTreeExcludesSmithProposals(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("task"), 0o644)

	hash1, _ := HashTaskTree(dir)

	// Adding .smith/proposals/ should NOT change the hash.
	os.MkdirAll(filepath.Join(dir, ".smith", "proposals", "abc"), 0o755)
	os.WriteFile(filepath.Join(dir, ".smith", "proposals", "abc", "manifest.json"), []byte("{}"), 0o644)

	hash2, _ := HashTaskTree(dir)
	if hash1 != hash2 {
		t.Error(".smith/proposals/ should be excluded from hash")
	}
}

func TestHashTaskTreeExcludesSmithCache(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("task"), 0o644)

	hash1, _ := HashTaskTree(dir)

	// Adding .smith/cache/ should NOT change the hash.
	os.MkdirAll(filepath.Join(dir, ".smith", "cache", "planner", "abc123"), 0o755)
	os.WriteFile(filepath.Join(dir, ".smith", "cache", "planner", "abc123", "result.json"), []byte("{}"), 0o644)

	hash2, _ := HashTaskTree(dir)
	if hash1 != hash2 {
		t.Error(".smith/cache/ should be excluded from hash")
	}
}

func TestHashProjectState(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("root"), 0o644)
	os.MkdirAll(filepath.Join(dir, "subtasks", "01-gather"), 0o755)
	os.WriteFile(filepath.Join(dir, "subtasks", "01-gather", "task.md"), []byte("gather"), 0o644)

	state, err := HashProjectState(dir)
	if err != nil {
		t.Fatalf("HashProjectState: %v", err)
	}
	if state.TaskCount != 2 {
		t.Errorf("TaskCount = %d, want 2", state.TaskCount)
	}
	if state.Empty {
		t.Error("expected Empty = false")
	}
	if state.Hash == "" {
		t.Error("expected non-empty hash")
	}
}

func TestHashProjectStateEmpty(t *testing.T) {
	dir := t.TempDir()

	state, err := HashProjectState(dir)
	if err != nil {
		t.Fatalf("HashProjectState: %v", err)
	}
	if state.TaskCount != 0 {
		t.Errorf("TaskCount = %d, want 0", state.TaskCount)
	}
	if !state.Empty {
		t.Error("expected Empty = true")
	}
}

func TestHashProjectStateNonexistent(t *testing.T) {
	state, err := HashProjectState("/nonexistent/dir")
	if err != nil {
		t.Fatalf("expected no error for nonexistent dir, got: %v", err)
	}
	if !state.Empty {
		t.Error("expected Empty = true for nonexistent dir")
	}
	if state.TaskCount != 0 {
		t.Errorf("TaskCount = %d, want 0", state.TaskCount)
	}
}
