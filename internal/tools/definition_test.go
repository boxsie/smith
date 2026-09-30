package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeToolDir creates a tool definition directory with the given files.
func writeToolDir(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

var validShellYAML = `description: "Search issues"
type: shell
`

var validTaskYAML = `description: "Look up customer"
type: task
source: customer-lookup
`

var validNativeYAML = `description: "Fetch from the web"
type: native
`

var validInputSchema = `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`

func TestParseToolDir_ValidShellTool(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "issue.search")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         validShellYAML,
		"input.schema.json": validInputSchema,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	def, err := ParseToolDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if def.ToolYAML.Type != "shell" {
		t.Errorf("type = %q, want shell", def.ToolYAML.Type)
	}
	if def.ToolYAML.Description != "Search issues" {
		t.Errorf("description = %q", def.ToolYAML.Description)
	}
	if def.Timeout.String() != "30s" {
		t.Errorf("timeout = %s, want 30s", def.Timeout)
	}
	if def.ToolYAML.Cache != "auto" {
		t.Errorf("cache = %q, want auto", def.ToolYAML.Cache)
	}
	if def.RunShPath == "" {
		t.Error("RunShPath should be set for shell tools")
	}
}

func TestParseToolDir_ValidTaskTool(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "customer.lookup")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         validTaskYAML,
		"input.schema.json": validInputSchema,
	})

	def, err := ParseToolDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if def.ToolYAML.Type != "task" {
		t.Errorf("type = %q, want task", def.ToolYAML.Type)
	}
	if def.ToolYAML.Source != "customer-lookup" {
		t.Errorf("source = %q", def.ToolYAML.Source)
	}
}

func TestParseToolDir_ValidNativeTool(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "web.lookup")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         validNativeYAML,
		"input.schema.json": validInputSchema,
	})

	def, err := ParseToolDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if def.ToolYAML.Type != "native" {
		t.Errorf("type = %q, want native", def.ToolYAML.Type)
	}
	if def.RunShPath != "" {
		t.Error("RunShPath should be empty for native tools")
	}
}

func TestParseToolDir_MissingDescription(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         "type: shell\n",
		"input.schema.json": validInputSchema,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "description") {
		t.Fatalf("expected description error, got: %v", err)
	}
}

func TestParseToolDir_EmptyDescription(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         "description: \"\"\ntype: shell\n",
		"input.schema.json": validInputSchema,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "description") {
		t.Fatalf("expected description error, got: %v", err)
	}
}

func TestParseToolDir_UnknownType(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         "description: test\ntype: unknown\n",
		"input.schema.json": validInputSchema,
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "type") {
		t.Fatalf("expected type error, got: %v", err)
	}
}

func TestParseToolDir_ShellTimeoutExceeds5m(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         "description: test\ntype: shell\ntimeout: 10m\n",
		"input.schema.json": validInputSchema,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "5m") {
		t.Fatalf("expected 5m timeout error, got: %v", err)
	}
}

func TestParseToolDir_TaskTimeoutExceeds15m(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         "description: test\ntype: task\nsource: foo\ntimeout: 20m\n",
		"input.schema.json": validInputSchema,
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "15m") {
		t.Fatalf("expected 15m timeout error, got: %v", err)
	}
}

func TestParseToolDir_EnvOnTaskTool(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         "description: test\ntype: task\nsource: foo\nenv:\n  - FOO\n",
		"input.schema.json": validInputSchema,
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "env") {
		t.Fatalf("expected env error, got: %v", err)
	}
}

func TestParseToolDir_SourceOnShellTool(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         "description: test\ntype: shell\nsource: foo\n",
		"input.schema.json": validInputSchema,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("expected source error, got: %v", err)
	}
}

func TestParseToolDir_MissingSourceOnTask(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         "description: test\ntype: task\n",
		"input.schema.json": validInputSchema,
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("expected source error, got: %v", err)
	}
}

func TestParseToolDir_SourceOnNativeTool(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         "description: test\ntype: native\nsource: foo\n",
		"input.schema.json": validInputSchema,
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("expected source error, got: %v", err)
	}
}

func TestParseToolDir_EnvOnNativeTool(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "web.lookup")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         "description: test\ntype: native\nenv:\n  - API_TOKEN\n",
		"input.schema.json": validInputSchema,
	})

	def, err := ParseToolDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(def.ToolYAML.Env) != 1 || def.ToolYAML.Env[0] != "API_TOKEN" {
		t.Fatalf("env = %v, want [API_TOKEN]", def.ToolYAML.Env)
	}
}

func TestParseToolDir_UnknownYAMLKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         "description: test\ntype: shell\nfoo: bar\n",
		"input.schema.json": validInputSchema,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	_, err := ParseToolDir(dir)
	if err == nil {
		t.Fatal("expected unknown key error, got nil")
	}
}

func TestParseToolDir_ShellMissingRunSh(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         validShellYAML,
		"input.schema.json": validInputSchema,
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "run.sh") {
		t.Fatalf("expected run.sh error, got: %v", err)
	}
}

func TestParseToolDir_TaskWithRunSh(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         validTaskYAML,
		"input.schema.json": validInputSchema,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "run.sh") {
		t.Fatalf("expected run.sh error, got: %v", err)
	}
}

func TestParseToolDir_NativeWithRunSh(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         validNativeYAML,
		"input.schema.json": validInputSchema,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "run.sh") {
		t.Fatalf("expected run.sh error, got: %v", err)
	}
}

func TestParseToolDir_MissingInputSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml": validShellYAML,
		"run.sh":    "#!/bin/sh\necho '{}'",
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "input.schema.json") {
		t.Fatalf("expected input.schema.json error, got: %v", err)
	}
}

func TestParseToolDir_NonObjectRootSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         validShellYAML,
		"input.schema.json": `{"type":"array"}`,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "object") {
		t.Fatalf("expected object type error, got: %v", err)
	}
}

func TestParseToolDir_MalformedInputSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         validShellYAML,
		"input.schema.json": `{invalid`,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("expected parse error, got: %v", err)
	}
}

func TestParseToolDir_OutputSchemaLoaded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tool.with.output")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":          validShellYAML,
		"input.schema.json":  validInputSchema,
		"output.schema.json": `{"type":"object","properties":{"result":{"type":"string"}}}`,
		"run.sh":             "#!/bin/sh\necho '{}'",
	})

	def, err := ParseToolDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if def.OutputSchema == nil {
		t.Fatal("OutputSchema should not be nil when output.schema.json exists")
	}
}

func TestParseToolDir_OutputSchemaAbsent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tool.no.output")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         validShellYAML,
		"input.schema.json": validInputSchema,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	def, err := ParseToolDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if def.OutputSchema != nil {
		t.Error("OutputSchema should be nil when output.schema.json is absent")
	}
}

func TestParseToolDir_ProviderSchemaPassthrough(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tool.passthrough")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         validShellYAML,
		"input.schema.json": validInputSchema,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	def, err := ParseToolDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(def.ProviderSchema) != string(def.AuthorSchema) {
		t.Error("ProviderSchema should be identical to AuthorSchema in v1 passthrough")
	}
}

func TestAppToolDef_ToToolDef(t *testing.T) {
	def := &AppToolDef{
		ID: "my.tool",
		ToolYAML: ToolYAML{
			Description: "My tool description",
		},
		ProviderSchema: json.RawMessage(`{"type":"object"}`),
	}

	td := def.ToToolDef()
	if td.ID != "my.tool" {
		t.Errorf("ID = %q", td.ID)
	}
	if td.Description != "My tool description" {
		t.Errorf("Description = %q", td.Description)
	}
	if string(td.InputSchema) != `{"type":"object"}` {
		t.Errorf("InputSchema = %s", td.InputSchema)
	}
}

func TestParseToolDir_InvalidCacheValue(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bad.tool")
	writeToolDir(t, dir, map[string]string{
		"tool.yaml":         "description: test\ntype: shell\ncache: always\n",
		"input.schema.json": validInputSchema,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	_, err := ParseToolDir(dir)
	if err == nil || !strings.Contains(err.Error(), "cache") {
		t.Fatalf("expected cache error, got: %v", err)
	}
}
