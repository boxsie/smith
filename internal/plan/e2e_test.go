package plan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/boxsie/smith/internal/executor"
	"github.com/boxsie/smith/internal/proposal"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/validate"
)

// TestE2E_PlanApplyValidateRun proves the v2 proof point:
// given an empty folder, Smith can propose, apply, validate, and run
// a valid task tree from a goal.
func TestE2E_PlanApplyValidateRun(t *testing.T) {
	targetDir := t.TempDir()
	plannerPath := setupPlannerFixture(t)

	// The mock planner writes a complete, valid task tree:
	// - task.md (root task)
	// - agent.md (model config)
	// - subtasks/01-gather/task.md
	// - subtasks/02-summarize/task.md (depends on 01-gather)
	mock := e2ePlannerMock()

	// ---- Phase 1: smith plan ----
	planResult, err := Plan(context.Background(), PlanInput{
		TargetDir:   targetDir,
		Goal:        "Create a research pipeline that gathers and summarizes information",
		Factory:     mockFactory(mock),
		PlannerPath: plannerPath,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// Verify proposal artifacts on disk.
	manifestPath := filepath.Join(planResult.ProposalDir, "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("manifest.json not found: %v", err)
	}
	summaryPath := filepath.Join(planResult.ProposalDir, "summary.md")
	if _, err := os.Stat(summaryPath); err != nil {
		t.Fatalf("summary.md not found: %v", err)
	}

	// Verify manifest has all required provenance fields.
	manifest := planResult.Proposal.Manifest
	assertNonEmpty(t, "ID", manifest.ID)
	assertNonEmpty(t, "Goal", manifest.Goal)
	assertNonEmpty(t, "CreatedAt", manifest.CreatedAt)
	assertNonEmpty(t, "Planner.Model", manifest.Planner.Model)
	assertNonEmpty(t, "Planner.ResolvedFrom", manifest.Planner.ResolvedFrom)
	assertNonEmpty(t, "Planner.ContentHash", manifest.Planner.ContentHash)
	assertNonEmpty(t, "ObservedState.Hash", manifest.ObservedState.Hash)
	if !manifest.ObservedState.Empty {
		t.Error("expected ObservedState.Empty = true")
	}
	if manifest.Validation == nil {
		t.Error("expected Validation in manifest")
	} else if manifest.Validation.Status != "pass" {
		t.Errorf("expected pre-validation pass, got %q: %v", manifest.Validation.Status, manifest.Validation.Errors)
	}
	if len(manifest.Operations) != 4 {
		t.Fatalf("expected 4 operations, got %d", len(manifest.Operations))
	}

	// Verify staged files exist.
	filesDir := proposal.FilesDir(planResult.ProposalDir)
	for _, op := range manifest.Operations {
		if op.Op == "write" {
			staged := filepath.Join(filesDir, op.Path)
			if _, err := os.Stat(staged); err != nil {
				t.Errorf("staged file missing: %s", op.Path)
			}
		}
	}

	// ---- Phase 2: smith apply ----
	applyResult, err := proposal.Apply(proposal.ApplyInput{
		ProposalDir: planResult.ProposalDir,
		TargetDir:   targetDir,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applyResult.Conflicts) > 0 {
		t.Fatalf("unexpected conflicts: %v", applyResult.Conflicts)
	}
	if applyResult.Validation != nil && applyResult.Validation.Status != "pass" {
		t.Fatalf("post-apply validation failed: %v", applyResult.Validation.Errors)
	}

	// Verify files materialized.
	if _, err := os.Stat(filepath.Join(targetDir, "task.md")); err != nil {
		t.Error("task.md not materialized")
	}
	if _, err := os.Stat(filepath.Join(targetDir, "agent.md")); err != nil {
		t.Error("agent.md not materialized")
	}
	if _, err := os.Stat(filepath.Join(targetDir, "subtasks", "01-gather", "task.md")); err != nil {
		t.Error("subtasks/01-gather/task.md not materialized")
	}
	if _, err := os.Stat(filepath.Join(targetDir, "subtasks", "02-summarize", "task.md")); err != nil {
		t.Error("subtasks/02-summarize/task.md not materialized")
	}

	// ---- Phase 3: smith validate ----
	vr := validate.Validate(targetDir)
	if len(vr.Errs) > 0 {
		for _, e := range vr.Errs {
			t.Errorf("validation error: %v", e)
		}
		t.Fatal("validation failed after apply")
	}

	// ---- Phase 4: smith run ----
	root := vr.Root
	graph := vr.Graph
	runMock := &runtime.MockProvider{Default: "This is the research output."}

	runResult, err := executor.Execute(context.Background(), root, graph, executor.Config{
		Factory: mockFactory(runMock),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !runResult.Success {
		for _, tr := range runResult.Tasks {
			if tr.Err != nil {
				t.Errorf("task %q failed: %v", tr.TaskID, tr.Err)
			}
		}
		t.Fatal("run failed")
	}

	// Verify outputs exist on disk.
	rootOutput := filepath.Join(targetDir, "output", "result.md")
	if _, err := os.Stat(rootOutput); err != nil {
		t.Error("root output/result.md not found")
	}
	gatherOutput := filepath.Join(targetDir, "subtasks", "01-gather", "output", "result.md")
	if _, err := os.Stat(gatherOutput); err != nil {
		t.Error("01-gather output/result.md not found")
	}
	summarizeOutput := filepath.Join(targetDir, "subtasks", "02-summarize", "output", "result.md")
	if _, err := os.Stat(summarizeOutput); err != nil {
		t.Error("02-summarize output/result.md not found")
	}

	// All outputs are inspectable files on disk. The proof point is complete.
}

// e2ePlannerMock returns a scripted mock that writes a complete task tree.
func e2ePlannerMock() *runtime.MockProvider {
	callNum := 0
	return &runtime.MockProvider{
		Respond: func(req *runtime.Request) *runtime.Response {
			callNum++
			if callNum == 1 {
				// Round 1: write all proposal files via tool calls.
				return &runtime.Response{
					ToolCalls: []runtime.ToolCall{
						{
							ID:     "call_1",
							ToolID: "proposal.write",
							Input: json.RawMessage(`{
								"path": "task.md",
								"content": "Synthesize the gathered research into a final summary report."
							}`),
						},
						{
							ID:     "call_2",
							ToolID: "proposal.write",
							Input: json.RawMessage(`{
								"path": "agent.md",
								"content": "model: mock/test\npersona: Research coordinator\ntemperature: 0.2\n"
							}`),
						},
						{
							ID:     "call_3",
							ToolID: "proposal.write",
							Input: json.RawMessage(`{
								"path": "subtasks/01-gather/task.md",
								"content": "Gather information about the research topic. Identify key sources, extract relevant facts, and organize them by theme."
							}`),
						},
						{
							ID:     "call_4",
							ToolID: "proposal.write",
							Input: json.RawMessage(`{
								"path": "subtasks/02-summarize/task.md",
								"content": "---\ndepends_on:\n  - 01-gather\n---\n\nSummarize the gathered information into a concise report with key findings and conclusions."
							}`),
						},
					},
					TokensIn:  200,
					TokensOut: 300,
					CostUSD:   0.005,
				}
			}
			// Round 2: return the PlannerOutput JSON.
			return &runtime.Response{
				Content: `{
					"summary": "Creates a research pipeline with gather and summarize stages",
					"operations": [
						{"op": "write", "path": "task.md"},
						{"op": "write", "path": "agent.md"},
						{"op": "write", "path": "subtasks/01-gather/task.md"},
						{"op": "write", "path": "subtasks/02-summarize/task.md"}
					]
				}`,
				TokensIn:  100,
				TokensOut: 80,
				CostUSD:   0.002,
			}
		},
	}
}

func assertNonEmpty(t *testing.T, field, value string) {
	t.Helper()
	if value == "" {
		t.Errorf("expected non-empty %s", field)
	}
}
