package app

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type fileState struct {
	data []byte
	mode fs.FileMode
}

func snapshot(root string) (map[string]fileState, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve app root: %w", err)
	}
	states := make(map[string]fileState)
	err = filepath.WalkDir(abs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if rel == ".git" || strings.HasPrefix(rel, ".git/") || entry.Name() == "output" {
				return filepath.SkipDir
			}
			if rel == ".smith" {
				return nil
			}
			if strings.HasPrefix(rel, ".smith/") && rel != ".smith/lib" && !strings.HasPrefix(rel, ".smith/lib/") {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("authored app file %q is a symlink", rel)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		// Transaction temporaries live beside their targets so rename remains
		// atomic. They are never part of the authored app model.
		if strings.HasPrefix(entry.Name(), ".smith-write-") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		states[rel] = fileState{data: data, mode: info.Mode().Perm()}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot app: %w", err)
	}
	return states, nil
}

func revision(states map[string]fileState) string {
	paths := make([]string, 0, len(states))
	for path := range states {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		_, _ = fmt.Fprintf(h, "path:%s\n", path)
		_, _ = fmt.Fprintf(h, "mode:%o\n", states[path].mode)
		h.Write(states[path].data)
		h.Write([]byte{0})
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum)
}

func diffStates(before, after map[string]fileState) []FileChange {
	all := make(map[string]struct{}, len(before)+len(after))
	for path := range before {
		all[path] = struct{}{}
	}
	for path := range after {
		all[path] = struct{}{}
	}
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	changes := make([]FileChange, 0)
	for _, path := range paths {
		old, hadOld := before[path]
		newState, hasNew := after[path]
		if hadOld && hasNew && string(old.data) == string(newState.data) && old.mode == newState.mode {
			continue
		}
		change := FileChange{Path: path}
		if hadOld {
			change.BeforeHash = hashBytes(old.data)
			change.BeforeMode = fmt.Sprintf("%04o", old.mode)
		}
		if hasNew {
			change.Op = "write"
			change.AfterHash = hashBytes(newState.data)
			change.AfterMode = fmt.Sprintf("%04o", newState.mode)
		} else {
			change.Op = "delete"
		}
		changes = append(changes, change)
	}
	return changes
}

func copySnapshot(root string, states map[string]fileState) error {
	for path, state := range states {
		target := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, state.data, state.mode); err != nil {
			return err
		}
	}
	return nil
}
