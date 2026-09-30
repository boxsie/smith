package prompt

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStaticContext_MultipleFiles(t *testing.T) {
	tmp := t.TempDir()
	staticDir := filepath.Join(tmp, "context", "static")
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(staticDir, "b.txt"), "second")
	writeFile(t, filepath.Join(staticDir, "a.txt"), "first")

	files, err := LoadStaticContext(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("files count = %d, want 2", len(files))
	}

	// Lexicographic order: a.txt before b.txt.
	if files[0].RelPath != "a.txt" {
		t.Errorf("files[0].RelPath = %q, want %q", files[0].RelPath, "a.txt")
	}
	if files[0].Content != "first" {
		t.Errorf("files[0].Content = %q, want %q", files[0].Content, "first")
	}
	if files[1].RelPath != "b.txt" {
		t.Errorf("files[1].RelPath = %q, want %q", files[1].RelPath, "b.txt")
	}
	if files[1].Content != "second" {
		t.Errorf("files[1].Content = %q, want %q", files[1].Content, "second")
	}
}

func TestLoadStaticContext_NestedDirs(t *testing.T) {
	tmp := t.TempDir()
	staticDir := filepath.Join(tmp, "context", "static")
	nestedDir := filepath.Join(staticDir, "sub", "deep")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(staticDir, "top.md"), "top level")
	writeFile(t, filepath.Join(nestedDir, "nested.md"), "nested content")

	files, err := LoadStaticContext(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("files count = %d, want 2", len(files))
	}

	// sub/deep/nested.md sorts before top.md.
	if files[0].RelPath != "sub/deep/nested.md" {
		t.Errorf("files[0].RelPath = %q, want %q", files[0].RelPath, "sub/deep/nested.md")
	}
	if files[1].RelPath != "top.md" {
		t.Errorf("files[1].RelPath = %q, want %q", files[1].RelPath, "top.md")
	}
}

func TestLoadStaticContext_BinaryFile(t *testing.T) {
	tmp := t.TempDir()
	staticDir := filepath.Join(tmp, "context", "static")
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a file with null bytes.
	if err := os.WriteFile(filepath.Join(staticDir, "image.png"), []byte{0x89, 0x50, 0x4E, 0x47, 0x00}, 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadStaticContext(tmp)
	if !errors.Is(err, ErrBinaryFile) {
		t.Errorf("err = %v, want ErrBinaryFile", err)
	}
}

func TestLoadStaticContext_InvalidUTF8(t *testing.T) {
	tmp := t.TempDir()
	staticDir := filepath.Join(tmp, "context", "static")
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a file with invalid UTF-8 (truncated multi-byte sequence, no null bytes).
	if err := os.WriteFile(filepath.Join(staticDir, "bad.txt"), []byte{0xC0, 0xAF, 0x68, 0x65, 0x6C, 0x6C, 0x6F}, 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadStaticContext(tmp)
	if !errors.Is(err, ErrInvalidUTF8) {
		t.Errorf("err = %v, want ErrInvalidUTF8", err)
	}
}

func TestLoadStaticContext_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	staticDir := filepath.Join(tmp, "context", "static")
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		t.Fatal(err)
	}

	files, err := LoadStaticContext(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("files count = %d, want 0", len(files))
	}
}

func TestLoadStaticContext_MissingDir(t *testing.T) {
	tmp := t.TempDir()

	files, err := LoadStaticContext(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if files == nil {
		t.Error("files should be non-nil empty slice, got nil")
	}
	if len(files) != 0 {
		t.Errorf("files count = %d, want 0", len(files))
	}
}

func TestLoadStaticContext_MixedTextFiles(t *testing.T) {
	tmp := t.TempDir()
	staticDir := filepath.Join(tmp, "context", "static")
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(staticDir, "data.json"), `{"key": "value"}`)
	writeFile(t, filepath.Join(staticDir, "notes.md"), "# Notes\nSome notes.")
	writeFile(t, filepath.Join(staticDir, "plain.txt"), "plain text")

	files, err := LoadStaticContext(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) != 3 {
		t.Fatalf("files count = %d, want 3", len(files))
	}

	// Lexicographic: data.json, notes.md, plain.txt.
	if files[0].RelPath != "data.json" {
		t.Errorf("files[0].RelPath = %q, want %q", files[0].RelPath, "data.json")
	}
	if files[1].RelPath != "notes.md" {
		t.Errorf("files[1].RelPath = %q, want %q", files[1].RelPath, "notes.md")
	}
	if files[2].RelPath != "plain.txt" {
		t.Errorf("files[2].RelPath = %q, want %q", files[2].RelPath, "plain.txt")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
