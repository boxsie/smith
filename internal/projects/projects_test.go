package projects

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrackNewPath(t *testing.T) {
	dir := t.TempDir()

	if err := TrackIn(dir, "/home/user/project-a"); err != nil {
		t.Fatalf("Track: %v", err)
	}

	idx, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(idx.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(idx.Projects))
	}
	if idx.Projects[0] != "/home/user/project-a" {
		t.Errorf("expected /home/user/project-a, got %s", idx.Projects[0])
	}
}

func TestTrackExistingPathPromotesToFront(t *testing.T) {
	dir := t.TempDir()

	TrackIn(dir, "/home/user/project-a")
	TrackIn(dir, "/home/user/project-b")
	TrackIn(dir, "/home/user/project-c")

	// Re-track project-a — should move to front.
	if err := TrackIn(dir, "/home/user/project-a"); err != nil {
		t.Fatalf("Track: %v", err)
	}

	idx, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(idx.Projects) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(idx.Projects))
	}
	want := []string{"/home/user/project-a", "/home/user/project-c", "/home/user/project-b"}
	for i, p := range idx.Projects {
		if p != want[i] {
			t.Errorf("index[%d] = %s, want %s", i, p, want[i])
		}
	}
}

func TestTrackNoDuplicates(t *testing.T) {
	dir := t.TempDir()

	TrackIn(dir, "/home/user/project-a")
	TrackIn(dir, "/home/user/project-a")
	TrackIn(dir, "/home/user/project-a")

	idx, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(idx.Projects) != 1 {
		t.Fatalf("expected 1 project (no duplicates), got %d", len(idx.Projects))
	}
}

func TestRemovePresentPath(t *testing.T) {
	dir := t.TempDir()

	TrackIn(dir, "/home/user/project-a")
	TrackIn(dir, "/home/user/project-b")

	if err := RemoveFrom(dir, "/home/user/project-a"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	idx, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(idx.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(idx.Projects))
	}
	if idx.Projects[0] != "/home/user/project-b" {
		t.Errorf("expected /home/user/project-b, got %s", idx.Projects[0])
	}
}

func TestRemoveAbsentPathNoError(t *testing.T) {
	dir := t.TempDir()

	TrackIn(dir, "/home/user/project-a")

	if err := RemoveFrom(dir, "/home/user/nonexistent"); err != nil {
		t.Fatalf("Remove absent path should not error: %v", err)
	}

	idx, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(idx.Projects) != 1 {
		t.Fatalf("expected 1 project unchanged, got %d", len(idx.Projects))
	}
}

func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()

	idx, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("Load missing file should not error: %v", err)
	}
	if len(idx.Projects) != 0 {
		t.Fatalf("expected empty index, got %d projects", len(idx.Projects))
	}
}

func TestLoadCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "projects.json"), []byte("{corrupt"), 0o600)

	_, err := LoadFrom(dir)
	if err == nil {
		t.Fatal("expected error for corrupt JSON, got nil")
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()

	paths := []string{
		"/home/user/project-a",
		"/home/user/project-b",
		"/home/user/project-c",
	}
	for _, p := range paths {
		if err := TrackIn(dir, p); err != nil {
			t.Fatalf("Track %s: %v", p, err)
		}
	}

	idx, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Most recently tracked should be first.
	want := []string{"/home/user/project-c", "/home/user/project-b", "/home/user/project-a"}
	if len(idx.Projects) != len(want) {
		t.Fatalf("expected %d projects, got %d", len(want), len(idx.Projects))
	}
	for i, p := range idx.Projects {
		if p != want[i] {
			t.Errorf("index[%d] = %s, want %s", i, p, want[i])
		}
	}
}

func TestTrackCreatesFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "projects.json")

	// File should not exist yet.
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("expected projects.json to not exist initially")
	}

	if err := TrackIn(dir, "/home/user/project-a"); err != nil {
		t.Fatalf("Track: %v", err)
	}

	// File should exist now.
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("expected projects.json to exist after Track: %v", err)
	}
}
