package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/lib"
)

func TestInputSummary(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   string
	}{
		{
			name:   "required and optional fields",
			schema: `{"type":"object","properties":{"query":{"type":"string"},"status":{"type":"string"}},"required":["query"]}`,
			want:   "[query*, status]",
		},
		{
			name:   "all required",
			schema: `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a","b"]}`,
			want:   "[a*, b*]",
		},
		{
			name:   "no required",
			schema: `{"type":"object","properties":{"path":{"type":"string"}}}`,
			want:   "[path]",
		},
		{
			name:   "empty schema",
			schema: `{}`,
			want:   "[]",
		},
		{
			name:   "nil schema",
			schema: "",
			want:   "[]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var schema json.RawMessage
			if tt.schema != "" {
				schema = json.RawMessage(tt.schema)
			}
			got := inputSummary(schema)
			if got != tt.want {
				t.Errorf("inputSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatToolInventoryBuiltinsOnly(t *testing.T) {
	// Empty dir with no app tools — should still list built-ins.
	dir := t.TempDir()
	inv := formatToolInventory(dir)

	if !strings.Contains(inv, "Tools:") {
		t.Error("expected Tools: header")
	}
	for _, id := range []string{"project.read", "project.list", "project.find"} {
		if !strings.Contains(inv, id) {
			t.Errorf("expected built-in tool %q in inventory", id)
		}
	}
	// Built-ins should show (builtin) format.
	if !strings.Contains(inv, "(builtin)") {
		t.Error("expected (builtin) label for built-in tools")
	}
}

func TestFormatToolInventoryWithAppTools(t *testing.T) {
	dir := t.TempDir()

	// Create an app tool.
	toolDir := filepath.Join(dir, "tools", "issue.search")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolDir, "tool.yaml"), []byte("description: Search issues\ntype: shell\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolDir, "input.schema.json"), []byte(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolDir, "run.sh"), []byte("#!/bin/sh\necho '{}'"), 0o755); err != nil {
		t.Fatal(err)
	}

	inv := formatToolInventory(dir)

	if !strings.Contains(inv, "issue.search") {
		t.Error("expected app tool issue.search in inventory")
	}
	if !strings.Contains(inv, "(app, shell)") {
		t.Error("expected (app, shell) label for app tool")
	}
	// Also still has built-ins.
	if !strings.Contains(inv, "project.read") {
		t.Error("expected built-in project.read in inventory")
	}
}

func TestGreenfieldSummaryIncludesToolInventory(t *testing.T) {
	dir := t.TempDir()
	summary, err := BuildProjectSummary(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary.Text, "Tools:") {
		t.Error("greenfield summary should include tool inventory")
	}
	if !strings.Contains(summary.Text, "project.read") {
		t.Error("greenfield summary should list built-in tools")
	}
}

func TestRepairModeSummaryIncludesToolInventory(t *testing.T) {
	dir := t.TempDir()
	// Create a broken task tree (task.md but invalid content).
	if err := os.WriteFile(filepath.Join(dir, "task.md"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a subtask with missing task.md to trigger validation failure.
	subtaskDir := filepath.Join(dir, "subtasks", "01-broken")
	if err := os.MkdirAll(subtaskDir, 0o755); err != nil {
		t.Fatal(err)
	}

	summary, err := BuildProjectSummary(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary.Text, "Tools:") {
		t.Error("repair-mode summary should include tool inventory")
	}
}

func TestDiscoveryFailureStillListsBuiltins(t *testing.T) {
	dir := t.TempDir()

	// Create a broken tool directory (bare file in tools/).
	toolsDir := filepath.Join(dir, "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolsDir, "bad-file.txt"), []byte("oops"), 0o644); err != nil {
		t.Fatal(err)
	}

	inv := formatToolInventory(dir)

	// Should have a warning about discovery.
	if !strings.Contains(inv, "discovery warning") {
		t.Error("expected discovery warning in inventory")
	}
	// Should still list built-ins.
	if !strings.Contains(inv, "project.read") {
		t.Error("expected built-in tools despite discovery failure")
	}
}

func TestFormatToolInventoryWithInstalledLibWebTools(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := lib.Init(filepath.Join(home, ".smith", "lib"), false); err != nil {
		t.Fatalf("init lib: %v", err)
	}

	inv := formatToolInventory(dir)

	for _, id := range []string{"web.lookup", "web.fetch", "web.fetch_markdown", "web.summarize"} {
		if !strings.Contains(inv, id) {
			t.Errorf("expected installed lib tool %q in inventory", id)
		}
	}
	if !strings.Contains(inv, "web.lookup (lib, native)") {
		t.Error("expected web.lookup to appear as a lib native tool")
	}
	if !strings.Contains(inv, "web.summarize (lib, task)") {
		t.Error("expected web.summarize to appear as a lib task tool")
	}
}
