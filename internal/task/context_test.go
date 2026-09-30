package task

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStaticContextEager_NoContext(t *testing.T) {
	tmp := t.TempDir()
	files, err := LoadStaticContextEager(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected empty, got %d files", len(files))
	}
}

func TestLoadStaticContextEager_WithFiles(t *testing.T) {
	tmp := t.TempDir()
	staticDir := filepath.Join(tmp, "context", "static")
	os.MkdirAll(staticDir, 0o755)
	os.WriteFile(filepath.Join(staticDir, "a.md"), []byte("content a"), 0o644)
	os.WriteFile(filepath.Join(staticDir, "b.md"), []byte("content b"), 0o644)

	files, err := LoadStaticContextEager(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
}

func TestMergeStaticContext_SourceAndOverride(t *testing.T) {
	tmp := t.TempDir()

	sourceDir := filepath.Join(tmp, "source")
	os.MkdirAll(filepath.Join(sourceDir, "context", "static"), 0o755)
	os.WriteFile(filepath.Join(sourceDir, "context", "static", "a.md"), []byte("source a"), 0o644)

	overrideDir := filepath.Join(tmp, "override")
	os.MkdirAll(filepath.Join(overrideDir, "context", "static"), 0o755)
	os.WriteFile(filepath.Join(overrideDir, "context", "static", "b.md"), []byte("override b"), 0o644)

	files, err := MergeStaticContext(sourceDir, overrideDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].RelPath != "a.md" {
		t.Errorf("first file = %q, want a.md (source)", files[0].RelPath)
	}
	if files[1].RelPath != "b.md" {
		t.Errorf("second file = %q, want b.md (override)", files[1].RelPath)
	}
}

func TestMergeStaticContext_SourceOnly(t *testing.T) {
	tmp := t.TempDir()

	sourceDir := filepath.Join(tmp, "source")
	os.MkdirAll(filepath.Join(sourceDir, "context", "static"), 0o755)
	os.WriteFile(filepath.Join(sourceDir, "context", "static", "a.md"), []byte("source a"), 0o644)

	files, err := MergeStaticContext(sourceDir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
}

func TestMergeStaticContext_SameDir(t *testing.T) {
	tmp := t.TempDir()

	os.MkdirAll(filepath.Join(tmp, "context", "static"), 0o755)
	os.WriteFile(filepath.Join(tmp, "context", "static", "a.md"), []byte("content"), 0o644)

	files, err := MergeStaticContext(tmp, tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Same dir should not duplicate.
	if len(files) != 1 {
		t.Errorf("expected 1 file (no duplication), got %d", len(files))
	}
}
