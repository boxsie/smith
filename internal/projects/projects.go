package projects

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Index is the global list of known Smith project paths.
// Paths are ordered most-recently-used first.
type Index struct {
	Projects []string `json:"projects"`
}

// dir returns ~/.smith.
func dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".smith"), nil
}

// path returns the full path to projects.json.
func path() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "projects.json"), nil
}

// Load reads the project index from ~/.smith/projects.json.
// Returns an empty index (not error) if the file doesn't exist.
func Load() (*Index, error) {
	return LoadFrom("")
}

// LoadFrom reads the project index from a specific directory.
// If dir is empty, uses the default ~/.smith directory.
// Returns an empty index (not error) if the file doesn't exist.
func LoadFrom(dir string) (*Index, error) {
	var p string
	if dir != "" {
		p = filepath.Join(dir, "projects.json")
	} else {
		var err error
		p, err = path()
		if err != nil {
			return nil, err
		}
	}

	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &Index{}, nil
		}
		return nil, fmt.Errorf("read project index: %w", err)
	}

	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse project index: %w", err)
	}
	return &idx, nil
}

// Save explicitly replaces the project index in ~/.smith/projects.json.
// Use Track/Remove for concurrent read-modify-write operations, not Load + Save.
func Save(idx *Index) error {
	return SaveTo("", idx)
}

// SaveTo explicitly replaces the project index in a specific directory.
// If dir is empty, uses the default ~/.smith directory.
// Use TrackIn/RemoveFrom to mutate the latest snapshot without losing updates.
func SaveTo(dir string, idx *Index) error {
	if idx == nil {
		return fmt.Errorf("project index is nil")
	}
	return withIndexLock(dir, func(d string) error { return saveLocked(d, idx) })
}

// Hold a separate, permanent inode: locking projects.json itself would stop
// serializing writers as soon as an atomic replacement changed that inode.
func withIndexLock(d string, update func(string) error) error {
	if d == "" {
		var err error
		d, err = dir()
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return fmt.Errorf("create projects directory: %w", err)
	}
	if err := os.Chmod(d, 0o700); err != nil {
		return fmt.Errorf("chmod projects directory: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(d, "projects.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open project index lock: %w", err)
	}
	// Closing the descriptor releases the kernel lock, including on process death.
	// Never unlink it: waiters must all continue locking the same inode.
	defer func() { _ = lock.Close() }()
	if err := lock.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod project index lock: %w", err)
	}
	if err := lockIndex(lock); err != nil {
		return fmt.Errorf("lock project index: %w", err)
	}
	return update(d)
}

func saveLocked(d string, idx *Index) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal project index: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(d, ".projects-*.tmp")
	if err != nil {
		return fmt.Errorf("create project index temporary file: %w", err)
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod project index: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write project index: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync project index: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close project index: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(d, "projects.json")); err != nil {
		return fmt.Errorf("replace project index: %w", err)
	}
	return syncIndexDirectory(d)
}

// Track adds a project path to the index (or promotes it to the front if already present).
func Track(absPath string) error {
	return TrackIn("", absPath)
}

// TrackIn adds a project path to the index stored in a specific directory.
func TrackIn(dir, absPath string) error {
	// Resolve to absolute path.
	absPath, err := filepath.Abs(absPath)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}

	return withIndexLock(dir, func(d string) error {
		idx, err := LoadFrom(d)
		if err != nil {
			return err
		}
		// Remove existing entry if present (we'll prepend it).
		filtered := make([]string, 0, len(idx.Projects))
		for _, p := range idx.Projects {
			if p != absPath {
				filtered = append(filtered, p)
			}
		}

		// Prepend (most-recently-used first).
		idx.Projects = append([]string{absPath}, filtered...)

		return saveLocked(d, idx)
	})
}

// Remove removes a project path from the index. No-op if not present.
func Remove(absPath string) error {
	return RemoveFrom("", absPath)
}

// RemoveFrom removes a project path from the index stored in a specific directory.
func RemoveFrom(dir, absPath string) error {
	return withIndexLock(dir, func(d string) error {
		idx, err := LoadFrom(d)
		if err != nil {
			return err
		}

		filtered := make([]string, 0, len(idx.Projects))
		for _, p := range idx.Projects {
			if p != absPath {
				filtered = append(filtered, p)
			}
		}
		idx.Projects = filtered

		return saveLocked(d, idx)
	})
}
