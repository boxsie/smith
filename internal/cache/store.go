package cache

import (
	"fmt"
	"os"
	"path/filepath"
)

// Artifacts holds the cached output files for a task execution.
type Artifacts struct {
	ResultMD    []byte // canonical result.md (single-phase or return-phase)
	ResultJSON  []byte // result.json (only for json output type)
	TaskPhaseMD []byte // task-result.md (only for task-phase entries)
}

// entryDir returns the content-addressed cache directory for a given hash.
// Uses two-character fan-out: <cacheRoot>/<hash[0:2]>/<hash>/
func entryDir(cacheRoot, hash string) string {
	if len(hash) < 2 {
		return filepath.Join(cacheRoot, hash, hash)
	}
	return filepath.Join(cacheRoot, hash[:2], hash)
}

// Exists checks whether a cache entry exists (fast stat, no file reads).
func Exists(cacheRoot, hash string) bool {
	if cacheRoot == "" {
		return false
	}
	info, err := os.Stat(entryDir(cacheRoot, hash))
	return err == nil && info.IsDir()
}

// Store writes artifacts to the shared cache atomically.
// Uses temp directory + rename for atomicity. Concurrent stores with the
// same hash are safe (last-writer-wins).
func Store(cacheRoot, hash string, arts Artifacts) error {
	if cacheRoot == "" {
		return nil
	}

	dir := entryDir(cacheRoot, hash)
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create cache parent: %w", err)
	}

	// Write to a temp directory alongside the target, then rename.
	tmp, err := os.MkdirTemp(parent, ".smith-cache-tmp-*")
	if err != nil {
		return fmt.Errorf("create cache temp dir: %w", err)
	}

	writeFile := func(name string, data []byte) error {
		return os.WriteFile(filepath.Join(tmp, name), data, 0o644)
	}

	if arts.ResultMD != nil {
		if err := writeFile("result.md", arts.ResultMD); err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("write result.md: %w", err)
		}
	}
	if arts.ResultJSON != nil {
		if err := writeFile("result.json", arts.ResultJSON); err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("write result.json: %w", err)
		}
	}
	if arts.TaskPhaseMD != nil {
		if err := writeFile("task-result.md", arts.TaskPhaseMD); err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("write task-result.md: %w", err)
		}
	}

	// Atomic rename into place. If the target already exists (concurrent store),
	// remove our temp and succeed — the content is identical (content-addressed).
	if err := os.Rename(tmp, dir); err != nil {
		os.RemoveAll(tmp)
		// If the entry now exists (concurrent store succeeded), that's fine.
		if Exists(cacheRoot, hash) {
			return nil
		}
		return fmt.Errorf("rename cache entry: %w", err)
	}
	return nil
}

// Retrieve reads cached artifacts. Returns (zero, false, nil) on miss.
// Corrupt or missing entries return a clean miss, not an error.
func Retrieve(cacheRoot, hash string) (Artifacts, bool, error) {
	if cacheRoot == "" {
		return Artifacts{}, false, nil
	}

	dir := entryDir(cacheRoot, hash)
	if _, err := os.Stat(dir); err != nil {
		return Artifacts{}, false, nil
	}

	var arts Artifacts

	if data, err := os.ReadFile(filepath.Join(dir, "result.md")); err == nil {
		arts.ResultMD = data
	}
	if data, err := os.ReadFile(filepath.Join(dir, "result.json")); err == nil {
		arts.ResultJSON = data
	}
	if data, err := os.ReadFile(filepath.Join(dir, "task-result.md")); err == nil {
		arts.TaskPhaseMD = data
	}

	// If no files were found, treat as corrupt/empty entry — clean miss.
	if arts.ResultMD == nil && arts.ResultJSON == nil && arts.TaskPhaseMD == nil {
		return Artifacts{}, false, nil
	}

	return arts, true, nil
}
