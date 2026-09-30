package lib

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

var (
	ErrModuleNotFound = errors.New("module not found in lib path")
)

// ResolveModule searches for a library module by name through the resolution
// chain: project-local .smith/lib/ → user-global ~/.smith/lib/ → embedded.
// projectRoot is the directory containing .smith/ (the project root).
// Returns the absolute path to the resolved module directory.
func ResolveModule(name string, projectRoot string) (string, error) {
	userDir, err := UserLibDir()
	if err != nil {
		return "", fmt.Errorf("resolve user lib dir: %w", err)
	}

	libDirs := []string{
		ProjectLibDir(projectRoot),
		userDir,
	}

	resolved, err := resolveModuleInPaths(name, libDirs)
	if err == nil {
		return resolved, nil
	}

	// Tier 3: embedded — auto-extract to user lib dir and return that path.
	return resolveEmbedded(name, userDir)
}

// ProjectLibDir returns the project-local lib path.
func ProjectLibDir(projectRoot string) string {
	return filepath.Join(projectRoot, ".smith", "lib")
}

// UserLibDir returns the user-global lib path (~/.smith/lib/).
func UserLibDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".smith", "lib"), nil
}

// resolveModuleInPaths searches for a module in the given lib directories.
// A match requires a directory containing task.md.
func resolveModuleInPaths(name string, libDirs []string) (string, error) {
	for _, dir := range libDirs {
		candidate := filepath.Join(dir, name)
		taskMD := filepath.Join(candidate, "task.md")
		if _, err := os.Stat(taskMD); err == nil {
			abs, err := filepath.Abs(candidate)
			if err != nil {
				return "", err
			}
			return abs, nil
		}
	}
	return "", ErrModuleNotFound
}

// resolveEmbedded checks the embedded FS for a module and auto-extracts
// it to the user lib dir on first use.
func resolveEmbedded(name, userLibDir string) (string, error) {
	embeddedRoot := filepath.Join("builtin", name)
	// Check if the module exists in the embedded FS.
	taskMDPath := filepath.Join(embeddedRoot, "task.md")
	if _, err := fs.Stat(embeddedFS, filepath.ToSlash(taskMDPath)); err != nil {
		return "", ErrModuleNotFound
	}

	// Auto-extract to user lib dir.
	targetDir := filepath.Join(userLibDir, name)
	if err := extractEmbeddedModule(name, userLibDir); err != nil {
		return "", fmt.Errorf("auto-extract module %q: %w", name, err)
	}

	abs, err := filepath.Abs(targetDir)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// extractEmbeddedModule extracts a single module from the embedded FS
// to the target lib directory. Skips files that already exist and differ
// from the embedded version (user-modified).
func extractEmbeddedModule(name, userLibDir string) error {
	embeddedRoot := filepath.Join("builtin", name)
	return fs.WalkDir(embeddedFS, filepath.ToSlash(embeddedRoot), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Compute the target path relative to the module name.
		relPath, err := filepath.Rel(filepath.ToSlash(embeddedRoot), filepath.ToSlash(path))
		if err != nil {
			return err
		}
		targetPath := filepath.Join(userLibDir, name, relPath)

		if d.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}

		// Read embedded content.
		embeddedContent, err := fs.ReadFile(embeddedFS, filepath.ToSlash(path))
		if err != nil {
			return err
		}

		// If file exists on disk, check if it's user-modified.
		if existing, readErr := os.ReadFile(targetPath); readErr == nil {
			if sha256.Sum256(existing) != sha256.Sum256(embeddedContent) {
				// User-modified — skip.
				return nil
			}
			// Already matches — skip.
			return nil
		}

		// Extract.
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(targetPath, embeddedContent, 0o644)
	})
}
