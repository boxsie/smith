package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/run"
)

func TestReadArtifactOnlyReturnsBoundedPublishedRuntimeFiles(t *testing.T) {
	root := t.TempDir()
	runID := "20260902-100000.000000000-artifact"
	runDir := run.RunDir(root, runID)
	runtimeDir := run.RuntimeDir(runDir)
	published := filepath.Join(runtimeDir, "output", "result.md")
	unpublished := filepath.Join(runtimeDir, "secret.txt")
	outside := filepath.Join(root, "outside.txt")
	escapingLink := filepath.Join(runtimeDir, "output", "outside-link.txt")
	for path, content := range map[string]string{published: "result", unpublished: "secret", outside: "outside"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, escapingLink); err != nil {
		t.Fatal(err)
	}
	store, err := run.NewEventStore(runDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []run.Event{
		{Type: run.EventRunQueued, RunID: runID, AppRoot: root},
		{Type: run.EventArtifactPublished, RunID: runID, Artifact: outside},
		{Type: run.EventArtifactPublished, RunID: runID, Artifact: escapingLink},
		{Type: run.EventArtifactPublished, RunID: runID, Artifact: published},
		{Type: run.EventRunCompleted, RunID: runID},
	} {
		if _, err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	service := New(Dependencies{})
	artifact, err := service.ReadArtifact(root, runID, "output/result.md", 100)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Content != "result" || artifact.Path != "output/result.md" {
		t.Fatalf("unexpected artifact: %#v", artifact)
	}
	for _, path := range []string{unpublished, outside, escapingLink} {
		if _, err := service.ReadArtifact(root, runID, path, 100); err == nil {
			t.Fatalf("read unapproved path %s", path)
		}
	}
	if _, err := service.ReadArtifact(root, runID, published, 2); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("size limit error = %v", err)
	}
	if _, err := service.ReadArtifact(root, runID, published, defaultArtifactLimit+1); err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("maximum limit error = %v", err)
	}
}
