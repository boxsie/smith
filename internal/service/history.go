package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/boxsie/smith/internal/run"
	"github.com/boxsie/smith/internal/runtime"
)

// Status reads a specific run manifest, or the latest when runID is empty.
func (s *Service) Status(path, runID string) (run.Manifest, error) {
	absRoot, err := filepath.Abs(path)
	if err != nil {
		return run.Manifest{}, fmt.Errorf("resolve app path: %w", err)
	}

	if runID == "" {
		runDir, err := run.LatestRunDir(absRoot)
		if err != nil {
			return run.Manifest{}, fmt.Errorf("no runs found for %s: %w", absRoot, err)
		}
		return s.reconcileRun(runDir)
	}

	runDir := run.RunDir(absRoot, runID)
	manifest, err := s.reconcileRun(runDir)
	if err != nil {
		return run.Manifest{}, fmt.Errorf("read manifest for run %s: %w", runID, err)
	}
	return manifest, nil
}

// ListRuns returns run manifests newest-first.
func (s *Service) ListRuns(path string) ([]run.Manifest, error) {
	absRoot, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve app path: %w", err)
	}
	dirs, err := run.ListRunDirs(absRoot)
	if err != nil {
		return nil, err
	}
	manifests := make([]run.Manifest, 0, len(dirs))
	for _, runDir := range dirs {
		manifest, reconcileErr := s.reconcileRun(runDir)
		if reconcileErr != nil {
			if _, eventErr := os.Stat(run.EventsPath(runDir)); eventErr == nil {
				return nil, reconcileErr
			}
			continue
		}
		manifests = append(manifests, manifest)
	}
	return manifests, nil
}

func (s *Service) reconcileRun(runDir string) (manifest run.Manifest, err error) {
	if s.runDirIsActive(runDir) {
		return run.ReadManifest(run.ManifestPath(runDir))
	}
	lease, err := s.acquireRunLease(runDir)
	if errors.Is(err, run.ErrRunLeaseHeld) {
		return run.ReadManifest(run.ManifestPath(runDir))
	}
	if err != nil {
		return run.Manifest{}, err
	}
	defer func() {
		err = errors.Join(err, releaseRunLease(lease))
	}()

	store, err := run.NewEventStore(runDir, s.clock)
	if err != nil {
		return run.Manifest{}, err
	}
	manifest, eventBacked, err := run.ReconcileManifest(runDir)
	if err != nil {
		return run.Manifest{}, err
	}
	if !eventBacked || manifest.Status != "running" {
		return manifest, nil
	}

	for _, attempt := range store.AttemptStates() {
		if attempt.Status == run.AttemptTerminal {
			continue
		}
		if _, err := store.Append(run.Event{
			Type: run.EventAttemptTerminal, RunID: manifest.RunID,
			InvocationID: attempt.Spec.ID, ParentInvocationID: attempt.Spec.TaskInvocationID,
			TaskID: attempt.Spec.TaskID, AttemptID: attempt.Spec.ID, AttemptOrdinal: attempt.Spec.Ordinal,
			AttemptCondition: &run.AttemptCondition{
				Status: run.AttemptTerminal, Reason: runtime.TerminalHostLoss,
				Message: "service owner disappeared before the attempt reached a terminal state",
			},
		}); err != nil {
			return run.Manifest{}, err
		}
	}

	if _, err := store.Append(run.Event{
		Type:  run.EventRunInterrupted,
		RunID: manifest.RunID,
		Error: "service restarted before the run reached a terminal state",
	}); err != nil {
		return run.Manifest{}, err
	}
	return run.ReadManifest(run.ManifestPath(runDir))
}

func (s *Service) runDirIsActive(runDir string) bool {
	s.runsMu.Lock()
	var active *activeRun
	for _, candidate := range s.runs {
		if candidate.handle.RunDir == runDir {
			active = candidate
			break
		}
	}
	s.runsMu.Unlock()
	if active == nil {
		return false
	}
	select {
	case <-active.done:
		return false
	default:
		return true
	}
}

// PruneResult reports the result of run-history pruning.
type PruneResult struct {
	Removed int
	Kept    int
}

// PruneRuns removes old run directories according to opts.
func (s *Service) PruneRuns(path string, opts run.PruneOpts) (PruneResult, error) {
	absRoot, err := filepath.Abs(path)
	if err != nil {
		return PruneResult{}, fmt.Errorf("resolve app path: %w", err)
	}
	if _, err := s.ListRuns(absRoot); err != nil {
		return PruneResult{}, err
	}
	removed, kept, err := run.PruneRuns(absRoot, opts)
	if err != nil {
		return PruneResult{}, err
	}
	return PruneResult{Removed: removed, Kept: kept}, nil
}
