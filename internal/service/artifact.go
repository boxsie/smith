package service

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/boxsie/smith/internal/run"
)

const defaultArtifactLimit = 1 << 20

// Artifact is a bounded, published output from a Smith run.
type Artifact struct {
	RunID   string `json:"run_id"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Size    int64  `json:"size"`
}

// ReadArtifact returns only files named by artifact.published events. It does
// not provide a general-purpose view of the run or application filesystem.
func (s *Service) ReadArtifact(appRoot, runID, artifactPath string, maxBytes int64) (*Artifact, error) {
	absRoot, err := filepath.Abs(appRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve app path: %w", err)
	}
	if maxBytes <= 0 {
		maxBytes = defaultArtifactLimit
	}
	if maxBytes > defaultArtifactLimit {
		return nil, fmt.Errorf("artifact byte limit %d exceeds maximum %d", maxBytes, defaultArtifactLimit)
	}
	runDir := run.RunDir(absRoot, runID)
	if _, err := s.reconcileRun(runDir); err != nil {
		return nil, err
	}
	runtimeDir := run.RuntimeDir(runDir)
	wanted := filepath.Clean(filepath.FromSlash(artifactPath))
	var matched, relative string
	var cursor uint64
	for matched == "" {
		page, readErr := run.ReadEvents(runDir, cursor, 1000)
		if readErr != nil {
			return nil, readErr
		}
		for _, event := range page.Events {
			if event.Type != run.EventArtifactPublished || event.Artifact == "" {
				continue
			}
			absolute, absErr := filepath.Abs(event.Artifact)
			if absErr != nil {
				continue
			}
			rel, relErr := filepath.Rel(runtimeDir, absolute)
			if relErr != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			if wanted == filepath.Clean(event.Artifact) || wanted == filepath.Clean(rel) {
				matched, relative = absolute, filepath.ToSlash(rel)
				break
			}
		}
		if matched != "" || !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if matched == "" {
		return nil, fmt.Errorf("artifact %q was not published by run %s", artifactPath, runID)
	}
	resolvedRuntime, err := filepath.EvalSymlinks(runtimeDir)
	if err != nil {
		return nil, err
	}
	resolvedArtifact, err := filepath.EvalSymlinks(matched)
	if err != nil {
		return nil, err
	}
	resolvedRelative, err := filepath.Rel(resolvedRuntime, resolvedArtifact)
	if err != nil || resolvedRelative == ".." || filepath.IsAbs(resolvedRelative) || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("published artifact %q resolves outside the run runtime", relative)
	}
	file, err := os.Open(resolvedArtifact)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("artifact %q exceeds byte limit %d", relative, maxBytes)
	}
	return &Artifact{RunID: runID, Path: relative, Content: string(data), Size: int64(len(data))}, nil
}
