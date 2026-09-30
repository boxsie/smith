package proposal

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ProvenanceInput holds the runtime-observed data the runner collects
// and feeds into the manifest's provenance fields.
type ProvenanceInput struct {
	Goal            string
	Model           string
	Temperature     float64
	ResolvedFrom    string
	ContentHash     string
	ObservedState   ObservedState
	ModulesResolved []ModuleRecord
	TokensIn        int
	TokensOut       int
	CostUSD         float64
	DurationMS      int64
	StageInputs     []StageInput // per-stage metrics for staged planners
}

// StageInput holds per-stage metrics collected by the runner.
type StageInput struct {
	TaskID     string
	Model      string
	Cached     bool
	TokensIn   int
	TokensOut  int
	CostUSD    float64
	DurationMS int64
}

// BuildManifest constructs a Manifest from runner-observed provenance data.
func BuildManifest(id string, input ProvenanceInput, ops []Operation, validation *ValidationResult) *Manifest {
	m := &Manifest{
		ID:        id,
		Goal:      input.Goal,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Planner: PlannerInfo{
			Model:        input.Model,
			Temperature:  input.Temperature,
			ResolvedFrom: input.ResolvedFrom,
			ContentHash:  input.ContentHash,
		},
		ObservedState:   input.ObservedState,
		ModulesResolved: input.ModulesResolved,
		Operations:      ops,
		Validation:      validation,
	}

	if m.ModulesResolved == nil {
		m.ModulesResolved = []ModuleRecord{}
	}

	// Populate optional metrics if non-zero.
	if input.TokensIn > 0 {
		v := input.TokensIn
		m.Planner.TokensIn = &v
	}
	if input.TokensOut > 0 {
		v := input.TokensOut
		m.Planner.TokensOut = &v
	}
	if input.CostUSD > 0 {
		v := input.CostUSD
		m.Planner.CostUSD = &v
	}
	if input.DurationMS > 0 {
		v := input.DurationMS
		m.Planner.DurationMS = &v
	}

	// Populate per-stage metrics for staged planners.
	if len(input.StageInputs) > 0 {
		for _, si := range input.StageInputs {
			sm := StageMetrics{
				TaskID: si.TaskID,
				Model:  si.Model,
				Cached: si.Cached,
			}
			if si.TokensIn > 0 {
				v := si.TokensIn
				sm.TokensIn = &v
			}
			if si.TokensOut > 0 {
				v := si.TokensOut
				sm.TokensOut = &v
			}
			if si.CostUSD > 0 {
				v := si.CostUSD
				sm.CostUSD = &v
			}
			if si.DurationMS > 0 {
				v := si.DurationMS
				sm.DurationMS = &v
			}
			m.Planner.Stages = append(m.Planner.Stages, sm)
		}
	}

	return m
}

// HashTaskTree computes a deterministic SHA-256 hash of all files under dir,
// excluding output/, .smith/proposals/, and .smith/cache/ directories. .smith/lib/ is included
// because project-local modules affect validation and execution behavior.
// Files are visited in sorted order and each file contributes its relative
// path and content to the hash.
func HashTaskTree(dir string) (string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	// Collect all file paths relative to dir.
	var relPaths []string
	err = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "output" {
				return filepath.SkipDir
			}
			// Under .smith/, only include lib/. Skip proposals/, cache/, and
			// other runner-managed subdirectories.
			rel, relErr := filepath.Rel(absDir, path)
			if relErr == nil {
				slashRel := filepath.ToSlash(rel)
				if slashRel == ".smith/proposals" || hasSlashPrefix(slashRel, ".smith/proposals/") ||
					slashRel == ".smith/cache" || hasSlashPrefix(slashRel, ".smith/cache/") {
					return filepath.SkipDir
				}
			}
			return nil
		}
		rel, err := filepath.Rel(absDir, path)
		if err != nil {
			return err
		}
		relPaths = append(relPaths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk tree: %w", err)
	}

	sort.Strings(relPaths)

	h := sha256.New()
	for _, rel := range relPaths {
		absPath := filepath.Join(absDir, filepath.FromSlash(rel))
		// Hash the relative path.
		fmt.Fprintf(h, "path:%s\n", rel)
		// Hash the file content.
		f, err := os.Open(absPath)
		if err != nil {
			return "", fmt.Errorf("open %s: %w", rel, err)
		}
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", fmt.Errorf("read %s: %w", rel, err)
		}
		f.Close()
		// Separator between files.
		h.Write([]byte{0})
	}

	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

// HashProjectState computes the observed state of a target project directory.
// Excludes .smith/proposals/, .smith/cache/, and output/ directories. Includes .smith/lib/
// because project-local modules affect validation behavior. Counts task.md
// and module.yaml files to determine task count and emptiness.
func HashProjectState(targetDir string) (ObservedState, error) {
	absDir, err := filepath.Abs(targetDir)
	if err != nil {
		return ObservedState{}, err
	}

	// Check if directory exists.
	info, err := os.Stat(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return ObservedState{
				Hash:      "sha256:" + strings.Repeat("0", 64),
				TaskCount: 0,
				Empty:     true,
			}, nil
		}
		return ObservedState{}, err
	}
	if !info.IsDir() {
		return ObservedState{}, fmt.Errorf("not a directory: %s", absDir)
	}

	hash, err := HashTaskTree(absDir)
	if err != nil {
		return ObservedState{}, err
	}

	// Count tasks (excludes .smith/proposals/ and output/, same as HashTaskTree).
	taskCount := 0
	walkErr := filepath.WalkDir(absDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "output" {
				return filepath.SkipDir
			}
			rel, relErr := filepath.Rel(absDir, path)
			if relErr == nil {
				slashRel := filepath.ToSlash(rel)
				if slashRel == ".smith/proposals" || hasSlashPrefix(slashRel, ".smith/proposals/") ||
					slashRel == ".smith/cache" || hasSlashPrefix(slashRel, ".smith/cache/") {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if d.Name() == "task.md" || d.Name() == "module.yaml" {
			taskCount++
		}
		return nil
	})
	if walkErr != nil {
		return ObservedState{}, fmt.Errorf("count tasks: %w", walkErr)
	}

	return ObservedState{
		Hash:      hash,
		TaskCount: taskCount,
		Empty:     taskCount == 0,
	}, nil
}

// hasSlashPrefix checks if s starts with prefix (already slash-separated).
func hasSlashPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
