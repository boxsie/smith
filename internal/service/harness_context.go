package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/boxsie/smith/internal/contextsource"
)

// Context bodies live only in the private launch store, never in patch config,
// envelopes, or Artifact's public JSON. Retain them for the life of the launch
// history: recovery needs the approved bytes even if the memory renderer is offline or changed.
// Deleting a launch deliberately deletes its context too; there is no live-source
// fallback for a missing or corrupt snapshot.
func retainHarnessContext(root string, artifacts []contextsource.Artifact) error {
	dir, err := harnessContextDir(root, true)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	seen := map[string]bool{}
	for _, artifact := range artifacts {
		if !validContextHash(artifact.SHA256) || fmt.Sprintf("%x", sha256.Sum256([]byte(artifact.Content))) != artifact.SHA256 || len(artifact.Content) != artifact.Bytes {
			return fmt.Errorf("invalid retained context artifact")
		}
		if seen[artifact.SHA256] {
			continue
		}
		seen[artifact.SHA256] = true
		file, err := dir.OpenFile(artifact.SHA256, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("create retained context: %w", err)
		}
		_, writeErr := file.WriteString(artifact.Content)
		syncErr := file.Sync()
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	// Persist the directory entries before any runtime can be launched.
	return syncContextDirectory(dir)
}

func validContextHash(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func harnessContextDir(root string, create bool) (*os.Root, error) {
	base, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = base.Close() }()
	for _, name := range []string{".smith", ".smith/context"} {
		if create {
			if err := base.Mkdir(name, 0o700); err != nil && !os.IsExist(err) {
				return nil, err
			}
		}
		info, err := base.Lstat(name)
		if err != nil {
			return nil, fmt.Errorf("retained context directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("retained context requires private real directories")
		}
	}
	if create {
		for _, name := range []string{".", ".smith"} {
			dir, err := base.OpenRoot(name)
			if err != nil {
				return nil, err
			}
			err = syncContextDirectory(dir)
			_ = dir.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	return base.OpenRoot(filepath.Join(".smith", "context"))
}

func syncContextDirectory(dir *os.Root) error {
	file, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return file.Sync()
}

func loadHarnessContext(root string, declarations []contextsource.Declaration, expected any) ([]contextsource.Resolution, error) {
	data, err := json.Marshal(expected)
	if err != nil {
		return nil, fmt.Errorf("invalid retained context metadata")
	}
	var artifacts []contextsource.Artifact
	if err := json.Unmarshal(data, &artifacts); err != nil || len(artifacts) == 0 {
		return nil, fmt.Errorf("retained context requires approved artifact metadata")
	}
	dir, err := harnessContextDir(root, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	resolutions := make([]contextsource.Resolution, len(declarations))
	for i, declaration := range declarations {
		resolutions[i].Source = declaration.Source
	}
	for _, artifact := range artifacts {
		// ResolveAll admits at most 4 MiB per artifact. Read one extra byte to
		// detect growth, rather than trusting a file's cached size or its name.
		if !validContextHash(artifact.SHA256) || artifact.Bytes < 0 || artifact.Bytes > 4<<20 {
			return nil, fmt.Errorf("invalid retained context metadata")
		}
		info, err := dir.Lstat(artifact.SHA256)
		if err != nil {
			return nil, fmt.Errorf("retained context unavailable: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("retained context must be a private regular file")
		}
		file, err := dir.Open(artifact.SHA256)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(file, int64(artifact.Bytes)+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(body) != artifact.Bytes || fmt.Sprintf("%x", sha256.Sum256(body)) != artifact.SHA256 {
			return nil, fmt.Errorf("retained context does not match approved bytes")
		}
		artifact.Content = string(body)
		found := false
		for i := range resolutions {
			if resolutions[i].Source == artifact.Source {
				resolutions[i].Artifacts = append(resolutions[i].Artifacts, artifact)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("retained context source is not declared")
		}
	}
	for _, resolution := range resolutions {
		if len(resolution.Artifacts) == 0 {
			return nil, fmt.Errorf("retained context source has no artifacts")
		}
	}
	return resolutions, nil
}
