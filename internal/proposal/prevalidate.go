package proposal

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/boxsie/smith/internal/validate"
)

// PreValidate creates a temporary workspace, replays operations, and
// runs validation against the result. Returns the validation outcome
// to be embedded in manifest.json.
func PreValidate(targetDir, proposalDir string, ops []Operation) (*ValidationResult, error) {
	tmpDir, err := os.MkdirTemp("", "smith-prevalidate-*")
	if err != nil {
		return nil, fmt.Errorf("create temp workspace: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Copy target directory into temp workspace.
	// Excludes .smith/proposals/, .smith/cache/, and output/ but preserves
	// .smith/lib/ for module resolution.
	if targetDir != "" {
		if info, err := os.Stat(targetDir); err == nil && info.IsDir() {
			if err := copyDir(targetDir, tmpDir); err != nil {
				return nil, fmt.Errorf("copy target to workspace: %w", err)
			}
		}
	}

	// Replay operations in order.
	if err := replayOperations(tmpDir, proposalDir, ops); err != nil {
		return nil, fmt.Errorf("replay operations: %w", err)
	}

	// Run validation against the workspace.
	vr := validate.Validate(tmpDir)

	result := &ValidationResult{
		ValidatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	if len(vr.Errs) > 0 {
		result.Status = "fail"
		for _, e := range vr.Errs {
			result.Errors = append(result.Errors, e.Error())
		}
	} else {
		result.Status = "pass"
	}

	return result, nil
}

// replayOperations applies operations to a workspace directory.
func replayOperations(workspaceDir, proposalDir string, ops []Operation) error {
	for _, op := range ops {
		targetPath := filepath.Join(workspaceDir, op.Path)

		switch op.Op {
		case "write":
			sourcePath := filepath.Join(proposalDir, op.Source)
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return fmt.Errorf("create dirs for %q: %w", op.Path, err)
			}
			if err := copyFile(sourcePath, targetPath); err != nil {
				return fmt.Errorf("write %q: %w", op.Path, err)
			}

		case "delete":
			if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("delete %q: %w", op.Path, err)
			}

		default:
			return fmt.Errorf("unknown operation: %q", op.Op)
		}
	}
	return nil
}

// copyDir recursively copies src to dst, excluding .smith/proposals/,
// .smith/cache/, and output/ directories. Preserves .smith/lib/ for module resolution.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		// Exclusion rules.
		if d.IsDir() {
			slashRel := filepath.ToSlash(rel)
			// Skip .smith/proposals/ and .smith/cache/ entirely.
			if slashRel == ".smith/proposals" || hasPrefix(slashRel, ".smith/proposals/") ||
				slashRel == ".smith/cache" || hasPrefix(slashRel, ".smith/cache/") {
				return filepath.SkipDir
			}
			// Skip output/ directories at any level.
			if d.Name() == "output" {
				return filepath.SkipDir
			}
		}

		dstPath := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}

		return copyFile(path, dstPath)
	})
}

// copyFile copies a single file from src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	return out.Close()
}

// hasPrefix checks if s starts with prefix + "/".
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
