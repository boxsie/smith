package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSafeResolve_NormalPath(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "file.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := SafeResolve(root, "sub/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(root, "sub", "file.txt")
	// Compare via EvalSymlinks since TempDir may contain symlinks on some OSes.
	wantResolved, _ := filepath.EvalSymlinks(want)
	if got != wantResolved {
		t.Errorf("got %q, want %q", got, wantResolved)
	}
}

func TestSafeResolve_EmptyPath(t *testing.T) {
	root := t.TempDir()
	got, err := SafeResolve(root, ".")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	canonRoot, _ := filepath.EvalSymlinks(root)
	if got != canonRoot {
		t.Errorf("got %q, want %q", got, canonRoot)
	}
}

func TestSafeResolve_DotDotRejected(t *testing.T) {
	root := t.TempDir()
	_, err := SafeResolve(root, "../outside")
	if err == nil {
		t.Fatal("expected error for ../ traversal")
	}
}

func TestSafeResolve_EmbeddedDotDotRejected(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := SafeResolve(root, "a/../../outside")
	if err == nil {
		t.Fatal("expected error for embedded ../ traversal")
	}
}

func TestSafeResolve_DotDotStaysWithin(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := SafeResolve(root, "a/b/../file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	canonRoot, _ := filepath.EvalSymlinks(root)
	want := filepath.Join(canonRoot, "a", "file.txt")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSafeResolve_AbsolutePathRejected(t *testing.T) {
	root := t.TempDir()
	_, err := SafeResolve(root, "/etc/passwd")
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
}

func TestSafeResolve_SymlinkEscapeRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink test not supported on Windows")
	}

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a symlink inside root pointing outside.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	_, err := SafeResolve(root, "escape/secret.txt")
	if err == nil {
		t.Fatal("expected error for symlink escape")
	}
}

func TestSafeResolve_SymlinkWithinAllowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink test not supported on Windows")
	}

	root := t.TempDir()
	sub := filepath.Join(root, "real")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "file.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create symlink within root pointing to another location within root.
	link := filepath.Join(root, "link")
	if err := os.Symlink(sub, link); err != nil {
		t.Fatal(err)
	}

	got, err := SafeResolve(root, "link/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	canonRoot, _ := filepath.EvalSymlinks(root)
	want := filepath.Join(canonRoot, "real", "file.txt")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSafeResolve_NonExistentLeaf(t *testing.T) {
	root := t.TempDir()
	// Resolving a path where the leaf doesn't exist should succeed
	// (for write operations).
	got, err := SafeResolve(root, "newfile.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	canonRoot, _ := filepath.EvalSymlinks(root)
	want := filepath.Join(canonRoot, "newfile.txt")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIsSmithDir(t *testing.T) {
	if !IsSmithDir(".smith") {
		t.Error("expected true for .smith")
	}
	if IsSmithDir("smith") {
		t.Error("expected false for smith")
	}
	if IsSmithDir(".smithx") {
		t.Error("expected false for .smithx")
	}
}

func TestHasSmithComponent(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{".smith/foo", true},
		{"a/.smith/b", true},
		{".smith", true},
		{"smith/foo", false},
		{"a/b/c", false},
		{".smithx/foo", false},
	}
	for _, tt := range tests {
		got := HasSmithComponent(tt.path)
		if got != tt.want {
			t.Errorf("HasSmithComponent(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
