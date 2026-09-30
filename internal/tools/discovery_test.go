package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupProject creates a temp project with the given tool directories.
// Each entry in toolDefs maps "tools/<id>" to a map of file contents.
func setupProject(t *testing.T, toolDefs map[string]map[string]string) string {
	t.Helper()
	isolateUserLib(t)
	root := t.TempDir()
	for relDir, files := range toolDefs {
		dir := filepath.Join(root, relDir)
		writeToolDir(t, dir, files)
	}
	return root
}

func isolateUserLib(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestDiscover_AppLocalTool(t *testing.T) {
	root := setupProject(t, map[string]map[string]string{
		"tools/foo.bar": {
			"tool.yaml":         validShellYAML,
			"input.schema.json": validInputSchema,
			"run.sh":            "#!/bin/sh\necho '{}'",
		},
	})

	resolved, err := Discover(root, BuiltinToolIDs, NativeToolIDs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := resolved.AppDefs["foo.bar"]; !ok {
		t.Fatal("expected foo.bar to be discovered")
	}
	if resolved.Sources["foo.bar"] != "app" {
		t.Errorf("source = %q, want app", resolved.Sources["foo.bar"])
	}
}

func TestDiscover_LibTool(t *testing.T) {
	isolateUserLib(t)
	root := t.TempDir()
	libDir := filepath.Join(root, ".smith", "lib", "tools", "lib.tool")
	writeToolDir(t, libDir, map[string]string{
		"tool.yaml":         validShellYAML,
		"input.schema.json": validInputSchema,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	resolved, err := Discover(root, BuiltinToolIDs, NativeToolIDs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := resolved.AppDefs["lib.tool"]; !ok {
		t.Fatal("expected lib.tool to be discovered")
	}
	if !strings.HasPrefix(resolved.Sources["lib.tool"], "lib:") {
		t.Errorf("source = %q, want lib:* prefix", resolved.Sources["lib.tool"])
	}
}

func TestDiscover_AppShadowsLib(t *testing.T) {
	isolateUserLib(t)
	root := t.TempDir()

	// App-local version.
	appDir := filepath.Join(root, "tools", "my.tool")
	writeToolDir(t, appDir, map[string]string{
		"tool.yaml":         `description: "App version"` + "\ntype: shell\n",
		"input.schema.json": validInputSchema,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	// Lib version.
	libDir := filepath.Join(root, ".smith", "lib", "tools", "my.tool")
	writeToolDir(t, libDir, map[string]string{
		"tool.yaml":         `description: "Lib version"` + "\ntype: shell\n",
		"input.schema.json": validInputSchema,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	resolved, err := Discover(root, BuiltinToolIDs, NativeToolIDs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	def := resolved.AppDefs["my.tool"]
	if def == nil {
		t.Fatal("expected my.tool to be discovered")
	}
	if def.ToolYAML.Description != "App version" {
		t.Errorf("got description %q, want 'App version' (app should shadow lib)", def.ToolYAML.Description)
	}
	if resolved.Sources["my.tool"] != "app" {
		t.Errorf("source = %q, want app", resolved.Sources["my.tool"])
	}
}

func TestDiscover_BuiltinShadowingForbidden(t *testing.T) {
	root := setupProject(t, map[string]map[string]string{
		"tools/project.read": {
			"tool.yaml":         validShellYAML,
			"input.schema.json": validInputSchema,
			"run.sh":            "#!/bin/sh\necho '{}'",
		},
	})

	_, err := Discover(root, BuiltinToolIDs, NativeToolIDs())
	if err == nil || !strings.Contains(err.Error(), "shadows") {
		t.Fatalf("expected shadowing error, got: %v", err)
	}
}

func TestDiscover_BareFileInToolsDir(t *testing.T) {
	isolateUserLib(t)
	root := t.TempDir()
	toolsDir := filepath.Join(root, "tools")
	os.MkdirAll(toolsDir, 0o755)
	os.WriteFile(filepath.Join(toolsDir, "stray.txt"), []byte("bad"), 0o644)

	_, err := Discover(root, BuiltinToolIDs, NativeToolIDs())
	if err == nil || !strings.Contains(err.Error(), "directories") {
		t.Fatalf("expected bare file error, got: %v", err)
	}
}

func TestDiscover_EmptySubdirectory(t *testing.T) {
	isolateUserLib(t)
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "tools", "empty.tool"), 0o755)

	_, err := Discover(root, BuiltinToolIDs, NativeToolIDs())
	if err == nil || !strings.Contains(err.Error(), "tool.yaml") {
		t.Fatalf("expected tool.yaml error, got: %v", err)
	}
}

func TestDiscover_NoToolsDirectory(t *testing.T) {
	isolateUserLib(t)
	root := t.TempDir()

	resolved, err := Discover(root, BuiltinToolIDs, NativeToolIDs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved.AppDefs) != 0 {
		t.Errorf("expected empty AppDefs, got %d entries", len(resolved.AppDefs))
	}
}

func TestDiscover_ToolIDSetFromDirName(t *testing.T) {
	root := setupProject(t, map[string]map[string]string{
		"tools/my.custom.tool": {
			"tool.yaml":         validShellYAML,
			"input.schema.json": validInputSchema,
			"run.sh":            "#!/bin/sh\necho '{}'",
		},
	})

	resolved, err := Discover(root, BuiltinToolIDs, NativeToolIDs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	def := resolved.AppDefs["my.custom.tool"]
	if def == nil {
		t.Fatal("expected my.custom.tool")
	}
	if def.ID != "my.custom.tool" {
		t.Errorf("ID = %q, want my.custom.tool", def.ID)
	}
}

func TestDiscover_UnknownNativeToolRejected(t *testing.T) {
	root := setupProject(t, map[string]map[string]string{
		"tools/custom.native": {
			"tool.yaml":         validNativeYAML,
			"input.schema.json": validInputSchema,
		},
	})

	_, err := Discover(root, BuiltinToolIDs, NativeToolIDs())
	if err == nil || !strings.Contains(err.Error(), "native") {
		t.Fatalf("expected native handler registration error, got: %v", err)
	}
}

func TestDiscover_AppShellShadowsLibNative(t *testing.T) {
	isolateUserLib(t)
	root := t.TempDir()

	appDir := filepath.Join(root, "tools", "my.tool")
	writeToolDir(t, appDir, map[string]string{
		"tool.yaml":         `description: "App shell"` + "\ntype: shell\n",
		"input.schema.json": validInputSchema,
		"run.sh":            "#!/bin/sh\necho '{}'",
	})

	libDir := filepath.Join(root, ".smith", "lib", "tools", "my.tool")
	writeToolDir(t, libDir, map[string]string{
		"tool.yaml":         `description: "Lib native"` + "\ntype: native\n",
		"input.schema.json": validInputSchema,
	})

	nativeIDs := append(NativeToolIDs(), "my.tool")
	resolved, err := Discover(root, BuiltinToolIDs, nativeIDs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	def := resolved.AppDefs["my.tool"]
	if def == nil {
		t.Fatal("expected my.tool to be discovered")
	}
	if def.ToolYAML.Type != "shell" {
		t.Fatalf("type = %q, want shell", def.ToolYAML.Type)
	}
	if def.ToolYAML.Description != "App shell" {
		t.Fatalf("description = %q, want app shell definition", def.ToolYAML.Description)
	}
}
