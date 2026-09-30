package lib

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveModuleInPaths_ProjectLocal(t *testing.T) {
	tmp := t.TempDir()
	modDir := filepath.Join(tmp, "project", ".smith", "lib", "mymod")
	os.MkdirAll(modDir, 0o755)
	os.WriteFile(filepath.Join(modDir, "task.md"), []byte("# task"), 0o644)

	resolved, err := resolveModuleInPaths("mymod", []string{filepath.Join(tmp, "project", ".smith", "lib")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(resolved) != "mymod" {
		t.Errorf("expected resolved to end with mymod, got %s", resolved)
	}
}

func TestResolveModuleInPaths_UserGlobalFallback(t *testing.T) {
	tmp := t.TempDir()
	projectLib := filepath.Join(tmp, "project", ".smith", "lib")
	userLib := filepath.Join(tmp, "user", ".smith", "lib")

	// Only create in user-global.
	modDir := filepath.Join(userLib, "mymod")
	os.MkdirAll(modDir, 0o755)
	os.WriteFile(filepath.Join(modDir, "task.md"), []byte("# task"), 0o644)

	resolved, err := resolveModuleInPaths("mymod", []string{projectLib, userLib})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(resolved) != "mymod" {
		t.Errorf("expected resolved to end with mymod, got %s", resolved)
	}
}

func TestResolveModuleInPaths_ProjectLocalShadowsUserGlobal(t *testing.T) {
	tmp := t.TempDir()
	projectLib := filepath.Join(tmp, "project", ".smith", "lib")
	userLib := filepath.Join(tmp, "user", ".smith", "lib")

	// Create in both.
	for _, dir := range []string{projectLib, userLib} {
		modDir := filepath.Join(dir, "mymod")
		os.MkdirAll(modDir, 0o755)
		os.WriteFile(filepath.Join(modDir, "task.md"), []byte("# task"), 0o644)
	}

	resolved, err := resolveModuleInPaths("mymod", []string{projectLib, userLib})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should resolve to project-local.
	want := filepath.Join(projectLib, "mymod")
	wantAbs, _ := filepath.Abs(want)
	if resolved != wantAbs {
		t.Errorf("want %s, got %s", wantAbs, resolved)
	}
}

func TestResolveModuleInPaths_DirWithoutTaskMD(t *testing.T) {
	tmp := t.TempDir()
	modDir := filepath.Join(tmp, "lib", "mymod")
	os.MkdirAll(modDir, 0o755)
	// No task.md — just the directory.

	_, err := resolveModuleInPaths("mymod", []string{filepath.Join(tmp, "lib")})
	if !errors.Is(err, ErrModuleNotFound) {
		t.Errorf("want ErrModuleNotFound, got %v", err)
	}
}

func TestResolveModuleInPaths_NotFound(t *testing.T) {
	tmp := t.TempDir()
	_, err := resolveModuleInPaths("nonexistent", []string{tmp})
	if !errors.Is(err, ErrModuleNotFound) {
		t.Errorf("want ErrModuleNotFound, got %v", err)
	}
}
