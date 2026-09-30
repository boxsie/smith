package lib

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// UpdateReport holds the results of a lib update operation.
type UpdateReport struct {
	Updated   []string // unmodified files updated to new embedded version
	Created   []string // new files extracted
	Skipped   []string // user-modified files preserved
	Unchanged []string // files already matching current embedded
	Pruned    []string // obsolete files removed
}

// Update re-extracts embedded libraries, updating unmodified files
// and warning about user-modified ones. Uses the manifest to distinguish
// "user-modified" (on-disk differs from manifest hash) from "embedded changed"
// (on-disk matches manifest hash but embedded content is new).
// With force=true, overwrites everything.
func Update(targetDir string, force bool) (*UpdateReport, error) {
	report := &UpdateReport{}

	// Read existing manifest to detect user modifications.
	oldManifest, err := ReadManifest(targetDir)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	newManifest := make(Manifest)

	err = fs.WalkDir(embeddedFS, "builtin", func(path string, d fs.DirEntry, err error) error {
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

		existing, readErr := os.ReadFile(targetPath)
		if readErr != nil {
			// File doesn't exist on disk — extract it.
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(targetPath, embeddedContent, 0o644); err != nil {
				return err
			}
			newManifest[relPath] = embeddedHex
			report.Created = append(report.Created, relPath)
			return nil
		}

		existingHash := sha256.Sum256(existing)
		existingHex := hex.EncodeToString(existingHash[:])

		if existingHex == embeddedHex {
			// Already up to date.
			newManifest[relPath] = embeddedHex
			report.Unchanged = append(report.Unchanged, relPath)
			return nil
		}

		// File differs from embedded. Check if user-modified.
		// User-modified = on-disk hash differs from what we last extracted (manifest).
		// Not user-modified = on-disk hash matches manifest (so it's just an embedded update).
		manifestHash := oldManifest[relPath]
		userModified := manifestHash != "" && existingHex != manifestHash

		if userModified && !force {
			report.Skipped = append(report.Skipped, relPath)
			return nil
		}

		// Safe to update (unmodified file, or --force).
		if err := os.WriteFile(targetPath, embeddedContent, 0o644); err != nil {
			return err
		}
		newManifest[relPath] = embeddedHex
		report.Updated = append(report.Updated, relPath)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update embedded libraries: %w", err)
	}

	// Prune files that were in the old manifest but are no longer in the
	// embedded FS. Only prune unmodified files (matching old manifest hash)
	// unless force is set.
	for relPath, manifestHash := range oldManifest {
		if _, exists := newManifest[relPath]; exists {
			continue // still in embedded FS
		}
		targetPath := filepath.Join(targetDir, relPath)
		if !force {
			// Only prune if the file hasn't been user-modified.
			existing, err := os.ReadFile(targetPath)
			if err != nil {
				continue // already gone
			}
			existingHash := sha256.Sum256(existing)
			if hex.EncodeToString(existingHash[:]) != manifestHash {
				report.Skipped = append(report.Skipped, relPath)
				continue // user-modified, skip
			}
		}
		if err := os.Remove(targetPath); err == nil {
			report.Pruned = append(report.Pruned, relPath)
		}
	}

	if err := WriteManifest(targetDir, newManifest); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}

	return report, nil
}
