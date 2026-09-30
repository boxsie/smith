package lib

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInit_EmptyEmbedded(t *testing.T) {
	tmp := t.TempDir()
	report, err := Init(tmp, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	// With only .gitkeep embedded, should have at most 1 created file.
	if len(report.Skipped) > 0 {
		t.Errorf("unexpected skipped files: %v", report.Skipped)
	}
}

func TestInit_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	_, err := Init(tmp, false)
	if err != nil {
		t.Fatalf("Init 1: %v", err)
	}
	report, err := Init(tmp, false)
	if err != nil {
		t.Fatalf("Init 2: %v", err)
	}
	// Second run should find everything unchanged.
	if len(report.Created) > 0 {
		t.Errorf("second run created %d files, want 0", len(report.Created))
	}
}

func TestInit_SkipsUserModified(t *testing.T) {
	tmp := t.TempDir()
	_, err := Init(tmp, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Modify the .gitkeep file.
	gitkeepPath := filepath.Join(tmp, ".gitkeep")
	if _, err := os.Stat(gitkeepPath); err == nil {
		os.WriteFile(gitkeepPath, []byte("user content"), 0o644)

		report, err := Init(tmp, false)
		if err != nil {
			t.Fatalf("Init after modify: %v", err)
		}
		if len(report.Skipped) == 0 {
			t.Error("expected modified file to be skipped")
		}
	}
}

func TestInit_PrunesObsoleteFiles(t *testing.T) {
	tmp := t.TempDir()
	_, err := Init(tmp, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Simulate a file from a previous version that no longer exists in embedded.
	obsoletePath := filepath.Join(tmp, "planner", "tools.md")
	os.MkdirAll(filepath.Dir(obsoletePath), 0o755)
	os.WriteFile(obsoletePath, []byte("old content"), 0o644)

	// Add it to the manifest as if it was extracted.
	manifest, _ := ReadManifest(tmp)
	manifest["planner/tools.md"] = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	WriteManifest(tmp, manifest)

	// Re-init. The file should be pruned since it's in the old manifest
	// but not in the current embedded FS.
	report, err := Init(tmp, false)
	if err != nil {
		t.Fatalf("Init prune: %v", err)
	}

	found := false
	for _, p := range report.Pruned {
		if p == "planner/tools.md" {
			found = true
		}
	}
	if !found {
		t.Error("expected planner/tools.md to be pruned")
	}
	if _, err := os.Stat(obsoletePath); !os.IsNotExist(err) {
		t.Error("obsolete file should be deleted from disk")
	}
}

func TestInit_ForceOverwrites(t *testing.T) {
	tmp := t.TempDir()
	_, err := Init(tmp, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Modify the .gitkeep file.
	gitkeepPath := filepath.Join(tmp, ".gitkeep")
	if _, err := os.Stat(gitkeepPath); err == nil {
		os.WriteFile(gitkeepPath, []byte("user content"), 0o644)

		report, err := Init(tmp, true)
		if err != nil {
			t.Fatalf("Init --force: %v", err)
		}
		if len(report.Skipped) > 0 {
			t.Errorf("force should not skip any files, skipped %v", report.Skipped)
		}
	}
}
