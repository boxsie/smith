package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/boxsie/smith/internal/shell"
	"github.com/boxsie/smith/internal/task"
)

// runShellScript executes a shell task's body as a /bin/sh -e script.
// Returns stdout, stderr, and any error (non-zero exit includes stderr in error).
// On error, stdout is still returned so the caller can write result.md for debugging.
func runShellScript(ctx context.Context, t *task.Task, graph *task.Graph, cfg *Config) (string, string, error) {
	cmd := exec.Command("/bin/sh", "-e", "-c", t.Body)
	cmd.Dir = t.Path

	// Start with ambient environment so PATH etc. are available.
	env := os.Environ()

	// SMITH_INPUT_* — run input entries (root tasks only).
	if t.Parent == nil {
		for _, entry := range cfg.RunInput {
			if entry.Name == "stdin" {
				continue // stdin entry is delivered via process stdin, not env var
			}
			varName := "SMITH_INPUT_" + shell.NormalizeName(entry.Name)
			env = append(env, varName+"="+entry.Value)
		}
	}

	// SMITH_DEP_* — sibling dependency canonical output file paths.
	for _, dep := range graph.SiblingDeps[t.ID] {
		varName := "SMITH_DEP_" + shell.NormalizeName(dep.ID)
		outputPath := canonicalOutputPath(dep)
		env = append(env, varName+"="+outputPath)
	}

	// SMITH_PARENT_OUTPUT — parent's output file path.
	// Two-phase parent: points to task-phase output (output/task/result.md).
	// Single-phase parent: points to canonical output.
	if t.Parent != nil {
		var outputPath string
		if t.Parent.HasReturn {
			outputPath = filepath.Join(t.Parent.Path, "output", "task", "result.md")
		} else {
			outputPath = canonicalOutputPath(t.Parent)
		}
		env = append(env, "SMITH_PARENT_OUTPUT="+outputPath)
	}

	// SMITH_SOURCE_PATH — for module-backed tasks where source != runtime.
	if t.SourcePath != "" && t.SourcePath != t.Path {
		env = append(env, "SMITH_SOURCE_PATH="+t.SourcePath)
	}

	// Static context: ensure the full merged view is accessible at the task's
	// context/static/ filesystem path (RFC: "available at filesystem paths").
	// SMITH_CONTEXT_DIR is set as a convenience pointing to the same location.
	var contextCleanup func()
	if len(t.StaticContext) > 0 {
		contextDir, cleanup, err := materializeStaticContext(t)
		if err != nil {
			return "", "", fmt.Errorf("materialize static context: %w", err)
		}
		contextCleanup = cleanup
		env = append(env, "SMITH_CONTEXT_DIR="+contextDir)
	}

	cmd.Env = env

	// Deliver stdin run input as actual stdin to the shell process.
	if t.Parent == nil {
		for _, entry := range cfg.RunInput {
			if entry.Name == "stdin" {
				cmd.Stdin = strings.NewReader(entry.Value)
				break
			}
		}
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := shell.RunCommand(ctx, cmd)
	stdout := stdoutBuf.String()
	stderr := stderrBuf.String()

	// Clean up materialized context if we created one.
	if contextCleanup != nil {
		contextCleanup()
	}

	if runErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return stdout, stderr, fmt.Errorf("shell task %q timed out", t.ID)
		}
		exitCode := -1
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		// Return stdout even on error so the caller can write result.md for debugging.
		return stdout, stderr, fmt.Errorf("shell task %q exited %d: %s", t.ID, exitCode, strings.TrimSpace(stderr))
	}

	return stdout, stderr, nil
}

// materializeStaticContext ensures the full merged static context is accessible
// at the task's context/static/ filesystem path (relative to cmd.Dir = t.Path).
// This satisfies the RFC contract: "context/static/ available at filesystem paths".
//
// Three cases:
//  1. Non-module task: context/static/ already at t.Path — nothing to do.
//  2. Module, no local overrides: symlink t.Path/context → source/context.
//  3. Module with local overrides: copy source-only files into t.Path/context/static/.
//
// Returns the absolute path to context/static/, a cleanup function, and any error.
func materializeStaticContext(t *task.Task) (string, func(), error) {
	noop := func() {}
	contextStaticDir := filepath.Join(t.Path, "context", "static")

	// Case 1: non-module task — already at the right place.
	if t.SourcePath == "" || t.SourcePath == t.Path {
		return contextStaticDir, noop, nil
	}

	// Module-backed task. Check if the runtime path has its own context/static/.
	_, runtimeErr := os.Stat(contextStaticDir)

	// Case 2: no local overrides — symlink source context into runtime path.
	if runtimeErr != nil {
		srcContext := filepath.Join(t.SourcePath, "context")
		dstContext := filepath.Join(t.Path, "context")
		if err := os.Symlink(srcContext, dstContext); err != nil {
			return "", nil, fmt.Errorf("symlink context: %w", err)
		}
		cleanup := func() { os.Remove(dstContext) }
		return contextStaticDir, cleanup, nil
	}

	// Case 3: both source and runtime have context/static/. The runtime dir
	// already has override files. Copy in source-only files (from t.StaticContext)
	// that don't already exist at the runtime path, so the full merged view is
	// available at context/static/.
	var copiedFiles []string
	for _, f := range t.StaticContext {
		dst := filepath.Join(contextStaticDir, f.RelPath)
		if _, err := os.Stat(dst); err == nil {
			continue // local override already exists
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			// Best-effort cleanup of files we already copied.
			for _, p := range copiedFiles {
				os.Remove(p)
			}
			return "", nil, err
		}
		if err := os.WriteFile(dst, []byte(f.Content), 0o644); err != nil {
			for _, p := range copiedFiles {
				os.Remove(p)
			}
			return "", nil, err
		}
		copiedFiles = append(copiedFiles, dst)
	}
	cleanup := func() {
		for _, p := range copiedFiles {
			os.Remove(p)
		}
	}
	return contextStaticDir, cleanup, nil
}

// canonicalOutputPath returns the absolute path to a task's canonical output file.
func canonicalOutputPath(t *task.Task) string {
	if t.Frontmatter.OutputType() == "json" {
		return filepath.Join(t.Path, "output", "result.json")
	}
	return filepath.Join(t.Path, "output", "result.md")
}
