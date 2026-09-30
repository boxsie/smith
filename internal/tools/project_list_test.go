package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupListDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "file.txt"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(root, "readme.md"), []byte("# hi"), 0o644)
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	os.MkdirAll(filepath.Join(root, ".smith", "proposals"), 0o755)
	return root
}

type listEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

func execList(t *testing.T, root string, input string) ([]listEntry, error) {
	t.Helper()
	tool := &ProjectList{}
	scope := map[string]string{ScopeRoot: root}
	var raw json.RawMessage
	if input != "" {
		raw = json.RawMessage(input)
	}
	out, err := tool.Execute(context.Background(), raw, scope)
	if err != nil {
		return nil, err
	}
	var entries []listEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	return entries, nil
}

func TestProjectList_MixedEntries(t *testing.T) {
	root := setupListDir(t)
	entries, err := execList(t, root, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have file.txt, readme.md, sub — NOT .smith
	names := make(map[string]string)
	for _, e := range entries {
		names[e.Name] = e.Type
	}
	if _, ok := names[".smith"]; ok {
		t.Error(".smith should be excluded from listing")
	}
	if names["file.txt"] != "file" {
		t.Error("expected file.txt as file")
	}
	if names["sub"] != "directory" {
		t.Error("expected sub as directory")
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
}

func TestProjectList_FileSize(t *testing.T) {
	root := setupListDir(t)
	entries, err := execList(t, root, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == "file.txt" {
			if e.Size != 5 {
				t.Errorf("file.txt size = %d, want 5", e.Size)
			}
			return
		}
	}
	t.Error("file.txt not found in listing")
}

func TestProjectList_Subdirectory(t *testing.T) {
	root := setupListDir(t)
	os.WriteFile(filepath.Join(root, "sub", "inner.txt"), []byte("x"), 0o644)

	entries, err := execList(t, root, `{"path":"sub"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "inner.txt" {
		t.Errorf("expected [inner.txt], got %v", entries)
	}
}

func TestProjectList_TraversalRejected(t *testing.T) {
	root := setupListDir(t)
	_, err := execList(t, root, `{"path":"../"}`)
	if err == nil {
		t.Fatal("expected error for ../ traversal")
	}
}

func TestProjectList_NotFound(t *testing.T) {
	root := setupListDir(t)
	_, err := execList(t, root, `{"path":"nonexistent"}`)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got: %v", err)
	}
}

func TestProjectList_FileTarget(t *testing.T) {
	root := setupListDir(t)
	_, err := execList(t, root, `{"path":"file.txt"}`)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected not a directory error, got: %v", err)
	}
}

func TestProjectList_SmithDirectPathRejected(t *testing.T) {
	root := setupListDir(t)
	_, err := execList(t, root, `{"path":".smith"}`)
	if err == nil || !strings.Contains(err.Error(), "runner-private") {
		t.Fatalf("expected runner-private error for .smith direct access, got: %v", err)
	}
}

func TestProjectList_SmithNestedPathRejected(t *testing.T) {
	root := setupListDir(t)
	_, err := execList(t, root, `{"path":".smith/proposals"}`)
	if err == nil || !strings.Contains(err.Error(), "runner-private") {
		t.Fatalf("expected runner-private error for .smith/proposals, got: %v", err)
	}
}

func TestProjectList_SmithCachePathRejected(t *testing.T) {
	root := setupListDir(t)
	_, err := execList(t, root, `{"path":".smith/cache"}`)
	if err == nil || !strings.Contains(err.Error(), "runner-private") {
		t.Fatalf("expected runner-private error for .smith/cache, got: %v", err)
	}
}

func TestProjectList_EmptyDir(t *testing.T) {
	root := t.TempDir()
	entries, err := execList(t, root, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestProjectList_DefaultPath(t *testing.T) {
	root := setupListDir(t)
	// Omitting path should list root.
	entries, err := execList(t, root, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
}
