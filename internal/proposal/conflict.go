package proposal

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Conflict describes a file conflict detected during apply.
type Conflict struct {
	Op           string
	Path         string
	ExpectedHash string
	ActualHash   string
	Reason       string
}

// CheckConflicts examines each operation with a base_hash against the current
// filesystem state at targetDir. Returns nil if no conflicts are found.
func CheckConflicts(targetDir string, ops []Operation) ([]Conflict, error) {
	var conflicts []Conflict

	for _, op := range ops {
		if op.BaseHash == "" {
			continue
		}

		filePath := filepath.Join(targetDir, op.Path)
		actualHash, err := HashFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("hash file %q: %w", op.Path, err)
		}

		switch op.Op {
		case "write":
			if actualHash == "" {
				// File was deleted since planning.
				conflicts = append(conflicts, Conflict{
					Op:           op.Op,
					Path:         op.Path,
					ExpectedHash: op.BaseHash,
					ActualHash:   "",
					Reason:       "file was deleted since planning",
				})
			} else if actualHash != op.BaseHash {
				conflicts = append(conflicts, Conflict{
					Op:           op.Op,
					Path:         op.Path,
					ExpectedHash: op.BaseHash,
					ActualHash:   actualHash,
					Reason:       "file was modified since planning",
				})
			}

		case "delete":
			if actualHash == "" {
				// File already removed.
				conflicts = append(conflicts, Conflict{
					Op:           op.Op,
					Path:         op.Path,
					ExpectedHash: op.BaseHash,
					ActualHash:   "",
					Reason:       "file was already removed",
				})
			} else if actualHash != op.BaseHash {
				conflicts = append(conflicts, Conflict{
					Op:           op.Op,
					Path:         op.Path,
					ExpectedHash: op.BaseHash,
					ActualHash:   actualHash,
					Reason:       "file was modified since planning",
				})
			}
		}
	}

	return conflicts, nil
}

// HashFile computes the SHA-256 hex digest of a file's contents.
// Returns ("", nil) if the file does not exist.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}
