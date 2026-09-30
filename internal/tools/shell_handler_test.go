package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupShellTool(t *testing.T, script string, extra map[string]string) (*ShellToolHandler, string) {
	t.Helper()
	root := t.TempDir()
	toolDir := filepath.Join(root, "tools", "test.tool")
	files := map[string]string{
		"tool.yaml":         "description: test tool\ntype: shell\n",
		"input.schema.json": `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`,
		"run.sh":            script,
	}
	for k, v := range extra {
		files[k] = v
	}
	writeToolDir(t, toolDir, files)
	os.Chmod(filepath.Join(toolDir, "run.sh"), 0o755)

	def, err := ParseToolDir(toolDir)
	if err != nil {
		t.Fatalf("parse tool dir: %v", err)
	}
	def.ID = "test.tool"

	handler, err := NewShellToolHandler(def, root)
	if err != nil {
		t.Fatalf("new shell handler: %v", err)
	}
	return handler, root
}

func TestShellHandler_ValidExecution(t *testing.T) {
	handler, root := setupShellTool(t, `#!/bin/sh
echo '{"result":"ok"}'`, nil)

	scope := map[string]string{ScopeRoot: root}
	out, err := handler.Execute(context.Background(), json.RawMessage(`{"query":"test"}`), scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]string
	json.Unmarshal(out, &result)
	if result["result"] != "ok" {
		t.Errorf("result = %v", result)
	}
}

func TestShellHandler_NonJSONStdout(t *testing.T) {
	handler, root := setupShellTool(t, `#!/bin/sh
echo 'not json'`, nil)

	scope := map[string]string{ScopeRoot: root}
	_, err := handler.Execute(context.Background(), json.RawMessage(`{"query":"test"}`), scope)
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("expected JSON error, got: %v", err)
	}
}

func TestShellHandler_NonzeroExit(t *testing.T) {
	handler, root := setupShellTool(t, `#!/bin/sh
echo "something failed" >&2
exit 1`, nil)

	scope := map[string]string{ScopeRoot: root}
	_, err := handler.Execute(context.Background(), json.RawMessage(`{"query":"test"}`), scope)
	if err == nil || !strings.Contains(err.Error(), "something failed") {
		t.Fatalf("expected stderr in error, got: %v", err)
	}
}

func TestShellHandler_InputValidation(t *testing.T) {
	handler, root := setupShellTool(t, `#!/bin/sh
echo '{"result":"should not reach"}'`, nil)

	scope := map[string]string{ScopeRoot: root}
	_, err := handler.Execute(context.Background(), json.RawMessage(`{"wrong":"field"}`), scope)
	if err == nil || !strings.Contains(err.Error(), "input validation") {
		t.Fatalf("expected input validation error, got: %v", err)
	}
}

func TestShellHandler_Timeout(t *testing.T) {
	root := t.TempDir()
	toolDir := filepath.Join(root, "tools", "slow.tool")
	writeToolDir(t, toolDir, map[string]string{
		"tool.yaml":         "description: slow\ntype: shell\ntimeout: 100ms\n",
		"input.schema.json": `{"type":"object"}`,
		"run.sh":            "#!/bin/sh\nsleep 60\necho '{}'",
	})
	os.Chmod(filepath.Join(toolDir, "run.sh"), 0o755)

	def, _ := ParseToolDir(toolDir)
	def.ID = "slow.tool"
	handler, _ := NewShellToolHandler(def, root)

	start := time.Now()
	scope := map[string]string{ScopeRoot: root}
	_, err := handler.Execute(context.Background(), json.RawMessage(`{}`), scope)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout handling took too long: %v", elapsed)
	}
}

func TestShellHandler_EnvFiltering(t *testing.T) {
	handler, root := setupShellTool(t, `#!/bin/sh
env | sort`, nil)

	// Set a var that should NOT be visible.
	t.Setenv("SECRET_KEY", "should-not-see")

	scope := map[string]string{ScopeRoot: root}
	// This will fail because env output isn't JSON, but we can check the error
	// message doesn't contain our secret.
	_, err := handler.Execute(context.Background(), json.RawMessage(`{"query":"test"}`), scope)
	if err == nil {
		t.Fatal("expected error (env output is not JSON)")
	}
	// The error or any output should not contain the secret.
	if strings.Contains(err.Error(), "should-not-see") {
		t.Error("SECRET_KEY leaked through environment")
	}
}

func TestShellHandler_SmithToolID(t *testing.T) {
	handler, root := setupShellTool(t, `#!/bin/sh
printf '{"tool_id":"%s"}' "$SMITH_TOOL_ID"`, nil)

	scope := map[string]string{ScopeRoot: root}
	out, err := handler.Execute(context.Background(), json.RawMessage(`{"query":"test"}`), scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]string
	json.Unmarshal(out, &result)
	if result["tool_id"] != "test.tool" {
		t.Errorf("SMITH_TOOL_ID = %q, want test.tool", result["tool_id"])
	}
}

func TestShellHandler_WorkingDirectory(t *testing.T) {
	handler, root := setupShellTool(t, `#!/bin/sh
printf '{"cwd":"%s"}' "$(pwd)"`, nil)

	scope := map[string]string{ScopeRoot: root}
	out, err := handler.Execute(context.Background(), json.RawMessage(`{"query":"test"}`), scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]string
	json.Unmarshal(out, &result)
	if result["cwd"] != root {
		t.Errorf("cwd = %q, want %q", result["cwd"], root)
	}
}

func TestShellHandler_CacheHit(t *testing.T) {
	// Use a side-effect file to count executions.
	root := t.TempDir()
	counterFile := filepath.Join(root, "counter")
	os.WriteFile(counterFile, []byte("0"), 0o644)

	toolDir := filepath.Join(root, "tools", "cached.tool")
	script := `#!/bin/sh
count=$(cat ` + counterFile + `)
count=$((count + 1))
echo "$count" > ` + counterFile + `
echo '{"count":'$count'}'`

	writeToolDir(t, toolDir, map[string]string{
		"tool.yaml":         "description: cached\ntype: shell\ncache: auto\n",
		"input.schema.json": `{"type":"object"}`,
		"run.sh":            script,
	})
	os.Chmod(filepath.Join(toolDir, "run.sh"), 0o755)

	def, _ := ParseToolDir(toolDir)
	def.ID = "cached.tool"
	handler, _ := NewShellToolHandler(def, root)
	scope := map[string]string{ScopeRoot: root}

	// First call: should execute.
	_, err := handler.Execute(context.Background(), json.RawMessage(`{}`), scope)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Second call: should be cached.
	out, err := handler.Execute(context.Background(), json.RawMessage(`{}`), scope)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	// Counter should still be 1 (script not re-executed).
	var result map[string]int
	json.Unmarshal(out, &result)
	if result["count"] != 1 {
		t.Errorf("count = %d, want 1 (cache should have prevented second execution)", result["count"])
	}
}

func TestShellHandler_CacheNever(t *testing.T) {
	root := t.TempDir()
	counterFile := filepath.Join(root, "counter")
	os.WriteFile(counterFile, []byte("0"), 0o644)

	toolDir := filepath.Join(root, "tools", "nocache.tool")
	script := `#!/bin/sh
count=$(cat ` + counterFile + `)
count=$((count + 1))
echo "$count" > ` + counterFile + `
echo '{"count":'$count'}'`

	writeToolDir(t, toolDir, map[string]string{
		"tool.yaml":         "description: nocache\ntype: shell\ncache: never\n",
		"input.schema.json": `{"type":"object"}`,
		"run.sh":            script,
	})
	os.Chmod(filepath.Join(toolDir, "run.sh"), 0o755)

	def, _ := ParseToolDir(toolDir)
	def.ID = "nocache.tool"
	handler, _ := NewShellToolHandler(def, root)
	scope := map[string]string{ScopeRoot: root}

	handler.Execute(context.Background(), json.RawMessage(`{}`), scope)
	out, err := handler.Execute(context.Background(), json.RawMessage(`{}`), scope)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	var result map[string]int
	json.Unmarshal(out, &result)
	if result["count"] != 2 {
		t.Errorf("count = %d, want 2 (cache: never should always execute)", result["count"])
	}
}

func TestShellHandler_OutputSchemaViolation(t *testing.T) {
	handler, root := setupShellTool(t, `#!/bin/sh
echo '{"wrong":"shape"}'`, map[string]string{
		"output.schema.json": `{"type":"object","properties":{"result":{"type":"string"}},"required":["result"]}`,
	})

	scope := map[string]string{ScopeRoot: root}
	_, err := handler.Execute(context.Background(), json.RawMessage(`{"query":"test"}`), scope)
	if err == nil || !strings.Contains(err.Error(), "output validation") {
		t.Fatalf("expected output validation error, got: %v", err)
	}
}

func TestShellHandler_Definition(t *testing.T) {
	handler, _ := setupShellTool(t, `#!/bin/sh
echo '{}'`, nil)

	def := handler.Definition()
	if def.ID != "test.tool" {
		t.Errorf("ID = %q", def.ID)
	}
	if def.Description != "test tool" {
		t.Errorf("Description = %q", def.Description)
	}
	if def.InputSchema == nil {
		t.Error("InputSchema is nil")
	}
}

func TestBoundStderr_Long(t *testing.T) {
	// Generate 100 lines of stderr.
	var lines []string
	for i := range 100 {
		lines = append(lines, strings.Repeat("x", 50)+string(rune('0'+i%10)))
	}
	stderr := strings.Join(lines, "\n")
	result := boundStderr([]byte(stderr))

	resultLines := strings.Split(result, "\n")
	if len(resultLines) > 40 {
		t.Errorf("got %d lines, want <= 40", len(resultLines))
	}
}
