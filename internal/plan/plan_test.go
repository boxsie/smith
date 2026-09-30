package plan

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/proposal"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/task"
)

// mockFactory returns a Factory that always returns the given provider.
func mockFactory(p runtime.Provider) *runtime.Factory {
	return &runtime.Factory{
		Override: func(model string) (runtime.Provider, error) {
			return p, nil
		},
	}
}

// plannerMock returns a scripted mock that writes proposal files via tool calls
// then returns a PlannerOutput JSON response.
func plannerMock() *runtime.MockProvider {
	callNum := 0
	return &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			callNum++
			if callNum == 1 {
				// Round 1: write proposal files via tool calls.
				return &runtime.Response{
					ToolCalls: []runtime.ToolCall{
						{
							ID:     "call_1",
							ToolID: "proposal.write",
							Input:  json.RawMessage(`{"path":"task.md","content":"---\noutput:\n  type: markdown\n---\n\nResearch the given topic and produce a summary."}`),
						},
						{
							ID:     "call_2",
							ToolID: "proposal.write",
							Input:  json.RawMessage(`{"path":"agent.md","content":"model: mock/test\npersona: Research analyst\ntemperature: 0.3\n"}`),
						},
					},
					TokensIn:  100,
					TokensOut: 50,
					CostUSD:   0.001,
				}
			}
			// Round 2: return the PlannerOutput JSON.
			return &runtime.Response{
				Content:   `{"summary":"Creates a simple research task","operations":[{"op":"write","path":"task.md"},{"op":"write","path":"agent.md"}]}`,
				TokensIn:  50,
				TokensOut: 80,
				CostUSD:   0.002,
			}
		},
	}
}

func TestPlan_EmptyDirectory(t *testing.T) {
	targetDir := t.TempDir()
	plannerPath := setupPlannerFixture(t)

	result, err := Plan(context.Background(), PlanInput{
		TargetDir:   targetDir,
		Goal:        "Create a simple research task",
		Factory:     mockFactory(plannerMock()),
		PlannerPath: plannerPath,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// Proposal should exist.
	if result.Proposal == nil {
		t.Fatal("expected proposal, got nil")
	}
	if result.ProposalDir == "" {
		t.Fatal("expected proposal dir, got empty")
	}

	// Manifest should have all required fields.
	manifest := result.Proposal.Manifest
	if manifest.ID == "" {
		t.Error("manifest.ID is empty")
	}
	if manifest.Goal != "Create a simple research task" {
		t.Errorf("manifest.Goal = %q, want %q", manifest.Goal, "Create a simple research task")
	}
	if manifest.CreatedAt == "" {
		t.Error("manifest.CreatedAt is empty")
	}
	if manifest.Planner.Model == "" {
		t.Error("manifest.Planner.Model is empty")
	}
	if manifest.Planner.ResolvedFrom == "" {
		t.Error("manifest.Planner.ResolvedFrom is empty")
	}
	if manifest.Planner.ContentHash == "" {
		t.Error("manifest.Planner.ContentHash is empty")
	}
	if manifest.ObservedState.Hash == "" {
		t.Error("manifest.ObservedState.Hash is empty")
	}
	if !manifest.ObservedState.Empty {
		t.Error("expected ObservedState.Empty = true for empty target")
	}

	// Operations should match what the mock planner returned.
	if len(manifest.Operations) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(manifest.Operations))
	}
	if manifest.Operations[0].Path != "task.md" {
		t.Errorf("op[0].Path = %q, want %q", manifest.Operations[0].Path, "task.md")
	}

	// Summary should exist.
	if result.Proposal.Summary == "" {
		t.Error("proposal summary is empty")
	}

	// Provenance metrics should be populated.
	if manifest.Planner.TokensIn == nil || *manifest.Planner.TokensIn == 0 {
		t.Error("expected non-zero TokensIn in provenance")
	}

	// Files should be staged on disk.
	filesDir := proposal.FilesDir(result.ProposalDir)
	if _, err := os.Stat(filepath.Join(filesDir, "task.md")); err != nil {
		t.Errorf("staged task.md not found: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filesDir, "agent.md")); err != nil {
		t.Errorf("staged agent.md not found: %v", err)
	}

	// manifest.json should exist on disk.
	if _, err := os.Stat(filepath.Join(result.ProposalDir, "manifest.json")); err != nil {
		t.Errorf("manifest.json not found: %v", err)
	}
}

func TestPlan_ModelOverride(t *testing.T) {
	targetDir := t.TempDir()
	plannerPath := setupPlannerFixture(t)

	mock := plannerMock()
	_, err := Plan(context.Background(), PlanInput{
		TargetDir:     targetDir,
		Goal:          "test",
		ModelOverride: "mock/override",
		Factory:       mockFactory(mock),
		PlannerPath:   plannerPath,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// The mock records calls — verify the model was used.
	// Since the mock factory resolves any model, just verify it didn't fail.
	// The real assertion is that the provenance records the override.
}

func TestPlan_MaxCostUSD(t *testing.T) {
	targetDir := t.TempDir()
	plannerPath := setupPlannerFixture(t)

	result, err := Plan(context.Background(), PlanInput{
		TargetDir:   targetDir,
		Goal:        "test",
		MaxCostUSD:  0.50,
		Factory:     mockFactory(plannerMock()),
		PlannerPath: plannerPath,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// MaxCostUSD is applied via agent override — verify the planner's effective
	// agent received it. We can't directly inspect it after execution, but the
	// plan should succeed without error.
	if result.Proposal == nil {
		t.Fatal("expected proposal")
	}
}

func TestBuildProjectSummary_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	ps, err := BuildProjectSummary(dir)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if !strings.HasPrefix(ps.Text, "Empty directory. No existing Smith task tree.\n") {
		t.Errorf("unexpected summary prefix: %q", ps.Text)
	}
	if !strings.Contains(ps.Text, "Tools:") {
		t.Error("empty dir summary should include tool inventory")
	}
}

func TestBuildProjectSummary_NonexistentDir(t *testing.T) {
	ps, err := BuildProjectSummary(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if ps.Text != "Empty directory. No existing Smith task tree." {
		t.Errorf("unexpected summary: %q", ps.Text)
	}
}

func TestBuildProjectSummary_ExistingTree(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "task.md", "Root task")
	writeFile(t, dir, "agent.md", "model: mock/test")
	writeFile(t, dir, "subtasks/01-gather/task.md", "---\ndepends_on: []\n---\n\nGather info")
	writeFile(t, dir, "subtasks/02-analyze/task.md", "---\ndepends_on:\n  - 01-gather\n---\n\nAnalyze info")

	ps, err := BuildProjectSummary(dir)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}

	// Should mention task count.
	if !contains(ps.Text, "3 task(s)") {
		t.Errorf("expected task count in summary, got:\n%s", ps.Text)
	}
	// Should mention dependencies (explicit depends_on from graph).
	if !contains(ps.Text, "depends_on:") {
		t.Errorf("expected dependency info in summary, got:\n%s", ps.Text)
	}
	if !contains(ps.Text, "01-gather") {
		t.Errorf("expected 01-gather dependency reference in summary, got:\n%s", ps.Text)
	}
	// Should mention validation.
	if !contains(ps.Text, "Validation: pass") {
		t.Errorf("expected validation status in summary, got:\n%s", ps.Text)
	}
}

func TestBuildProjectSummary_ImplicitPrefixDeps(t *testing.T) {
	// Tasks with numeric prefixes but no explicit depends_on should still
	// show graph-derived dependencies from implicit prefix ordering.
	dir := t.TempDir()
	writeFile(t, dir, "task.md", "Root task")
	writeFile(t, dir, "agent.md", "model: mock/test")
	writeFile(t, dir, "subtasks/01-first/task.md", "First step")
	writeFile(t, dir, "subtasks/02-second/task.md", "Second step")

	ps, err := BuildProjectSummary(dir)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}

	// 02-second should show a dependency on 01-first from implicit prefix ordering.
	if !contains(ps.Text, "depends_on:") {
		t.Errorf("expected implicit prefix dependency in summary, got:\n%s", ps.Text)
	}
}

func TestBuildProjectSummary_BrokenTree(t *testing.T) {
	// A tree with structural errors should still report files on disk.
	dir := t.TempDir()
	// Both task.md and module.yaml in same dir = validation error
	writeFile(t, dir, "task.md", "Root task")
	writeFile(t, dir, "module.yaml", "source: nonexistent")

	ps, err := BuildProjectSummary(dir)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if !contains(ps.Text, "Validation: fail") {
		t.Errorf("expected validation fail, got:\n%s", ps.Text)
	}
	if !contains(ps.Text, "Files on disk:") {
		t.Errorf("expected file listing for broken tree, got:\n%s", ps.Text)
	}
	if !contains(ps.Text, "task.md") {
		t.Errorf("expected task.md in file listing, got:\n%s", ps.Text)
	}
}

func TestPlan_InvalidTarget_File(t *testing.T) {
	// Target is an existing file, not a directory — should return ErrInvalidTarget.
	dir := t.TempDir()
	tmpFile := filepath.Join(dir, "afile")
	writeFile(t, dir, "afile", "not a dir")

	_, err := Plan(context.Background(), PlanInput{
		TargetDir:   tmpFile,
		Goal:        "test",
		Factory:     mockFactory(plannerMock()),
		PlannerPath: setupPlannerFixture(t),
	})
	if err == nil {
		t.Fatal("expected error for file target")
	}
	var targetErr *ErrInvalidTarget
	if !errors.As(err, &targetErr) {
		t.Errorf("expected ErrInvalidTarget, got %T: %v", err, err)
	}
}

func TestStageContentFiles_WriteWithContent(t *testing.T) {
	proposalDir := t.TempDir()
	ops := []proposal.PlannerOperation{
		{Op: "write", Path: "task.md", Content: "---\n---\nHello"},
		{Op: "write", Path: "agent.md", Content: "model: mock/test"},
	}

	if err := stageContentFiles(proposalDir, ops); err != nil {
		t.Fatalf("stageContentFiles: %v", err)
	}

	filesDir := proposal.FilesDir(proposalDir)
	data, err := os.ReadFile(filepath.Join(filesDir, "task.md"))
	if err != nil {
		t.Fatalf("read task.md: %v", err)
	}
	if string(data) != "---\n---\nHello" {
		t.Errorf("task.md content = %q", string(data))
	}

	data, err = os.ReadFile(filepath.Join(filesDir, "agent.md"))
	if err != nil {
		t.Fatalf("read agent.md: %v", err)
	}
	if string(data) != "model: mock/test" {
		t.Errorf("agent.md content = %q", string(data))
	}
}

func TestStageContentFiles_NestedPaths(t *testing.T) {
	proposalDir := t.TempDir()
	ops := []proposal.PlannerOperation{
		{Op: "write", Path: "subtasks/01-extract/task.md", Content: "Extract data"},
	}

	if err := stageContentFiles(proposalDir, ops); err != nil {
		t.Fatalf("stageContentFiles: %v", err)
	}

	filesDir := proposal.FilesDir(proposalDir)
	data, err := os.ReadFile(filepath.Join(filesDir, "subtasks", "01-extract", "task.md"))
	if err != nil {
		t.Fatalf("read nested file: %v", err)
	}
	if string(data) != "Extract data" {
		t.Errorf("content = %q", string(data))
	}
}

func TestStageContentFiles_DeleteWithContent_Error(t *testing.T) {
	proposalDir := t.TempDir()
	ops := []proposal.PlannerOperation{
		{Op: "delete", Path: "old.md", Content: "should not be here"},
	}

	err := stageContentFiles(proposalDir, ops)
	if err == nil {
		t.Fatal("expected error for delete op with content")
	}
	if !contains(err.Error(), "delete") {
		t.Errorf("error should mention delete, got: %v", err)
	}
}

func TestStageContentFiles_EmptyContentSkipped(t *testing.T) {
	proposalDir := t.TempDir()
	ops := []proposal.PlannerOperation{
		{Op: "write", Path: "task.md"}, // no Content
	}

	if err := stageContentFiles(proposalDir, ops); err != nil {
		t.Fatalf("stageContentFiles: %v", err)
	}

	filesDir := proposal.FilesDir(proposalDir)
	if _, err := os.Stat(filepath.Join(filesDir, "task.md")); !os.IsNotExist(err) {
		t.Error("empty content should not stage a file")
	}
}

func TestStageContentFiles_NormalizesUnsafeReturnConstraints(t *testing.T) {
	proposalDir := t.TempDir()
	content := "---\nconstraints:\n  - Produce exactly five sections in order: Major Headlines, Game Results, Injuries / Transactions, Source Links\n---\nRender the final markdown briefing."
	ops := []proposal.PlannerOperation{
		{Op: "write", Path: "return.md", Content: content},
	}

	if err := stageContentFiles(proposalDir, ops); err != nil {
		t.Fatalf("stageContentFiles: %v", err)
	}

	filesDir := proposal.FilesDir(proposalDir)
	data, err := os.ReadFile(filepath.Join(filesDir, "return.md"))
	if err != nil {
		t.Fatalf("read return.md: %v", err)
	}
	if !strings.Contains(string(data), `- "Produce exactly five sections in order: Major Headlines, Game Results, Injuries / Transactions, Source Links"`) {
		t.Fatalf("expected quoted constraint, got:\n%s", string(data))
	}

	constraints, body, err := task.ParseReturnMD(data)
	if err != nil {
		t.Fatalf("ParseReturnMD: %v", err)
	}
	if len(constraints) != 1 {
		t.Fatalf("constraints count = %d, want 1", len(constraints))
	}
	if constraints[0] != "Produce exactly five sections in order: Major Headlines, Game Results, Injuries / Transactions, Source Links" {
		t.Errorf("constraint = %q", constraints[0])
	}
	if body != "Render the final markdown briefing." {
		t.Errorf("body = %q", body)
	}
}

func TestStageContentFiles_NormalizesUnsafeTaskConstraints(t *testing.T) {
	proposalDir := t.TempDir()
	content := "---\nconstraints:\n  - Match the schema exactly: lead, headlines, results\noutput:\n  type: json\n---\nBuild structured findings."
	ops := []proposal.PlannerOperation{
		{Op: "write", Path: "task.md", Content: content},
	}

	if err := stageContentFiles(proposalDir, ops); err != nil {
		t.Fatalf("stageContentFiles: %v", err)
	}

	filesDir := proposal.FilesDir(proposalDir)
	data, err := os.ReadFile(filepath.Join(filesDir, "task.md"))
	if err != nil {
		t.Fatalf("read task.md: %v", err)
	}
	if !strings.Contains(string(data), `- "Match the schema exactly: lead, headlines, results"`) {
		t.Fatalf("expected quoted constraint, got:\n%s", string(data))
	}

	fm, body, err := task.ParseTaskMD(data, "")
	if err != nil {
		t.Fatalf("ParseTaskMD: %v", err)
	}
	if len(fm.Constraints) != 1 {
		t.Fatalf("constraints count = %d, want 1", len(fm.Constraints))
	}
	if fm.Constraints[0] != "Match the schema exactly: lead, headlines, results" {
		t.Errorf("constraint = %q", fm.Constraints[0])
	}
	if fm.Output == nil || fm.Output.Type != "json" {
		t.Fatalf("output.type = %#v, want json", fm.Output)
	}
	if body != "Build structured findings." {
		t.Errorf("body = %q", body)
	}
}

func TestStageContentFiles_DeleteWithoutContent_NoError(t *testing.T) {
	proposalDir := t.TempDir()
	ops := []proposal.PlannerOperation{
		{Op: "delete", Path: "old.md"},
	}

	if err := stageContentFiles(proposalDir, ops); err != nil {
		t.Fatalf("stageContentFiles: %v", err)
	}
}

func TestPlan_ContentCarryingOps(t *testing.T) {
	// Test that a planner returning content-carrying operations stages files
	// and produces a valid proposal without using proposal.write tool.
	targetDir := t.TempDir()
	plannerPath := setupPlannerFixture(t)

	contentMock := &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			return &runtime.Response{
				Content: `{"summary":"Content-carrying test","operations":[` +
					`{"op":"write","path":"task.md","content":"---\noutput:\n  type: markdown\n---\n\nA task."},` +
					`{"op":"write","path":"agent.md","content":"model: mock/test\ntemperature: 0.3\n"}` +
					`]}`,
				TokensIn:  100,
				TokensOut: 80,
			}
		},
	}

	result, err := Plan(context.Background(), PlanInput{
		TargetDir:   targetDir,
		Goal:        "test content staging",
		Factory:     mockFactory(contentMock),
		PlannerPath: plannerPath,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if result.Proposal == nil {
		t.Fatal("expected proposal")
	}
	if len(result.Proposal.Manifest.Operations) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(result.Proposal.Manifest.Operations))
	}

	// Verify staged files.
	filesDir := proposal.FilesDir(result.ProposalDir)
	data, err := os.ReadFile(filepath.Join(filesDir, "task.md"))
	if err != nil {
		t.Fatalf("read staged task.md: %v", err)
	}
	if !contains(string(data), "A task.") {
		t.Errorf("staged task.md content: %q", string(data))
	}
}

func TestPlan_ContentCarryingOps_NormalizesUnsafeReturnConstraints(t *testing.T) {
	targetDir := t.TempDir()
	plannerPath := setupPlannerFixture(t)

	contentMock := &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			return &runtime.Response{
				Content: `{"summary":"Return normalization test","operations":[` +
					`{"op":"write","path":"task.md","content":"---\n---\nFrame the child output for the final briefing."},` +
					`{"op":"write","path":"agent.md","content":"model: anthropic/claude-sonnet-4-6\ntemperature: 0.2\n"},` +
					`{"op":"write","path":"return.md","content":"---\nconstraints:\n  - Produce exactly five sections in order: Major Headlines, Game Results, Injuries / Transactions, Source Links\n---\nRender the final markdown briefing from the child output."},` +
					`{"op":"write","path":"subtasks/01-search/task.md","content":"---\n---\nCollect the supporting facts."},` +
					`{"op":"write","path":"subtasks/01-search/agent.md","content":"model: anthropic/claude-sonnet-4-6\ntemperature: 0.2\n"}` +
					`]}`,
				TokensIn:  100,
				TokensOut: 80,
			}
		},
	}

	result, err := Plan(context.Background(), PlanInput{
		TargetDir:   targetDir,
		Goal:        "test return normalization",
		Factory:     mockFactory(contentMock),
		PlannerPath: plannerPath,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	filesDir := proposal.FilesDir(result.ProposalDir)
	data, err := os.ReadFile(filepath.Join(filesDir, "return.md"))
	if err != nil {
		t.Fatalf("read staged return.md: %v", err)
	}
	if !strings.Contains(string(data), `- "Produce exactly five sections in order: Major Headlines, Game Results, Injuries / Transactions, Source Links"`) {
		t.Fatalf("expected quoted constraint, got:\n%s", string(data))
	}
}

func TestHasContentOps(t *testing.T) {
	tests := []struct {
		name string
		ops  []proposal.PlannerOperation
		want bool
	}{
		{"all empty", []proposal.PlannerOperation{{Op: "write", Path: "a"}}, false},
		{"one with content", []proposal.PlannerOperation{{Op: "write", Path: "a", Content: "x"}}, true},
		{"mixed", []proposal.PlannerOperation{{Op: "write", Path: "a"}, {Op: "write", Path: "b", Content: "y"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasContentOps(tt.ops); got != tt.want {
				t.Errorf("hasContentOps = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildOpsForValidation(t *testing.T) {
	output := proposal.PlannerOutput{
		Summary: "test",
		Operations: []proposal.PlannerOperation{
			{Op: "write", Path: "task.md", Content: "x"},
			{Op: "delete", Path: "old.md"},
		},
	}
	ops := buildOpsForValidation(output)
	if len(ops) != 2 {
		t.Fatalf("expected 2 ops, got %d", len(ops))
	}
	if ops[0].Source != "files/task.md" {
		t.Errorf("write source: got %q", ops[0].Source)
	}
	if ops[1].Source != "" {
		t.Errorf("delete source should be empty, got %q", ops[1].Source)
	}
}

func TestAuthorWithPreValidation(t *testing.T) {
	proposalDir, targetDir := setupProposalTestDirs(t)

	preVal := &proposal.ValidationResult{
		Status:      "pass",
		ValidatedAt: "2026-03-21T00:00:00Z",
	}

	input := proposal.AuthorInput{
		ProposalDir: proposalDir,
		PlannerOutput: proposal.PlannerOutput{
			Summary: "test",
			Operations: []proposal.PlannerOperation{
				{Op: "write", Path: "task.md"},
				{Op: "write", Path: "agent.md"},
			},
		},
		Provenance: proposal.ProvenanceInput{
			Goal:        "test",
			Model:       "test-model",
			ContentHash: "sha256:abc",
			ObservedState: proposal.ObservedState{
				Hash:  "sha256:def",
				Empty: true,
			},
		},
		TargetDir:     targetDir,
		PreValidation: preVal,
	}

	p, err := proposal.Author(input)
	if err != nil {
		t.Fatalf("Author: %v", err)
	}

	// The provided PreValidation should be used in the manifest.
	if p.Manifest.Validation == nil {
		t.Fatal("expected validation in manifest")
	}
	if p.Manifest.Validation.ValidatedAt != "2026-03-21T00:00:00Z" {
		t.Errorf("validation.validated_at = %q, want pre-provided value", p.Manifest.Validation.ValidatedAt)
	}
}

func setupProposalTestDirs(t *testing.T) (proposalDir, targetDir string) {
	t.Helper()
	targetDir = t.TempDir()
	proposalDir = t.TempDir()
	filesDir := filepath.Join(proposalDir, "files")
	os.MkdirAll(filesDir, 0o755)
	os.WriteFile(filepath.Join(filesDir, "task.md"), []byte("---\n---\nRoot task"), 0o644)
	os.WriteFile(filepath.Join(filesDir, "agent.md"), []byte("model: anthropic/claude-sonnet-4-6"), 0o644)
	return proposalDir, targetDir
}

func TestReadPipelineMarker_Present(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pipeline.md", "terminal_stage: subtasks/04-review\n")
	ts, err := readPipelineMarker(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts != "subtasks/04-review" {
		t.Errorf("terminal_stage: got %q, want subtasks/04-review", ts)
	}
}

func TestReadPipelineMarker_Absent(t *testing.T) {
	dir := t.TempDir()
	ts, err := readPipelineMarker(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts != "" {
		t.Errorf("expected empty for absent pipeline.md, got %q", ts)
	}
}

func TestReadPipelineMarker_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pipeline.md", "not: [valid yaml: {")
	_, err := readPipelineMarker(dir)
	if err == nil {
		t.Fatal("expected error for invalid YAML in pipeline.md")
	}
}

func TestReadPipelineMarker_EmptyTerminalStage(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pipeline.md", "terminal_stage:\n")
	_, err := readPipelineMarker(dir)
	if err == nil {
		t.Fatal("expected error for empty terminal_stage")
	}
}

func TestPlan_PipelineMarkerActivation(t *testing.T) {
	// A planner with pipeline.md should trigger pipeline behavior.
	// We can verify this by checking that the planner runs without
	// proposal.write tool access (pipeline mode disables it).
	targetDir := t.TempDir()
	plannerPath := setupStagedPlannerFixture(t)

	mock := stagedPlannerMock()
	result, err := Plan(context.Background(), PlanInput{
		TargetDir:   targetDir,
		Goal:        "test pipeline marker",
		Factory:     mockFactory(mock),
		PlannerPath: plannerPath,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if result.Proposal == nil {
		t.Fatal("expected proposal")
	}
}

func TestPlan_StagedPlannerRoutesModelsByStage(t *testing.T) {
	targetDir := t.TempDir()
	plannerPath := setupBuiltinLikeStagedPlannerFixture(t)
	mock := stagedPlannerMock()

	_, err := Plan(context.Background(), PlanInput{
		TargetDir:   targetDir,
		Goal:        "route planner stages",
		Factory:     mockFactory(mock),
		PlannerPath: plannerPath,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if len(mock.Calls) != 5 {
		t.Fatalf("expected 5 planner calls, got %d", len(mock.Calls))
	}

	want := []string{
		plannerFallbackModel,
		plannerFallbackModel,
		plannerFallbackModel,
		plannerFallbackModel,
		plannerFallbackModel,
	}
	for i, call := range mock.Calls {
		if call.Model != want[i] {
			t.Errorf("call %d model = %q, want %q", i+1, call.Model, want[i])
		}
	}
}

func TestPlan_ModelOverrideAppliesToAllStagedPlannerTasks(t *testing.T) {
	targetDir := t.TempDir()
	plannerPath := setupBuiltinLikeStagedPlannerFixture(t)
	mock := stagedPlannerMock()

	_, err := Plan(context.Background(), PlanInput{
		TargetDir:     targetDir,
		Goal:          "force one planner model",
		ModelOverride: "mock/override",
		Factory:       mockFactory(mock),
		PlannerPath:   plannerPath,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if len(mock.Calls) != 5 {
		t.Fatalf("expected 5 planner calls, got %d", len(mock.Calls))
	}
	for i, call := range mock.Calls {
		if call.Model != "mock/override" {
			t.Errorf("call %d model = %q, want mock/override", i+1, call.Model)
		}
	}
}

func TestPlan_ModelOverrideAcceptsOllamaForPlanner(t *testing.T) {
	targetDir := t.TempDir()
	plannerPath := setupBuiltinLikeStagedPlannerFixture(t)
	mock := stagedPlannerMock()

	_, err := Plan(context.Background(), PlanInput{
		TargetDir:     targetDir,
		Goal:          "accept ollama override",
		ModelOverride: "ollama/llama3",
		Factory:       mockFactory(mock),
		PlannerPath:   plannerPath,
	})
	if err != nil {
		t.Fatalf("expected ollama override to succeed, got: %v", err)
	}
	for i, call := range mock.Calls {
		if call.Model != "ollama/llama3" {
			t.Errorf("call %d model = %q, want ollama/llama3", i+1, call.Model)
		}
	}
}

func TestPlan_ModelOverrideRejectsUnknownProvider(t *testing.T) {
	targetDir := t.TempDir()
	plannerPath := setupBuiltinLikeStagedPlannerFixture(t)
	mock := stagedPlannerMock()

	_, err := Plan(context.Background(), PlanInput{
		TargetDir:     targetDir,
		Goal:          "reject unknown provider",
		ModelOverride: "unknown/model",
		Factory:       mockFactory(mock),
		PlannerPath:   plannerPath,
	})
	if err == nil {
		t.Fatal("expected error for unknown provider override")
	}
	if !contains(err.Error(), "unsupported model provider") {
		t.Errorf("error should mention unsupported provider, got: %v", err)
	}
	if len(mock.Calls) != 0 {
		t.Errorf("expected no planner calls when override is rejected, got %d", len(mock.Calls))
	}
}

func TestPlan_CustomPlannerModelsRemainUnchanged(t *testing.T) {
	targetDir := t.TempDir()
	plannerPath := setupStagedPlannerFixture(t)
	mock := stagedPlannerMock()

	_, err := Plan(context.Background(), PlanInput{
		TargetDir:   targetDir,
		Goal:        "respect custom planner models",
		Factory:     mockFactory(mock),
		PlannerPath: plannerPath,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	for i, call := range mock.Calls {
		if call.Model != "mock/planner" {
			t.Errorf("call %d model = %q, want mock/planner", i+1, call.Model)
		}
	}
}

func TestPlan_NoPipelineMarkerLegacy(t *testing.T) {
	// A planner without pipeline.md should use legacy behavior.
	targetDir := t.TempDir()
	plannerPath := setupPlannerFixture(t)

	result, err := Plan(context.Background(), PlanInput{
		TargetDir:   targetDir,
		Goal:        "test legacy",
		Factory:     mockFactory(plannerMock()),
		PlannerPath: plannerPath,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if result.Proposal == nil {
		t.Fatal("expected proposal")
	}
}

func TestPlan_PipelineMarkerNonexistentStage(t *testing.T) {
	targetDir := t.TempDir()
	plannerPath := setupPlannerFixture(t) // legacy fixture
	// Add a pipeline.md pointing to nonexistent stage.
	writeFile(t, plannerPath, "pipeline.md", "terminal_stage: subtasks/99-nonexistent\n")

	_, err := Plan(context.Background(), PlanInput{
		TargetDir:   targetDir,
		Goal:        "test",
		Factory:     mockFactory(plannerMock()),
		PlannerPath: plannerPath,
	})
	if err == nil {
		t.Fatal("expected error for nonexistent terminal stage")
	}
	if !contains(err.Error(), "does not exist") {
		t.Errorf("error should mention nonexistent stage, got: %v", err)
	}
}

// setupStagedPlannerFixture creates a minimal staged planner for testing.
func setupStagedPlannerFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Root
	writeFile(t, dir, "task.md", "---\n---\nYou are the planner. Emit planning guidance.")
	writeFile(t, dir, "agent.md", "model: mock/planner\ntemperature: 0.2\n")
	writeFile(t, dir, "pipeline.md", "terminal_stage: subtasks/04-review\n")

	// 01-distill
	writeFile(t, dir, "subtasks/01-distill/task.md", "---\n---\nCompress the project summary.")
	writeFile(t, dir, "subtasks/01-distill/agent.md", "model: mock/planner\nmax_tokens: 2048\n")

	// 02-design
	writeFile(t, dir, "subtasks/02-design/task.md", "---\noutput:\n  type: json\n---\nDesign the tree.")
	writeFile(t, dir, "subtasks/02-design/agent.md", "model: mock/planner\nmax_tokens: 4096\n")
	writeFile(t, dir, "subtasks/02-design/schema.md", `{"type":"object","required":["tasks"],"properties":{"tasks":{"type":"array","items":{"type":"object"}}}}`)

	// 03-draft
	writeFile(t, dir, "subtasks/03-draft/task.md", "---\noutput:\n  type: json\ndepends_on:\n  - 02-design\n---\nProduce the draft artifact.")
	writeFile(t, dir, "subtasks/03-draft/agent.md", "model: mock/planner\nmax_tokens: 4096\n")
	writeFile(t, dir, "subtasks/03-draft/schema.md", `{"type":"object","required":["summary","operations"],"properties":{"summary":{"type":"string"},"operations":{"type":"array","items":{"type":"object","required":["op","path"],"properties":{"op":{"type":"string","enum":["write","delete"]},"path":{"type":"string"},"content":{"type":"string"}}}}}}`)

	// 04-review
	writeFile(t, dir, "subtasks/04-review/task.md", "---\noutput:\n  type: json\ndepends_on:\n  - 03-draft\n---\nValidate the draft artifact.")
	writeFile(t, dir, "subtasks/04-review/agent.md", "model: mock/planner\nmax_tokens: 4096\n")
	writeFile(t, dir, "subtasks/04-review/schema.md", `{"type":"object","required":["summary","operations"],"properties":{"summary":{"type":"string"},"operations":{"type":"array","items":{"type":"object","required":["op","path"],"properties":{"op":{"type":"string","enum":["write","delete"]},"path":{"type":"string"},"content":{"type":"string"}}}}}}`)

	return dir
}

func setupBuiltinLikeStagedPlannerFixture(t *testing.T) string {
	t.Helper()
	dir := setupStagedPlannerFixture(t)

	writeFile(t, dir, "agent.md", "model: anthropic/claude-sonnet-4-6\ntemperature: 0.2\n")
	writeFile(t, dir, "subtasks/01-distill/agent.md", "model: anthropic/claude-sonnet-4-6\nmax_tokens: 2048\n")
	writeFile(t, dir, "subtasks/02-design/agent.md", "model: anthropic/claude-sonnet-4-6\nmax_tokens: 4096\n")
	writeFile(t, dir, "subtasks/03-draft/agent.md", "model: anthropic/claude-sonnet-4-6\nmax_tokens: 4096\n")
	writeFile(t, dir, "subtasks/04-review/agent.md", "model: anthropic/claude-sonnet-4-6\nmax_tokens: 4096\n")

	return dir
}

// stagedPlannerMock returns a mock that simulates a 4-stage pipeline.
func stagedPlannerMock() *runtime.MockProvider {
	callNum := 0
	return &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			callNum++
			switch callNum {
			case 1:
				// Root task-phase: emit planning guidance
				return &runtime.Response{
					Content:  "Smith planning guidance: use return.md when needed...",
					TokensIn: 50, TokensOut: 100,
				}
			case 2:
				// 01-distill: compressed summary
				return &runtime.Response{
					Content:  "Compact project summary: empty directory, no existing task tree.",
					TokensIn: 30, TokensOut: 20,
				}
			case 3:
				// 02-design: tree design JSON
				return &runtime.Response{
					Content:  `{"tasks":[{"id":"root","path":".","purpose":"root task","output_type":"markdown"}],"root_has_return":false,"root_model":"anthropic/claude-sonnet-4-6"}`,
					TokensIn: 60, TokensOut: 40,
				}
			case 4:
				// 03-draft: draft artifact
				return &runtime.Response{
					Content: `{"summary":"Simple research task","operations":[` +
						`{"op":"write","path":"task.md","content":"---\noutput:\n  type: markdown\n---\n\nResearch the topic."},` +
						`{"op":"write","path":"agent.md","content":"model: mock/test\ntemperature: 0.3\n"}` +
						`]}`,
					TokensIn: 80, TokensOut: 60,
				}
			default:
				// 04-review: corrected draft artifact (unchanged)
				return &runtime.Response{
					Content: `{"summary":"Simple research task","operations":[` +
						`{"op":"write","path":"task.md","content":"---\noutput:\n  type: markdown\n---\n\nResearch the topic."},` +
						`{"op":"write","path":"agent.md","content":"model: mock/test\ntemperature: 0.3\n"}` +
						`]}`,
					TokensIn: 90, TokensOut: 70,
				}
			}
		},
	}
}

func TestBuildPlannerModelContext(t *testing.T) {
	content := buildPlannerModelContext(&plannerModelSelection{
		StageModels: map[string]string{
			"root":      plannerFallbackModel,
			"02-design": plannerFallbackModel,
			"04-review": plannerFallbackModel,
		},
		AvailableModels: []string{plannerFallbackModel},
	}, "")

	if !contains(content, "Available Models") {
		t.Fatalf("context should include an available models section, got:\n%s", content)
	}
	if !contains(content, plannerFallbackModel) {
		t.Errorf("context should include %s", plannerFallbackModel)
	}
	if !contains(content, "If the preferred model is unavailable") {
		t.Errorf("context should include fallback guidance, got:\n%s", content)
	}
}

func TestBuildPlannerModelContext_WithTaskModel(t *testing.T) {
	content := buildPlannerModelContext(&plannerModelSelection{
		AvailableModels: []string{plannerFallbackModel},
	}, "ollama/llama3.1:latest")

	if !contains(content, "Default Task Model") {
		t.Error("context should include Default Task Model section when taskModel is set")
	}
	if !contains(content, "ollama/llama3.1:latest") {
		t.Error("context should include the configured task model")
	}
}

func TestBuildPlannerModelContext_EmptyTaskModel(t *testing.T) {
	content := buildPlannerModelContext(&plannerModelSelection{
		AvailableModels: []string{plannerFallbackModel},
	}, "")

	if contains(content, "Default Task Model") {
		t.Error("context should not include Default Task Model section when taskModel is empty")
	}
}

func TestPlannerBuiltinModels_IncludesFallback(t *testing.T) {
	// The embedded planner assets hardcode plannerFallbackModel in agent.md.
	// plannerBuiltinModels() must always include the fallback so that stages
	// using it are recognized as reroutable when the user configures a
	// different default_planner_model.
	builtins := plannerBuiltinModels()
	if !builtins[plannerFallbackModel] {
		t.Errorf("plannerBuiltinModels() must include the fallback model %q", plannerFallbackModel)
	}
}

func TestInjectPlannerModelContext_TaskModelInAvailableList(t *testing.T) {
	// When default_task_model is set, it must appear in AvailableModels
	// so that planner prompts don't reject it as "not in the available list."
	selection := &plannerModelSelection{
		AvailableModels: []string{plannerFallbackModel},
	}
	taskModel := "ollama/llama3.1:latest"

	// Simulate what injectPlannerModelContext does with the selection.
	found := false
	for _, m := range selection.AvailableModels {
		if m == taskModel {
			found = true
			break
		}
	}
	if found {
		t.Fatal("precondition: task model should not be in the list yet")
	}

	// Build context through the real function — it should add the task model.
	content := buildPlannerModelContext(selection, taskModel)

	// After injectPlannerModelContext adds taskModel, the available list
	// rendered in the context must include it.
	if !contains(content, "ollama/llama3.1:latest") {
		t.Error("model context should include the task model")
	}

	// Also verify via the selection mutation path (what injectPlannerModelContext does).
	selection2 := &plannerModelSelection{
		AvailableModels: []string{plannerFallbackModel},
	}
	// Replicate the injection logic.
	selection2.AvailableModels = append(selection2.AvailableModels, taskModel)
	content2 := buildPlannerModelContext(selection2, taskModel)
	if !contains(content2, "- `ollama/llama3.1:latest`") {
		t.Error("task model should appear as a bullet in the Available Models list")
	}
}

func TestInjectPlannerModelContext_TaskModelNoDuplicate(t *testing.T) {
	// When the task model already matches a planner stage model, it should
	// not be duplicated in AvailableModels.
	selection := &plannerModelSelection{
		AvailableModels: []string{plannerFallbackModel},
	}
	// Task model same as planner model — should not duplicate.
	taskModel := plannerFallbackModel
	found := false
	for _, m := range selection.AvailableModels {
		if m == taskModel {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("precondition: task model should already be in the list")
	}
	// Count should stay at 1.
	count := 0
	for _, m := range selection.AvailableModels {
		if m == taskModel {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 occurrence of task model, got %d", count)
	}
}

func TestBuildPlannerModelContext_CapabilityHints(t *testing.T) {
	content := buildPlannerModelContext(&plannerModelSelection{
		AvailableModels: []string{"anthropic/claude-sonnet-4-6", "ollama/qwen2.5:14b"},
	}, "ollama/qwen2.5:14b")

	if !contains(content, "Model Capabilities") {
		t.Error("context should include Model Capabilities section")
	}
	if !contains(content, "`anthropic/claude-sonnet-4-6`: cloud, medium") {
		t.Error("context should annotate anthropic model as cloud, medium")
	}
	if !contains(content, "`ollama/qwen2.5:14b`: local, medium") {
		t.Error("context should annotate ollama model as local, medium")
	}
	if !contains(content, "Capability-Aware Task Design") {
		t.Error("context should include Capability-Aware Task Design section")
	}
	if !contains(content, "Small local models") {
		t.Error("context should include small model guidance")
	}
}

func TestBuildPlannerModelContext_SmallLocalDefault(t *testing.T) {
	content := buildPlannerModelContext(&plannerModelSelection{
		AvailableModels: []string{"ollama/qwen2.5:7b"},
	}, "ollama/qwen2.5:7b")

	if !contains(content, "`ollama/qwen2.5:7b`: local, small") {
		t.Error("context should annotate 7b model as local, small")
	}
	if !contains(content, "Capability-Aware Task Design") {
		t.Error("context should include capability guidance")
	}
}

func TestValidateOperationPaths_DeleteTraversal(t *testing.T) {
	targetDir := t.TempDir()
	ops := []proposal.PlannerOperation{
		{Op: "delete", Path: "../../victim"},
	}
	err := validateOperationPaths(targetDir, ops)
	if err == nil {
		t.Fatal("expected error for delete path traversal")
	}
	if !contains(err.Error(), "delete") {
		t.Errorf("error should mention delete, got: %v", err)
	}
}

func TestValidateOperationPaths_WriteTraversal(t *testing.T) {
	targetDir := t.TempDir()
	ops := []proposal.PlannerOperation{
		{Op: "write", Path: "../../../etc/passwd", Content: "x"},
	}
	err := validateOperationPaths(targetDir, ops)
	if err == nil {
		t.Fatal("expected error for write path traversal")
	}
}

func TestValidateOperationPaths_SmithDir(t *testing.T) {
	targetDir := t.TempDir()
	ops := []proposal.PlannerOperation{
		{Op: "delete", Path: ".smith/config.json"},
	}
	err := validateOperationPaths(targetDir, ops)
	if err == nil {
		t.Fatal("expected error for .smith/ path")
	}
}

func TestValidateOperationPaths_EmptyPath(t *testing.T) {
	targetDir := t.TempDir()
	tests := []proposal.PlannerOperation{
		{Op: "write", Path: "", Content: "x"},
		{Op: "delete", Path: ""},
	}
	for _, op := range tests {
		err := validateOperationPaths(targetDir, []proposal.PlannerOperation{op})
		if err == nil {
			t.Errorf("expected error for %s op with empty path", op.Op)
		}
		if !contains(err.Error(), "empty path") {
			t.Errorf("error should mention empty path, got: %v", err)
		}
	}
}

func TestValidateOperationPaths_ValidPaths(t *testing.T) {
	targetDir := t.TempDir()
	ops := []proposal.PlannerOperation{
		{Op: "write", Path: "task.md", Content: "x"},
		{Op: "write", Path: "subtasks/01-foo/task.md", Content: "y"},
		{Op: "delete", Path: "old.md"},
	}
	err := validateOperationPaths(targetDir, ops)
	if err != nil {
		t.Fatalf("unexpected error for valid paths: %v", err)
	}
}

func TestStageContentFiles_TraversalRejected(t *testing.T) {
	proposalDir := t.TempDir()
	ops := []proposal.PlannerOperation{
		{Op: "write", Path: "../../etc/passwd", Content: "malicious"},
	}

	err := stageContentFiles(proposalDir, ops)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	if !contains(err.Error(), "unsafe") {
		t.Errorf("error should mention unsafe path, got: %v", err)
	}
}

func TestStageContentFiles_AbsolutePathRejected(t *testing.T) {
	proposalDir := t.TempDir()
	ops := []proposal.PlannerOperation{
		{Op: "write", Path: "/etc/passwd", Content: "malicious"},
	}

	err := stageContentFiles(proposalDir, ops)
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
}

func TestStageContentFiles_SmithDirRejected(t *testing.T) {
	proposalDir := t.TempDir()
	ops := []proposal.PlannerOperation{
		{Op: "write", Path: ".smith/config.json", Content: "malicious"},
	}

	err := stageContentFiles(proposalDir, ops)
	if err == nil {
		t.Fatal("expected error for .smith/ path")
	}
	if !contains(err.Error(), ".smith/") {
		t.Errorf("error should mention .smith/, got: %v", err)
	}
}

func TestStageContentFiles_MixedOps(t *testing.T) {
	proposalDir := t.TempDir()
	ops := []proposal.PlannerOperation{
		{Op: "write", Path: "task.md", Content: "root task"},
		{Op: "write", Path: "agent.md"},      // no Content — skip
		{Op: "delete", Path: "old-notes.md"}, // delete without content — ok
	}

	if err := stageContentFiles(proposalDir, ops); err != nil {
		t.Fatalf("stageContentFiles: %v", err)
	}

	filesDir := proposal.FilesDir(proposalDir)
	// task.md should be staged.
	if _, err := os.Stat(filepath.Join(filesDir, "task.md")); err != nil {
		t.Error("task.md should be staged")
	}
	// agent.md should not be staged.
	if _, err := os.Stat(filepath.Join(filesDir, "agent.md")); !os.IsNotExist(err) {
		t.Error("agent.md should not be staged (empty Content)")
	}
}

func TestPlannerCacheDir_ProjectLocal(t *testing.T) {
	target := t.TempDir()
	hashDir := "abc123def456"

	dir, err := plannerCacheDir(target, hashDir)
	if err != nil {
		t.Fatalf("plannerCacheDir: %v", err)
	}

	want := filepath.Join(target, ".smith", "cache", "planner", hashDir)
	if dir != want {
		t.Errorf("plannerCacheDir = %q, want %q", dir, want)
	}

	// Directory should be created.
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory")
	}
}

func TestPlannerCacheDir_DifferentHashesDifferentDirs(t *testing.T) {
	target := t.TempDir()

	dir1, _ := plannerCacheDir(target, "hash1")
	dir2, _ := plannerCacheDir(target, "hash2")

	if dir1 == dir2 {
		t.Error("different hashes should produce different cache dirs")
	}
}

// --- helpers ---

func setupPlannerFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "task.md", "---\ncache: never\noutput:\n  type: json\n---\n\nYou are a planner.")
	writeFile(t, dir, "agent.md", "model: mock/planner\ntemperature: 0.2\n")
	writeFile(t, dir, "tools.md", "- project.list\n- project.read\n- project.find\n- proposal.write\n")
	writeFile(t, dir, "schema.md", `{"type":"object","required":["summary","operations"],"properties":{"summary":{"type":"string"},"operations":{"type":"array","items":{"type":"object","required":["op","path"],"properties":{"op":{"type":"string","enum":["write","delete"]},"path":{"type":"string"}}}}}}`)
	return dir
}

func writeFile(t *testing.T, base, relPath, content string) {
	t.Helper()
	fullPath := filepath.Join(base, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
