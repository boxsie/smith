package proposal

import (
	"fmt"
	"os"
	"path/filepath"
)

// PlannerOutput is the schema the planner's result.json must conform to.
type PlannerOutput struct {
	Summary          string             `json:"summary"`
	Operations       []PlannerOperation `json:"operations"`
	CreateTools      []ToolSpec         `json:"create_tools,omitempty"`
	UseExistingTools []string           `json:"use_existing_tools,omitempty"`
}

// PlannerOperation is an operation as declared by the planner.
type PlannerOperation struct {
	Op      string `json:"op"`
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
}

// AuthorInput holds everything needed to assemble a complete proposal.
type AuthorInput struct {
	ProposalDir   string
	PlannerOutput PlannerOutput
	Provenance    ProvenanceInput
	TargetDir     string
	PreValidation *ValidationResult // when non-nil, skip internal PreValidate
}

// Author assembles a complete proposal from the planner's output.
// It verifies staged files, computes base_hash values, runs pre-validation,
// builds the manifest with provenance, and writes manifest.json + summary.md.
func Author(input AuthorInput) (*Proposal, error) {
	proposalDir := input.ProposalDir
	filesDir := FilesDir(proposalDir)

	// Build operations with source paths and base_hash.
	var ops []Operation
	for _, pop := range input.PlannerOutput.Operations {
		op := Operation{
			Op:   pop.Op,
			Path: pop.Path,
		}

		switch pop.Op {
		case "write":
			// Source is relative to the proposal dir, includes files/ prefix.
			op.Source = filepath.ToSlash(filepath.Join("files", pop.Path))

			// Verify the staged file exists.
			sourcePath := filepath.Join(filesDir, pop.Path)
			if !fileExists(sourcePath) {
				return nil, fmt.Errorf("staged file missing for %q: expected at %s", pop.Path, sourcePath)
			}

			// Compute base_hash for existing target files.
			if input.TargetDir != "" {
				targetFile := filepath.Join(input.TargetDir, pop.Path)
				hash, err := HashFile(targetFile)
				if err != nil {
					return nil, fmt.Errorf("hash existing file %q: %w", pop.Path, err)
				}
				if hash != "" {
					op.BaseHash = hash
				}
			}

		case "delete":
			// Compute base_hash for the file being deleted.
			if input.TargetDir != "" {
				targetFile := filepath.Join(input.TargetDir, pop.Path)
				hash, err := HashFile(targetFile)
				if err != nil {
					return nil, fmt.Errorf("hash existing file %q: %w", pop.Path, err)
				}
				if hash != "" {
					op.BaseHash = hash
				}
			}

		default:
			return nil, fmt.Errorf("unknown operation type: %q", pop.Op)
		}

		ops = append(ops, op)
	}

	// Validate operation paths (traversal, .smith/ protection).
	if err := ValidateOperationPaths(input.TargetDir, proposalDir, ops); err != nil {
		return nil, fmt.Errorf("validate operation paths: %w", err)
	}

	// Pre-validate: use provided result or run internally.
	var validation *ValidationResult
	if input.PreValidation != nil {
		validation = input.PreValidation
	} else {
		var err error
		validation, err = PreValidate(input.TargetDir, proposalDir, ops)
		if err != nil {
			return nil, fmt.Errorf("pre-validate: %w", err)
		}
	}

	// Extract proposal ID from the directory name.
	id := filepath.Base(proposalDir)

	// Build manifest with provenance.
	manifest := BuildManifest(id, input.Provenance, ops, validation)

	// Write manifest.json and summary.md.
	if err := WriteManifest(proposalDir, manifest); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}
	if err := WriteSummary(proposalDir, input.PlannerOutput.Summary); err != nil {
		return nil, fmt.Errorf("write summary: %w", err)
	}

	return &Proposal{
		Dir:      proposalDir,
		Manifest: *manifest,
		Summary:  input.PlannerOutput.Summary,
	}, nil
}

// fileExists returns true if a regular file exists at path.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
