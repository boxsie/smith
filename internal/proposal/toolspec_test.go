package proposal

import (
	"encoding/json"
	"testing"
)

func TestToolSpecUnmarshalWithTools(t *testing.T) {
	raw := `{
		"summary": "test plan",
		"operations": [{"op": "write", "path": "task.md", "content": "hello"}],
		"create_tools": [
			{
				"id": "issue.search",
				"description": "Search issues",
				"type": "shell",
				"behavior_spec": "Call GitHub API",
				"timeout": "15s",
				"env": ["GITHUB_TOKEN"],
				"input_fields": [
					{"name": "query", "type": "string", "required": true, "description": "Search query"}
				],
				"output_fields": [
					{"name": "issues", "type": "array", "description": "Matching issues"}
				]
			}
		],
		"use_existing_tools": ["project.read"]
	}`

	var out PlannerOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(out.CreateTools) != 1 {
		t.Fatalf("expected 1 create_tools, got %d", len(out.CreateTools))
	}
	ct := out.CreateTools[0]
	if ct.ID != "issue.search" {
		t.Errorf("expected id %q, got %q", "issue.search", ct.ID)
	}
	if ct.Description != "Search issues" {
		t.Errorf("expected description %q, got %q", "Search issues", ct.Description)
	}
	if ct.Type != "shell" {
		t.Errorf("expected type %q, got %q", "shell", ct.Type)
	}
	if ct.BehaviorSpec != "Call GitHub API" {
		t.Errorf("expected behavior_spec %q, got %q", "Call GitHub API", ct.BehaviorSpec)
	}
	if ct.Timeout != "15s" {
		t.Errorf("expected timeout %q, got %q", "15s", ct.Timeout)
	}
	if len(ct.Env) != 1 || ct.Env[0] != "GITHUB_TOKEN" {
		t.Errorf("unexpected env: %v", ct.Env)
	}
	if len(ct.InputFields) != 1 {
		t.Fatalf("expected 1 input field, got %d", len(ct.InputFields))
	}
	if ct.InputFields[0].Name != "query" || !ct.InputFields[0].Required {
		t.Errorf("unexpected input field: %+v", ct.InputFields[0])
	}
	if len(ct.OutputFields) != 1 || ct.OutputFields[0].Name != "issues" {
		t.Errorf("unexpected output fields: %+v", ct.OutputFields)
	}

	if len(out.UseExistingTools) != 1 || out.UseExistingTools[0] != "project.read" {
		t.Errorf("unexpected use_existing_tools: %v", out.UseExistingTools)
	}
}

func TestToolSpecUnmarshalWithoutTools(t *testing.T) {
	raw := `{
		"summary": "simple plan",
		"operations": [{"op": "write", "path": "task.md", "content": "hello"}]
	}`

	var out PlannerOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.CreateTools != nil {
		t.Errorf("expected nil CreateTools, got %v", out.CreateTools)
	}
	if out.UseExistingTools != nil {
		t.Errorf("expected nil UseExistingTools, got %v", out.UseExistingTools)
	}
}

func TestToolSpecRoundTrip(t *testing.T) {
	original := PlannerOutput{
		Summary: "test",
		Operations: []PlannerOperation{
			{Op: "write", Path: "task.md", Content: "hello"},
		},
		CreateTools: []ToolSpec{
			{
				ID:           "db.query",
				Description:  "Run a database query",
				Type:         "shell",
				BehaviorSpec: "Execute SQL via psql",
				Timeout:      "30s",
				Cache:        "never",
				Env:          []string{"DATABASE_URL"},
				InputFields: []ToolFieldSpec{
					{Name: "sql", Type: "string", Required: true, Description: "SQL query"},
				},
				OutputFields: []ToolFieldSpec{
					{Name: "rows", Type: "array"},
				},
			},
		},
		UseExistingTools: []string{"project.read", "project.list"},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded PlannerOutput
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded.CreateTools) != 1 {
		t.Fatalf("expected 1 create_tools, got %d", len(decoded.CreateTools))
	}
	ct := decoded.CreateTools[0]
	if ct.ID != "db.query" || ct.Cache != "never" || len(ct.Env) != 1 {
		t.Errorf("round-trip mismatch: %+v", ct)
	}
	if len(decoded.UseExistingTools) != 2 {
		t.Errorf("expected 2 use_existing_tools, got %d", len(decoded.UseExistingTools))
	}
}

func TestToolSpecRequiredFieldsOnly(t *testing.T) {
	raw := `{
		"id": "simple.tool",
		"description": "A simple tool",
		"type": "shell",
		"behavior_spec": "echo hello",
		"input_fields": [{"name": "input", "type": "string", "required": true}]
	}`

	var spec ToolSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if spec.Timeout != "" {
		t.Errorf("expected empty timeout, got %q", spec.Timeout)
	}
	if spec.Cache != "" {
		t.Errorf("expected empty cache, got %q", spec.Cache)
	}
	if spec.Env != nil {
		t.Errorf("expected nil env, got %v", spec.Env)
	}
	if spec.OutputFields != nil {
		t.Errorf("expected nil output_fields, got %v", spec.OutputFields)
	}
}
