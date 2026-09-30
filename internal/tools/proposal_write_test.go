package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func execWrite(t *testing.T, root, proposalID, input string) (map[string]any, error) {
	t.Helper()
	tool := &ProposalWrite{}
	scope := map[string]string{
		ScopeRoot:       root,
		ScopeProposalID: proposalID,
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(input), scope)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return result, nil
}

func TestProposalWrite_SimpleFile(t *testing.T) {
	root := t.TempDir()
	result, err := execWrite(t, root, "abc123", `{"path":"task.md","content":"hello"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["written"] != true {
		t.Error("expected written=true")
	}

	// Verify file was created.
	data, err := os.ReadFile(filepath.Join(root, ".smith", "proposals", "abc123", "files", "task.md"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("content = %q, want %q", data, "hello")
	}
}

func TestProposalWrite_NestedDirs(t *testing.T) {
	root := t.TempDir()
	_, err := execWrite(t, root, "abc123", `{"path":"subtasks/01-gather/task.md","content":"deep"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, ".smith", "proposals", "abc123", "files", "subtasks", "01-gather", "task.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "deep" {
		t.Errorf("content = %q", data)
	}
}

func TestProposalWrite_TraversalInPath(t *testing.T) {
	root := t.TempDir()
	_, err := execWrite(t, root, "abc123", `{"path":"../../escape.txt","content":"bad"}`)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestProposalWrite_TraversalInProposalID(t *testing.T) {
	root := t.TempDir()
	_, err := execWrite(t, root, "../../outside", `{"path":"task.md","content":"bad"}`)
	if err == nil || !strings.Contains(err.Error(), "invalid proposal_id") {
		t.Fatalf("expected invalid proposal_id error, got: %v", err)
	}
}

func TestProposalWrite_InvalidProposalIDFormat(t *testing.T) {
	cases := []string{
		"has spaces",
		"has/slash",
		".starts-with-dot",
		"-starts-with-hyphen",
		"",
	}
	root := t.TempDir()
	for _, id := range cases {
		_, err := execWrite(t, root, id, `{"path":"task.md","content":"x"}`)
		if err == nil {
			t.Errorf("expected error for proposal_id %q", id)
		}
	}
}

func TestProposalWrite_ReservedManifest(t *testing.T) {
	root := t.TempDir()
	_, err := execWrite(t, root, "abc123", `{"path":"manifest.json","content":"{}"}`)
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved error, got: %v", err)
	}
}

func TestProposalWrite_ReservedSummary(t *testing.T) {
	root := t.TempDir()
	_, err := execWrite(t, root, "abc123", `{"path":"summary.md","content":"# hi"}`)
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved error, got: %v", err)
	}
}

func TestProposalWrite_ReservedDotSlashManifest(t *testing.T) {
	root := t.TempDir()
	_, err := execWrite(t, root, "abc123", `{"path":"./manifest.json","content":"{}"}`)
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved error for ./manifest.json, got: %v", err)
	}
}

func TestProposalWrite_ReservedTraversalSummary(t *testing.T) {
	root := t.TempDir()
	_, err := execWrite(t, root, "abc123", `{"path":"sub/../summary.md","content":"# hi"}`)
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved error for sub/../summary.md, got: %v", err)
	}
}

func TestProposalWrite_NestedManifestAllowed(t *testing.T) {
	root := t.TempDir()
	_, err := execWrite(t, root, "abc123", `{"path":"sub/manifest.json","content":"{}"}`)
	if err != nil {
		t.Fatalf("nested manifest.json should be allowed, got: %v", err)
	}
}

func TestProposalWrite_EmptyContent(t *testing.T) {
	root := t.TempDir()
	_, err := execWrite(t, root, "abc123", `{"path":"empty.txt","content":""}`)
	if err != nil {
		t.Fatalf("empty content should be allowed, got: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".smith", "proposals", "abc123", "files", "empty.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty file, got %d bytes", len(data))
	}
}

func TestProposalWrite_EmptyPath(t *testing.T) {
	root := t.TempDir()
	_, err := execWrite(t, root, "abc123", `{"path":"","content":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected required error, got: %v", err)
	}
}

func TestValidateProposalID(t *testing.T) {
	valid := []string{"abc123", "20260319-143022-abc123", "my_proposal", "A1"}
	for _, id := range valid {
		if err := ValidateProposalID(id); err != nil {
			t.Errorf("expected valid: %q, got error: %v", id, err)
		}
	}

	invalid := []string{"", "../escape", ".dot", "-dash", "has space", "a/b", "a\\b"}
	for _, id := range invalid {
		if err := ValidateProposalID(id); err == nil {
			t.Errorf("expected invalid: %q", id)
		}
	}
}
