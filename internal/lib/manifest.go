package lib

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const manifestFile = ".embedded-manifest.json"

// Manifest records the SHA256 hashes of files as they were last extracted
// from the embedded FS. This allows Update to distinguish between
// "user-modified" (on-disk differs from manifest) and "embedded changed"
// (manifest differs from current embedded, but on-disk matches manifest).
type Manifest map[string]string // relPath → hex SHA256

// ReadManifest reads the manifest from the target lib directory.
// Returns an empty manifest (not error) if the file does not exist.
func ReadManifest(targetDir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(targetDir, manifestFile))
	if err != nil {
		if os.IsNotExist(err) {
			return Manifest{}, nil
		}
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// WriteManifest writes the manifest to the target lib directory.
func WriteManifest(targetDir string, m Manifest) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(targetDir, manifestFile), data, 0o644)
}
