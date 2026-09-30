package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexWorkRejectsMalformedProtectedPathsBeforeProcess(t *testing.T) {
	for _, name := range []string{".codex", ".agents"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, name)
			if err := os.WriteFile(path, []byte("keep me"), 0600); err != nil {
				t.Fatal(err)
			}
			invocation := codexInvocation(root)
			invocation.Profile = CapabilityWork
			invocation.Workspace = WorkspacePolicy{Root: root, Access: WorkspaceWritable, Granted: true, GrantRoot: root}
			invocation.Capabilities = CapabilityPolicy{Profile: CapabilityWork, Allow: []string{"workspace.inspect", "workspace.edit"}}
			runtime := &CodexRuntime{Runner: processRunnerFunc(func(context.Context, ProcessRequest) (ProcessResult, error) {
				t.Fatal("malformed workspace reached the external process")
				return ProcessResult{}, nil
			})}
			sink := &codexSink{}
			err := runtime.Invoke(t.Context(), invocation, sink)
			if !errors.Is(err, ErrCodexConfiguration) || !strings.Contains(err.Error(), path) {
				t.Fatalf("expected actionable configuration error, got %v", err)
			}
			if sink.result != nil || len(sink.events) != 0 {
				t.Fatal("failed preflight reported runtime work")
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != "keep me" {
				t.Fatalf("protected path was changed: %q, %v", data, err)
			}
		})
	}
}

func TestCodexProtectedDirectoryShapes(t *testing.T) {
	for _, shape := range []string{"missing", "directories", "gitdir-file", "directory-link", "dangling-link"} {
		t.Run(shape, func(t *testing.T) {
			root := t.TempDir()
			var err error
			switch shape {
			case "directories":
				for _, name := range []string{".codex", ".agents"} {
					if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
						t.Fatal(err)
					}
				}
			case "gitdir-file":
				err = os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /some/worktree\n"), 0600)
			case "directory-link":
				err = os.Symlink(t.TempDir(), filepath.Join(root, ".codex"))
			case "dangling-link":
				err = os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, ".codex"))
			}
			if err != nil {
				t.Fatal(err)
			}
			err = validateCodexProtectedDirectories(root)
			if (err != nil) != (shape == "dangling-link") {
				t.Fatalf("unexpected preflight: %v", err)
			}
		})
	}
}
