package lib

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// InitReport holds the results of an init operation.
type InitReport struct {
	Created   []string // files created
	Skipped   []string // files skipped (user-modified)
	Unchanged []string // files already matching embedded
	Pruned    []string // obsolete files removed
}

// Init extracts embedded library content to targetDir (typically ~/.smith/lib/).
// It creates targetDir if needed. Skips files that already exist and differ
// from the embedded version (user-modified) unless force is true.
// Writes a manifest recording the hash of each extracted file.
func Init(targetDir string, force bool) (*InitReport, error) {
	report := &InitReport{}
	manifest := make(Manifest)

	err := fs.WalkDir(embeddedFS, "builtin", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "builtin" {
			return nil
		}

		relPath, err := filepath.Rel("builtin", path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(targetDir, relPath)

		if d.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}

		embeddedContent, err := fs.ReadFile(embeddedFS, path)
		if err != nil {
			return err
		}
		embeddedHash := sha256.Sum256(embeddedContent)
		embeddedHex := hex.EncodeToString(embeddedHash[:])

		if existing, readErr := os.ReadFile(targetPath); readErr == nil {
			existingHash := sha256.Sum256(existing)
			if embeddedHash == existingHash {
				manifest[relPath] = embeddedHex
				report.Unchanged = append(report.Unchanged, relPath)
				return nil
			}
			if !force {
				// Don't update manifest for skipped files.
				report.Skipped = append(report.Skipped, relPath)
				return nil
			}
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(targetPath, embeddedContent, 0o644); err != nil {
			return err
		}
		manifest[relPath] = embeddedHex
		report.Created = append(report.Created, relPath)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("extract embedded libraries: %w", err)
	}

	// Prune files that were in the old manifest but are no longer in the
	// embedded FS. This handles files removed between versions (e.g.
	// planner/tools.md, planner/schema.md removed in v3).
	oldManifest, _ := ReadManifest(targetDir)
	for relPath := range oldManifest {
		if _, exists := manifest[relPath]; !exists {
			targetPath := filepath.Join(targetDir, relPath)
			if err := os.Remove(targetPath); err == nil {
				report.Pruned = append(report.Pruned, relPath)
			}
		}
	}

	if err := WriteManifest(targetDir, manifest); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}

	return report, nil
}
