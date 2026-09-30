package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupFindDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// root/
	//   readme.md
	//   main.go
	//   sub/
	//     task.md
	//     deep/
	//       notes.md
	//   .smith/
	//     proposals/
	//       data.md
	os.WriteFile(filepath.Join(root, "readme.md"), []byte("hi"), 0o644)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644)
	os.MkdirAll(filepath.Join(root, "sub", "deep"), 0o755)
	os.WriteFile(filepath.Join(root, "sub", "task.md"), []byte("task"), 0o644)
	os.WriteFile(filepath.Join(root, "sub", "deep", "notes.md"), []byte("notes"), 0o644)
	os.MkdirAll(filepath.Join(root, ".smith", "proposals"), 0o755)
	os.WriteFile(filepath.Join(root, ".smith", "proposals", "data.md"), []byte("data"), 0o644)
	return root
}

func execFind(t *testing.T, root string, input string) ([]string, error) {
	t.Helper()
	tool := &ProjectFind{}
	scope := map[string]string{ScopeRoot: root}
	out, err := tool.Execute(context.Background(), json.RawMessage(input), scope)
	if err != nil {
		return nil, err
	}
	var matches []string
	if err := json.Unmarshal(out, &matches); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return matches, nil
}

func TestProjectFind_DoubleStarMD(t *testing.T) {
	root := setupFindDir(t)
	matches, err := execFind(t, root, `{"pattern":"**/*.md"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"readme.md", "sub/deep/notes.md", "sub/task.md"}
	if len(matches) != len(want) {
		t.Fatalf("got %v, want %v", matches, want)
	}
	for i := range want {
		if matches[i] != want[i] {
			t.Errorf("matches[%d] = %q, want %q", i, matches[i], want[i])
		}
	}
}

func TestProjectFind_RootOnly(t *testing.T) {
	root := setupFindDir(t)
	matches, err := execFind(t, root, `{"pattern":"*.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0] != "main.go" {
		t.Errorf("got %v, want [main.go]", matches)
	}
}

func TestProjectFind_NoMatches(t *testing.T) {
	root := setupFindDir(t)
	matches, err := execFind(t, root, `{"pattern":"*.rs"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("got %v, want empty", matches)
	}
}

func TestProjectFind_SmithExcluded(t *testing.T) {
	root := setupFindDir(t)
	matches, err := execFind(t, root, `{"pattern":"**/*.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		if strings.Contains(m, ".smith") {
			t.Errorf("match %q should not include .smith paths", m)
		}
	}
}

func TestProjectFind_SortedOutput(t *testing.T) {
	root := setupFindDir(t)
	matches, err := execFind(t, root, `{"pattern":"**/*.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(matches); i++ {
		if matches[i] < matches[i-1] {
			t.Errorf("not sorted: %q before %q", matches[i-1], matches[i])
		}
	}
}

func TestProjectFind_EmptyPattern(t *testing.T) {
	root := setupFindDir(t)
	_, err := execFind(t, root, `{"pattern":""}`)
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected required error, got: %v", err)
	}
}

func TestProjectFind_InvalidPattern(t *testing.T) {
	root := setupFindDir(t)
	_, err := execFind(t, root, `{"pattern":"[invalid"}`)
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("expected invalid pattern error, got: %v", err)
	}
}

func TestProjectFind_ExactFilename(t *testing.T) {
	root := setupFindDir(t)
	matches, err := execFind(t, root, `{"pattern":"**/task.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0] != "sub/task.md" {
		t.Errorf("got %v, want [sub/task.md]", matches)
	}
}

func TestProjectFind_DoubleStarAlone(t *testing.T) {
	root := setupFindDir(t)
	matches, err := execFind(t, root, `{"pattern":"**"}`)
	if err != nil {
		t.Fatal(err)
	}
	// Should match all files (not directories, not .smith)
	want := []string{"main.go", "readme.md", "sub/deep/notes.md", "sub/task.md"}
	if len(matches) != len(want) {
		t.Fatalf("got %v, want %v", matches, want)
	}
	for i := range want {
		if matches[i] != want[i] {
			t.Errorf("matches[%d] = %q, want %q", i, matches[i], want[i])
		}
	}
}

func TestProjectFind_FilesOnly(t *testing.T) {
	root := setupFindDir(t)
	matches, err := execFind(t, root, `{"pattern":"*"}`)
	if err != nil {
		t.Fatal(err)
	}
	// Should only match files in root, not the "sub" directory.
	for _, m := range matches {
		if m == "sub" {
			t.Error("directory 'sub' should not appear in results")
		}
	}
	if len(matches) != 2 {
		t.Errorf("expected 2 files (main.go, readme.md), got %v", matches)
	}
}
