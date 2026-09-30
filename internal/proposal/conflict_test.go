package proposal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello"), 0o644)

	hash, err := HashFile(path)
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
	// SHA-256 of "hello" is well-known.
	want := "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if hash != want {
		t.Errorf("hash = %q, want %q", hash, want)
	}
}

func TestHashFileNotFound(t *testing.T) {
	hash, err := HashFile("/nonexistent/file")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if hash != "" {
		t.Errorf("expected empty hash for missing file, got %q", hash)
	}
}

func TestHashFileDeterministic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	os.WriteFile(path, []byte("deterministic content"), 0o644)

	h1, _ := HashFile(path)
	h2, _ := HashFile(path)
	if h1 != h2 {
		t.Errorf("HashFile not deterministic: %q != %q", h1, h2)
	}
}

func TestCheckConflictsNoBaseHash(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("modified"), 0o644)

	ops := []Operation{
		{Op: "write", Path: "task.md", Source: "files/task.md"},
	}

	conflicts, err := CheckConflicts(dir, ops)
	if err != nil {
		t.Fatalf("CheckConflicts: %v", err)
	}
	if len(conflicts) != 0 {
		t.Errorf("expected no conflicts without base_hash, got %d", len(conflicts))
	}
}

func TestCheckConflictsMatchingHash(t *testing.T) {
	dir := t.TempDir()
	content := []byte("original content")
	os.WriteFile(filepath.Join(dir, "task.md"), content, 0o644)

	hash, _ := HashFile(filepath.Join(dir, "task.md"))

	ops := []Operation{
		{Op: "write", Path: "task.md", Source: "files/task.md", BaseHash: hash},
	}

	conflicts, err := CheckConflicts(dir, ops)
	if err != nil {
		t.Fatalf("CheckConflicts: %v", err)
	}
	if len(conflicts) != 0 {
		t.Errorf("expected no conflicts with matching hash, got %d", len(conflicts))
	}
}

func TestCheckConflictsMismatchedHash(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "task.md"), []byte("modified content"), 0o644)

	ops := []Operation{
		{Op: "write", Path: "task.md", Source: "files/task.md", BaseHash: "sha256:oldhash"},
	}

	conflicts, err := CheckConflicts(dir, ops)
	if err != nil {
		t.Fatalf("CheckConflicts: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	if conflicts[0].Reason != "file was modified since planning" {
		t.Errorf("unexpected reason: %q", conflicts[0].Reason)
	}
}

func TestCheckConflictsFileDeleted(t *testing.T) {
	dir := t.TempDir()
	// File does not exist, but base_hash is set.

	ops := []Operation{
		{Op: "write", Path: "task.md", Source: "files/task.md", BaseHash: "sha256:abc123"},
	}

	conflicts, err := CheckConflicts(dir, ops)
	if err != nil {
		t.Fatalf("CheckConflicts: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	if conflicts[0].Reason != "file was deleted since planning" {
		t.Errorf("unexpected reason: %q", conflicts[0].Reason)
	}
}

func TestCheckConflictsDeleteAlreadyRemoved(t *testing.T) {
	dir := t.TempDir()

	ops := []Operation{
		{Op: "delete", Path: "gone.md", BaseHash: "sha256:abc123"},
	}

	conflicts, err := CheckConflicts(dir, ops)
	if err != nil {
		t.Fatalf("CheckConflicts: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	if conflicts[0].Reason != "file was already removed" {
		t.Errorf("unexpected reason: %q", conflicts[0].Reason)
	}
}

func TestCheckConflictsDeleteModified(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "old.md"), []byte("changed"), 0o644)

	ops := []Operation{
		{Op: "delete", Path: "old.md", BaseHash: "sha256:oldhash"},
	}

	conflicts, err := CheckConflicts(dir, ops)
	if err != nil {
		t.Fatalf("CheckConflicts: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	if conflicts[0].Reason != "file was modified since planning" {
		t.Errorf("unexpected reason: %q", conflicts[0].Reason)
	}
}

func TestCheckConflictsMixed(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "clean.md"), []byte("clean"), 0o644)

	cleanHash, _ := HashFile(filepath.Join(dir, "clean.md"))

	ops := []Operation{
		{Op: "write", Path: "clean.md", Source: "files/clean.md", BaseHash: cleanHash},       // no conflict
		{Op: "write", Path: "gone.md", Source: "files/gone.md", BaseHash: "sha256:abc"},       // conflict: deleted
		{Op: "write", Path: "new.md", Source: "files/new.md"},                                  // no base_hash
		{Op: "delete", Path: "missing.md", BaseHash: "sha256:def"},                             // conflict: already removed
	}

	conflicts, err := CheckConflicts(dir, ops)
	if err != nil {
		t.Fatalf("CheckConflicts: %v", err)
	}
	if len(conflicts) != 2 {
		t.Errorf("expected 2 conflicts, got %d", len(conflicts))
	}
}
