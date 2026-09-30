package plan

import (
	"context"
	"fmt"
	"testing"

	"github.com/boxsie/smith/internal/executor"
	"github.com/boxsie/smith/internal/proposal"
	"github.com/boxsie/smith/internal/tools"
)

func TestOrchestrateToolCreation_RejectsBuiltinID(t *testing.T) {
	specs := []proposal.ToolSpec{
		{ID: "project.read", Description: "shadowing", Type: "shell", BehaviorSpec: "echo"},
	}
	_, err := orchestrateToolCreation(context.TODO(), specs, nil, nil, nil, t.TempDir())
	if err == nil {
		t.Fatal("expected error for built-in ID")
	}
	if got := err.Error(); !contains(got, "already exists as builtin") {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestOrchestrateToolCreation_RejectsExistingAppTool(t *testing.T) {
	appTools := &tools.ResolvedTools{
		AppDefs: map[string]*tools.AppToolDef{
			"issue.search": {},
		},
		Sources: map[string]string{
			"issue.search": "app",
		},
	}
	specs := []proposal.ToolSpec{
		{ID: "issue.search", Description: "duplicate", Type: "shell", BehaviorSpec: "echo"},
	}
	_, err := orchestrateToolCreation(context.TODO(), specs, appTools, nil, nil, t.TempDir())
	if err == nil {
		t.Fatal("expected error for existing app tool ID")
	}
	if got := err.Error(); !contains(got, "already exists as app") {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestOrchestrateToolCreation_EmptySpecsSkipped(t *testing.T) {
	// Empty specs should never reach orchestrateToolCreation (caller checks),
	// but if they do, it should be a no-op.
	ops, err := orchestrateToolCreation(context.TODO(), nil, nil, nil, nil, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("expected 0 ops, got %d", len(ops))
	}
}

func TestOrchestrateToolCreation_RejectsWhenDiscoveryFailed(t *testing.T) {
	specs := []proposal.ToolSpec{
		{ID: "new.tool", Description: "a tool", Type: "shell", BehaviorSpec: "echo"},
	}
	discoverErr := fmt.Errorf("tools/ contains bare file")
	_, err := orchestrateToolCreation(context.TODO(), specs, nil, discoverErr, nil, t.TempDir())
	if err == nil {
		t.Fatal("expected error when discovery failed")
	}
	if got := err.Error(); !contains(got, "uniqueness check unavailable") {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestValidatePathScoping(t *testing.T) {
	// Test that path validation rejects out-of-scope operations.
	prefix := "tools/issue.search/"
	tests := []struct {
		path    string
		wantErr bool
	}{
		{"tools/issue.search/tool.yaml", false},
		{"tools/issue.search/run.sh", false},
		{"tools/other.tool/tool.yaml", true},
		{"task.md", true},
		{"../escape", true},
	}

	for _, tt := range tests {
		ok := len(tt.path) >= len(prefix) && tt.path[:len(prefix)] == prefix
		if tt.wantErr && ok {
			t.Errorf("path %q should be rejected but passed scope check", tt.path)
		}
		if !tt.wantErr && !ok {
			t.Errorf("path %q should pass but was rejected by scope check", tt.path)
		}
	}
}

func TestToolCreateExecutionError_PrefersSpecificChildFailure(t *testing.T) {
	result := &executor.Result{
		Tasks: []executor.TaskResult{
			{TaskID: "tool-create", Status: "failed", Err: fmt.Errorf("return phase skipped: child dependency failed")},
			{TaskID: "01-draft", Status: "failed", Err: fmt.Errorf("provider returned invalid JSON")},
			{TaskID: "02-review", Status: "failed", Err: fmt.Errorf("return phase skipped: child dependency failed")},
		},
		Success: false,
	}

	err := toolCreateExecutionError(result, "tool-create")
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); !contains(got, "01-draft: provider returned invalid JSON") {
		t.Fatalf("expected specific child error, got %q", got)
	}
	if got := err.Error(); contains(got, "tool-create: return phase skipped: child dependency failed") {
		t.Fatalf("expected generic root error to be suppressed, got %q", got)
	}
}

func TestToolCreateExecutionError_FallsBackToGenericFailure(t *testing.T) {
	result := &executor.Result{
		Tasks: []executor.TaskResult{
			{TaskID: "tool-create", Status: "failed", Err: fmt.Errorf("return phase skipped: child dependency failed")},
		},
		Success: false,
	}

	err := toolCreateExecutionError(result, "tool-create")
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); !contains(got, "tool-create: return phase skipped: child dependency failed") {
		t.Fatalf("expected generic root error, got %q", got)
	}
}

// contains is defined in plan_test.go
