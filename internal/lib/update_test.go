package lib

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdate_EmptyEmbedded(t *testing.T) {
	tmp := t.TempDir()
	report, err := Update(tmp, false)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(report.Skipped) > 0 {
		t.Errorf("unexpected skipped: %v", report.Skipped)
	}
}

func TestUpdate_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	_, err := Update(tmp, false)
	if err != nil {
		t.Fatalf("Update 1: %v", err)
	}
	report, err := Update(tmp, false)
	if err != nil {
		t.Fatalf("Update 2: %v", err)
	}
	if len(report.Created) > 0 {
		t.Errorf("second run created %d files, want 0", len(report.Created))
	}
}

func TestUpdate_SkipsUserModified(t *testing.T) {
	tmp := t.TempDir()
	_, err := Update(tmp, false)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	gitkeepPath := filepath.Join(tmp, ".gitkeep")
	if _, err := os.Stat(gitkeepPath); err == nil {
		os.WriteFile(gitkeepPath, []byte("user content"), 0o644)

		report, err := Update(tmp, false)
		if err != nil {
			t.Fatalf("Update after modify: %v", err)
		}
		if len(report.Skipped) == 0 {
			t.Error("expected modified file to be skipped")
		}
	}
}

func TestUpdate_PrunesObsoleteFiles(t *testing.T) {
	tmp := t.TempDir()
	_, err := Update(tmp, false)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Simulate a file from a previous version.
	obsoletePath := filepath.Join(tmp, "planner", "tools.md")
	os.MkdirAll(filepath.Dir(obsoletePath), 0o755)
	content := []byte("old content")
	os.WriteFile(obsoletePath, content, 0o644)

	// Record it in manifest with the REAL hash of the content so it
	// looks like an unmodified extracted file.
	hash := sha256.Sum256(content)
	manifest, _ := ReadManifest(tmp)
	manifest["planner/tools.md"] = hex.EncodeToString(hash[:])
	WriteManifest(tmp, manifest)

	report, err := Update(tmp, false)
	if err != nil {
		t.Fatalf("Update prune: %v", err)
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
}

func TestUpdate_SkipsUserModifiedObsoleteFiles(t *testing.T) {
	tmp := t.TempDir()
	_, err := Update(tmp, false)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Simulate a file that was extracted, then user-modified, then removed from embedded.
	obsoletePath := filepath.Join(tmp, "planner", "old-custom.md")
	os.MkdirAll(filepath.Dir(obsoletePath), 0o755)

	manifest, _ := ReadManifest(tmp)
	manifest["planner/old-custom.md"] = "0000000000000000000000000000000000000000000000000000000000000000"
	WriteManifest(tmp, manifest)
	// Write different content than the manifest hash → user-modified.
	os.WriteFile(obsoletePath, []byte("user modified this"), 0o644)

	report, err := Update(tmp, false)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Should be skipped (user-modified), not pruned.
	for _, p := range report.Pruned {
		if p == "planner/old-custom.md" {
			t.Error("user-modified obsolete file should not be pruned without --force")
		}
	}
	if _, err := os.Stat(obsoletePath); os.IsNotExist(err) {
		t.Error("user-modified obsolete file should still exist")
	}
}

func TestUpdate_ForceOverwrites(t *testing.T) {
	tmp := t.TempDir()
	_, err := Update(tmp, false)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	gitkeepPath := filepath.Join(tmp, ".gitkeep")
	if _, err := os.Stat(gitkeepPath); err == nil {
		os.WriteFile(gitkeepPath, []byte("user content"), 0o644)

		report, err := Update(tmp, true)
		if err != nil {
			t.Fatalf("Update --force: %v", err)
		}
		if len(report.Skipped) > 0 {
			t.Errorf("force should not skip, skipped %v", report.Skipped)
		}
	}
}
