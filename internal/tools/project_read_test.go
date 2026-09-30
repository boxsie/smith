package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func execRead(t *testing.T, root string, input string) (map[string]string, error) {
	t.Helper()
	tool := &ProjectRead{}
	scope := map[string]string{ScopeRoot: root}
	out, err := tool.Execute(context.Background(), json.RawMessage(input), scope)
	if err != nil {
		return nil, err
	}
	var result map[string]string
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return result, nil
}

func TestProjectRead_TextFile(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "hello.txt"), []byte("world"), 0o644)

	result, err := execRead(t, root, `{"path":"hello.txt"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["content"] != "world" {
		t.Errorf("content = %q, want %q", result["content"], "world")
	}
	if result["path"] != "hello.txt" {
		t.Errorf("path = %q, want %q", result["path"], "hello.txt")
	}
}

func TestProjectRead_UnicodeContent(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "uni.txt"), []byte("hello 世界 🌍"), 0o644)

	result, err := execRead(t, root, `{"path":"uni.txt"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["content"] != "hello 世界 🌍" {
		t.Errorf("content = %q", result["content"])
	}
}

func TestProjectRead_BinaryRejected(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0x00, 0x01, 0x02}, 0o644)

	_, err := execRead(t, root, `{"path":"bin.dat"}`)
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected binary error, got: %v", err)
	}
}

func TestProjectRead_TraversalRejected(t *testing.T) {
	root := t.TempDir()
	_, err := execRead(t, root, `{"path":"../etc/passwd"}`)
	if err == nil {
		t.Fatal("expected error for traversal")
	}
}

func TestProjectRead_NotFound(t *testing.T) {
	root := t.TempDir()
	_, err := execRead(t, root, `{"path":"missing.txt"}`)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got: %v", err)
	}
}

func TestProjectRead_DirectoryRejected(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "dir"), 0o755)

	_, err := execRead(t, root, `{"path":"dir"}`)
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("expected directory error, got: %v", err)
	}
}

func TestProjectRead_SmithPathRejected(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".smith", "proposals"), 0o755)
	os.WriteFile(filepath.Join(root, ".smith", "proposals", "data.json"), []byte("{}"), 0o644)

	_, err := execRead(t, root, `{"path":".smith/proposals/data.json"}`)
	if err == nil || !strings.Contains(err.Error(), "runner-private") {
		t.Fatalf("expected runner-private error, got: %v", err)
	}
}

func TestProjectRead_SmithCachePathRejected(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".smith", "cache", "planner"), 0o755)
	os.WriteFile(filepath.Join(root, ".smith", "cache", "planner", "result.json"), []byte("{}"), 0o644)

	_, err := execRead(t, root, `{"path":".smith/cache/planner/result.json"}`)
	if err == nil || !strings.Contains(err.Error(), "runner-private") {
		t.Fatalf("expected runner-private error, got: %v", err)
	}
}

func TestProjectRead_EmptyPathRejected(t *testing.T) {
	root := t.TempDir()
	_, err := execRead(t, root, `{"path":""}`)
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected required error, got: %v", err)
	}
}
