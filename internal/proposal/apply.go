package proposal

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/boxsie/smith/internal/validate"
)

// ApplyInput holds inputs for the apply operation.
type ApplyInput struct {
	ProposalDir string
	TargetDir   string
	DryRun      bool
	Force       bool
}

// ApplyResult holds the outcome of an apply.
type ApplyResult struct {
	Applied    []Operation
	Conflicts  []Conflict
	Validation *ValidationResult
}

// Apply materializes a proposal into the target directory.
func Apply(input ApplyInput) (*ApplyResult, error) {
	result := &ApplyResult{}

	// Read manifest.
	manifest, err := ReadManifest(input.ProposalDir)
	if err != nil {
		return nil, fmt.Errorf("read proposal: %w", err)
	}

	// Validate operation paths (traversal, .smith/ protection).
	if err := ValidateOperationPaths(input.TargetDir, input.ProposalDir, manifest.Operations); err != nil {
		return nil, fmt.Errorf("validate operation paths: %w", err)
	}

	// Verify staged files exist.
	if err := ValidateManifestFiles(input.ProposalDir, manifest.Operations); err != nil {
		return nil, fmt.Errorf("validate staged files: %w", err)
	}

	// Check conflicts (unless forced).
	if !input.Force {
		conflicts, err := CheckConflicts(input.TargetDir, manifest.Operations)
		if err != nil {
			return nil, fmt.Errorf("check conflicts: %w", err)
		}
		if len(conflicts) > 0 {
			result.Conflicts = conflicts
			return result, nil
		}
	}

	// Dry run — return operations without writing.
	if input.DryRun {
		result.Applied = manifest.Operations
		return result, nil
	}

	// Execute operations in order.
	for _, op := range manifest.Operations {
		targetPath := filepath.Join(input.TargetDir, op.Path)

		switch op.Op {
		case "write":
			sourcePath := filepath.Join(input.ProposalDir, op.Source)
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return nil, fmt.Errorf("create dirs for %q: %w", op.Path, err)
			}
			if err := copyFile(sourcePath, targetPath); err != nil {
				return nil, fmt.Errorf("write %q: %w", op.Path, err)
			}

		case "delete":
			if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("delete %q: %w", op.Path, err)
			}

		default:
			return nil, fmt.Errorf("unknown operation: %q", op.Op)
		}
	}

	result.Applied = manifest.Operations

	// Post-apply validation.
	vr := validate.Validate(input.TargetDir)
	result.Validation = &ValidationResult{
		ValidatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if len(vr.Errs) > 0 {
		result.Validation.Status = "fail"
		for _, e := range vr.Errs {
			result.Validation.Errors = append(result.Validation.Errors, e.Error())
		}
	} else {
		result.Validation.Status = "pass"
	}

	return result, nil
}

// FindApplyTarget walks up from proposalDir to find the nearest ancestor
// directory that contains a .smith/ directory. Returns the target directory
// or an error if no ancestor qualifies.
func FindApplyTarget(proposalDir string) (string, error) {
	absDir, err := filepath.Abs(proposalDir)
	if err != nil {
		return "", err
	}

	// Walk up the directory tree.
	dir := absDir
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root without finding .smith/.
			return "", fmt.Errorf("no .smith/ directory found in any ancestor of %s", proposalDir)
		}
		dir = parent

		smithDir := filepath.Join(dir, ".smith")
		info, err := os.Stat(smithDir)
		if err == nil && info.IsDir() {
			return dir, nil
		}
	}
}
