package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SafeResolve resolves relPath against root and verifies the result stays within root.
// It rejects absolute relPaths, resolves symlinks via EvalSymlinks, and checks containment.
// Returns the resolved absolute path or an error.
func SafeResolve(root, relPath string) (string, error) {
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("absolute path not allowed: %q", relPath)
	}

	// Resolve symlinks in root to get the canonical root.
	canonRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	canonRoot = filepath.Clean(canonRoot)

	// Join and clean the target path.
	joined := filepath.Join(canonRoot, relPath)

	// Resolve symlinks in the target. If the target doesn't exist yet
	// (e.g. for writes), resolve the longest existing prefix.
	resolved, err := evalSymlinksLongest(joined)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	// Verify containment: resolved must be within canonRoot.
	if !isWithin(canonRoot, resolved) {
		return "", fmt.Errorf("path escapes scope root: %q", relPath)
	}

	return resolved, nil
}

// evalSymlinksLongest resolves symlinks for the longest existing prefix of path,
// then appends the remaining unresolved suffix. This handles writes to paths
// where the leaf file/directory doesn't exist yet.
func evalSymlinksLongest(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}

	// Walk up to find the longest existing prefix.
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	resolvedDir, err := evalSymlinksLongest(dir)
	if err != nil {
		return "", err
	}

	return filepath.Join(resolvedDir, base), nil
}

// isWithin checks if target is within or equal to root.
func isWithin(root, target string) bool {
	if root == target {
		return true
	}
	prefix := root + string(filepath.Separator)
	return strings.HasPrefix(target, prefix)
}

// IsSmithDir returns true if name is the smith internal directory name.
func IsSmithDir(name string) bool {
	return name == ".smith"
}

// HasSmithComponent returns true if any component of relPath is ".smith".
func HasSmithComponent(relPath string) bool {
	for part := range strings.SplitSeq(filepath.ToSlash(relPath), "/") {
		if part == ".smith" {
			return true
		}
	}
	return false
}
