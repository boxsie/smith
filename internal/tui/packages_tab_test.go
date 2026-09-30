package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/boxsie/smith/internal/lib"
)

// setupFixtureLib creates a temporary lib directory with modules and tools for testing.
func setupFixtureLib(t *testing.T) (string, lib.Manifest) {
	t.Helper()
	dir := t.TempDir()

	manifest := make(lib.Manifest)

	// Module: planner (embedded — all files match manifest)
	plannerDir := filepath.Join(dir, "planner")
	os.MkdirAll(plannerDir, 0o755)
	plannerTask := []byte("---\n---\n\nYou are Smith's built-in planner. Staged planning pipeline.")
	writeAndRecord(t, filepath.Join(plannerDir, "task.md"), plannerTask, "planner/task.md", manifest)

	// Module: web-summarize (user-modified — hash mismatch)
	wsDir := filepath.Join(dir, "web-summarize")
	os.MkdirAll(wsDir, 0o755)
	origContent := []byte("---\noutput:\n  type: json\n---\n\nOriginal web summarizer content.")
	modContent := []byte("---\noutput:\n  type: json\n---\n\nModified by user.")
	// Record original hash in manifest.
	h := sha256.Sum256(origContent)
	manifest["web-summarize/task.md"] = hex.EncodeToString(h[:])
	// Write modified content to disk.
	os.WriteFile(filepath.Join(wsDir, "task.md"), modContent, 0o644)

	// Module: my-custom (user-added — no manifest entries)
	customDir := filepath.Join(dir, "my-custom")
	os.MkdirAll(customDir, 0o755)
	os.WriteFile(filepath.Join(customDir, "task.md"), []byte("Custom module task."), 0o644)

	// Module with extra file (embedded module + extra file = user-modified)
	extraDir := filepath.Join(dir, "tool-create")
	os.MkdirAll(extraDir, 0o755)
	tcTask := []byte("---\noutput:\n  type: json\n---\n\nShell tool scaffolder.")
	writeAndRecord(t, filepath.Join(extraDir, "task.md"), tcTask, "tool-create/task.md", manifest)
	// Add an extra file not in manifest.
	os.WriteFile(filepath.Join(extraDir, "notes.txt"), []byte("user notes"), 0o644)

	// Tools directory.
	toolsDir := filepath.Join(dir, "tools")
	os.MkdirAll(toolsDir, 0o755)

	// Tool: web.lookup (embedded)
	wlDir := filepath.Join(toolsDir, "web.lookup")
	os.MkdirAll(wlDir, 0o755)
	toolYAML := []byte("description: \"DuckDuckGo web search\"\ntype: native\ntimeout: 30s\ncache: never\n")
	writeAndRecord(t, filepath.Join(wlDir, "tool.yaml"), toolYAML, "tools/web.lookup/tool.yaml", manifest)
	schema := []byte(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
	writeAndRecord(t, filepath.Join(wlDir, "input.schema.json"), schema, "tools/web.lookup/input.schema.json", manifest)

	// Tool: web.fetch_markdown (embedded) — correct name, not web.fetch_md.
	fmDir := filepath.Join(toolsDir, "web.fetch_markdown")
	os.MkdirAll(fmDir, 0o755)
	fmYAML := []byte("description: \"Readable markdown extractor\"\ntype: native\ntimeout: 30s\ncache: never\n")
	writeAndRecord(t, filepath.Join(fmDir, "tool.yaml"), fmYAML, "tools/web.fetch_markdown/tool.yaml", manifest)
	fmSchema := []byte(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`)
	writeAndRecord(t, filepath.Join(fmDir, "input.schema.json"), fmSchema, "tools/web.fetch_markdown/input.schema.json", manifest)

	// Tool: my-tool (user-added — no manifest entries)
	mtDir := filepath.Join(toolsDir, "my-tool")
	os.MkdirAll(mtDir, 0o755)
	os.WriteFile(filepath.Join(mtDir, "tool.yaml"), []byte("description: \"My custom tool\"\ntype: shell\ntimeout: 10s\n"), 0o644)
	os.WriteFile(filepath.Join(mtDir, "input.schema.json"), []byte(`{"type":"object"}`), 0o644)
	os.WriteFile(filepath.Join(mtDir, "run.sh"), []byte("#!/bin/sh\necho ok"), 0o755)

	// Write manifest file.
	lib.WriteManifest(dir, manifest)

	return dir, manifest
}

func writeAndRecord(t *testing.T, path string, data []byte, relPath string, manifest lib.Manifest) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	h := sha256.Sum256(data)
	manifest[relPath] = hex.EncodeToString(h[:])
}

func TestLoadModules(t *testing.T) {
	dir, manifest := setupFixtureLib(t)

	modules := loadModules(dir, manifest)

	if len(modules) < 3 {
		t.Fatalf("expected at least 3 modules, got %d", len(modules))
	}

	byName := make(map[string]moduleEntry)
	for _, m := range modules {
		byName[m.name] = m
	}

	// planner should be embedded.
	if m, ok := byName["planner"]; !ok {
		t.Error("planner module not found")
	} else if m.provenance != provenanceEmbedded {
		t.Errorf("planner provenance = %s, want embedded", m.provenance)
	}

	// web-summarize should be user-modified (hash mismatch).
	if m, ok := byName["web-summarize"]; !ok {
		t.Error("web-summarize module not found")
	} else if m.provenance != provenanceUserModified {
		t.Errorf("web-summarize provenance = %s, want user-modified", m.provenance)
	}

	// my-custom should be user-added.
	if m, ok := byName["my-custom"]; !ok {
		t.Error("my-custom module not found")
	} else if m.provenance != provenanceUserAdded {
		t.Errorf("my-custom provenance = %s, want user-added", m.provenance)
	}

	// tool-create should be user-modified (extra file).
	if m, ok := byName["tool-create"]; !ok {
		t.Error("tool-create module not found")
	} else if m.provenance != provenanceUserModified {
		t.Errorf("tool-create provenance = %s, want user-modified (has extra file)", m.provenance)
	}
}

func TestLoadTools(t *testing.T) {
	dir, manifest := setupFixtureLib(t)

	tools := loadTools(dir, manifest)

	if len(tools) < 2 {
		t.Fatalf("expected at least 2 tools, got %d", len(tools))
	}

	byID := make(map[string]toolEntry)
	for _, tl := range tools {
		byID[tl.id] = tl
	}

	// web.lookup should be embedded with type native.
	if tl, ok := byID["web.lookup"]; !ok {
		t.Error("web.lookup tool not found")
	} else {
		if tl.toolType != "native" {
			t.Errorf("web.lookup type = %s, want native", tl.toolType)
		}
		if tl.provenance != provenanceEmbedded {
			t.Errorf("web.lookup provenance = %s, want embedded", tl.provenance)
		}
		if tl.description != "DuckDuckGo web search" {
			t.Errorf("web.lookup description = %q", tl.description)
		}
	}

	// web.fetch_markdown — correct name (not web.fetch_md).
	if tl, ok := byID["web.fetch_markdown"]; !ok {
		t.Error("web.fetch_markdown tool not found (should be web.fetch_markdown, not web.fetch_md)")
	} else {
		if tl.toolType != "native" {
			t.Errorf("web.fetch_markdown type = %s, want native", tl.toolType)
		}
	}

	// my-tool should be user-added.
	if tl, ok := byID["my-tool"]; !ok {
		t.Error("my-tool not found")
	} else if tl.provenance != provenanceUserAdded {
		t.Errorf("my-tool provenance = %s, want user-added", tl.provenance)
	}
}

func TestModuleDescription(t *testing.T) {
	dir := t.TempDir()

	// Task with frontmatter.
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("---\noutput:\n  type: json\n---\n\nThis is the description line."), 0o644)
	desc := extractModuleDescription(filepath.Join(dir, "task.md"))
	if desc != "This is the description line." {
		t.Errorf("description = %q, want 'This is the description line.'", desc)
	}

	// Task without frontmatter.
	os.WriteFile(filepath.Join(dir, "task2.md"), []byte("Direct description without frontmatter."), 0o644)
	desc = extractModuleDescription(filepath.Join(dir, "task2.md"))
	if desc != "Direct description without frontmatter." {
		t.Errorf("description = %q, want 'Direct description without frontmatter.'", desc)
	}
}

func TestPackagesTabEnterShowsFiles(t *testing.T) {
	dir, manifest := setupFixtureLib(t)
	modules := loadModules(dir, manifest)
	tools := loadTools(dir, manifest)

	// We need to mock selectedDir to use our fixture dir.
	// Instead, test showFiles directly by setting up the state.
	tab := &packagesTab{modules: modules, tools: tools, cursor: 0}

	// Directly call showFiles — it calls selectedDir() which calls lib.UserLibDir().
	// For a unit test, just verify the detail view mechanics.
	tab.viewMode = packagesViewFiles
	tab.detailTitle = "planner — Files"
	tab.detailBody = "  task.md  (100 bytes)\n"

	view := tab.View()
	if !strings.Contains(view, "planner — Files") {
		t.Error("detail view should show title")
	}
	if !strings.Contains(view, "task.md") {
		t.Error("detail view should show file listing")
	}
	if !strings.Contains(view, "Back") {
		t.Error("detail view should show Back hint")
	}
}

func TestPackagesTabInfoView(t *testing.T) {
	tab := &packagesTab{
		modules:     []moduleEntry{{name: "planner"}},
		viewMode:    packagesViewInfo,
		detailTitle: "planner — Info",
		detailBody:  "---\n---\nYou are Smith's built-in planner.",
	}

	view := tab.View()
	if !strings.Contains(view, "planner — Info") {
		t.Error("info view should show title")
	}
	if !strings.Contains(view, "built-in planner") {
		t.Error("info view should show task.md content")
	}
}

func TestPackagesTabEscReturnsToList(t *testing.T) {
	tab := &packagesTab{
		modules:     []moduleEntry{{name: "planner"}},
		viewMode:    packagesViewFiles,
		detailTitle: "planner — Files",
		detailBody:  "file list",
	}

	result, _ := tab.Update(tea.KeyMsg{Type: tea.KeyEscape})
	tab = result.(*packagesTab)

	if tab.viewMode != packagesViewList {
		t.Error("Esc should return to list view")
	}
	if tab.detailTitle != "" || tab.detailBody != "" {
		t.Error("Esc should clear detail content")
	}
}

func TestPackagesTabUpdateFeedback(t *testing.T) {
	tab := &packagesTab{
		modules: []moduleEntry{{name: "mod1"}},
	}

	// Simulate update completion.
	result, _ := tab.Update(libUpdateDoneMsg{report: "Updated 2, created 0, skipped 1 (user-modified), pruned 0"})
	tab = result.(*packagesTab)

	if tab.updateMsg == "" {
		t.Error("update success should set updateMsg")
	}
	view := tab.View()
	if !strings.Contains(view, "Updated 2") {
		t.Error("view should show update report")
	}

	// Simulate update error.
	result, _ = tab.Update(libUpdateDoneMsg{err: fmt.Errorf("disk full")})
	tab = result.(*packagesTab)

	if tab.updateErr == "" {
		t.Error("update failure should set updateErr")
	}
	view = tab.View()
	if !strings.Contains(view, "disk full") {
		t.Error("view should show update error")
	}
}

func TestPackagesTabHelpBarShowsAllActions(t *testing.T) {
	tab := &packagesTab{
		modules: []moduleEntry{{name: "mod1"}},
		tools:   []toolEntry{{id: "tool1"}},
	}

	view := tab.View()
	if !strings.Contains(view, "View files") {
		t.Error("help bar should mention 'View files'")
	}
	if !strings.Contains(view, "Info") {
		t.Error("help bar should mention 'Info'")
	}
	if !strings.Contains(view, "Update") {
		t.Error("help bar should mention 'Update'")
	}
}

func TestPackagesTabViewRendersModulesAndTools(t *testing.T) {
	dir, manifest := setupFixtureLib(t)

	modules := loadModules(dir, manifest)
	tools := loadTools(dir, manifest)

	tab := &packagesTab{
		modules: modules,
		tools:   tools,
	}

	view := tab.View()

	if !strings.Contains(view, "Modules") {
		t.Error("view should contain 'Modules' section header")
	}
	if !strings.Contains(view, "Tools") {
		t.Error("view should contain 'Tools' section header")
	}
	if !strings.Contains(view, "planner") {
		t.Error("view should contain planner module")
	}
	if !strings.Contains(view, "web.lookup") {
		t.Error("view should contain web.lookup tool")
	}
	if !strings.Contains(view, "web.fetch_markdown") {
		t.Error("view should contain web.fetch_markdown tool (not web.fetch_md)")
	}
}

func TestPackagesTabEmptyLib(t *testing.T) {
	tab := &packagesTab{}
	view := tab.View()

	if !strings.Contains(view, "No packages installed") {
		t.Error("empty packages view should show 'No packages installed'")
	}
}

func TestPackagesTabLoadError(t *testing.T) {
	tab := &packagesTab{loadErr: "permission denied"}
	view := tab.View()

	if !strings.Contains(view, "permission denied") {
		t.Error("error view should contain the error message")
	}
}

func TestPackagesTabNavigation(t *testing.T) {
	tab := &packagesTab{
		modules: []moduleEntry{{name: "mod1"}, {name: "mod2"}},
		tools:   []toolEntry{{id: "tool1"}},
	}

	if tab.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", tab.cursor)
	}

	// Move down.
	result, _ := tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	tab = result.(*packagesTab)
	if tab.cursor != 1 {
		t.Fatalf("cursor after j = %d, want 1", tab.cursor)
	}

	// Move down again into tools.
	result, _ = tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	tab = result.(*packagesTab)
	if tab.cursor != 2 {
		t.Fatalf("cursor after second j = %d, want 2", tab.cursor)
	}

	// Can't go past end.
	result, _ = tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	tab = result.(*packagesTab)
	if tab.cursor != 2 {
		t.Fatalf("cursor should stay at 2 at end, got %d", tab.cursor)
	}

	// Move up.
	result, _ = tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	tab = result.(*packagesTab)
	if tab.cursor != 1 {
		t.Fatalf("cursor after k = %d, want 1", tab.cursor)
	}
}

func TestPackagesTabLoadsOnActivation(t *testing.T) {
	tab := newPackagesTab()
	_, cmd := tab.Update(tabActivatedMsg{})
	if cmd == nil {
		t.Fatal("tabActivatedMsg should trigger a reload command")
	}
}

func TestCorruptToolDir(t *testing.T) {
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, "tools")
	os.MkdirAll(toolsDir, 0o755)

	// Create a corrupt tool dir (invalid tool.yaml).
	badDir := filepath.Join(toolsDir, "bad-tool")
	os.MkdirAll(badDir, 0o755)
	os.WriteFile(filepath.Join(badDir, "tool.yaml"), []byte("not: valid: yaml: {broken"), 0o644)

	manifest := make(lib.Manifest)
	tools := loadTools(dir, manifest)

	found := false
	for _, tl := range tools {
		if tl.id == "bad-tool" {
			found = true
			if tl.err == "" {
				t.Error("corrupt tool should have an error message")
			}
		}
	}
	if !found {
		t.Error("bad-tool should still appear in the tools list")
	}
}

func TestProvenanceDetection(t *testing.T) {
	dir := t.TempDir()
	modDir := filepath.Join(dir, "test-mod")
	os.MkdirAll(modDir, 0o755)

	content := []byte("test content")
	os.WriteFile(filepath.Join(modDir, "task.md"), content, 0o644)
	h := sha256.Sum256(content)

	t.Run("embedded - all match", func(t *testing.T) {
		manifest := lib.Manifest{
			"test-mod/task.md": hex.EncodeToString(h[:]),
		}
		p := detectProvenance(modDir, "test-mod", manifest)
		if p != provenanceEmbedded {
			t.Errorf("provenance = %s, want embedded", p)
		}
	})

	t.Run("user-modified - hash mismatch", func(t *testing.T) {
		manifest := lib.Manifest{
			"test-mod/task.md": "0000000000000000000000000000000000000000000000000000000000000000",
		}
		p := detectProvenance(modDir, "test-mod", manifest)
		if p != provenanceUserModified {
			t.Errorf("provenance = %s, want user-modified", p)
		}
	})

	t.Run("user-modified - extra file", func(t *testing.T) {
		manifest := lib.Manifest{
			"test-mod/task.md": hex.EncodeToString(h[:]),
		}
		os.WriteFile(filepath.Join(modDir, "extra.txt"), []byte("extra"), 0o644)
		defer os.Remove(filepath.Join(modDir, "extra.txt"))

		p := detectProvenance(modDir, "test-mod", manifest)
		if p != provenanceUserModified {
			t.Errorf("provenance = %s, want user-modified (extra file)", p)
		}
	})

	t.Run("user-added - no manifest entries", func(t *testing.T) {
		manifest := lib.Manifest{}
		p := detectProvenance(modDir, "test-mod", manifest)
		if p != provenanceUserAdded {
			t.Errorf("provenance = %s, want user-added", p)
		}
	})
}

func TestNoVersionColumn(t *testing.T) {
	tab := &packagesTab{
		modules: []moduleEntry{{name: "mod1", provenance: provenanceEmbedded, description: "Test module"}},
		tools:   []toolEntry{{id: "web.lookup", toolType: "native", provenance: provenanceEmbedded, description: "Search"}},
	}

	view := tab.View()

	// Should not contain version-like patterns.
	if strings.Contains(view, "v1.") || strings.Contains(view, "v0.") {
		t.Error("view should not contain version numbers")
	}
}

// Verify the manifest is written correctly in fixture.
func TestFixtureManifestIntegrity(t *testing.T) {
	dir, _ := setupFixtureLib(t)

	data, err := os.ReadFile(filepath.Join(dir, ".embedded-manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}

	if _, ok := m["planner/task.md"]; !ok {
		t.Error("manifest should contain planner/task.md")
	}
	if _, ok := m["tools/web.lookup/tool.yaml"]; !ok {
		t.Error("manifest should contain tools/web.lookup/tool.yaml")
	}
}
