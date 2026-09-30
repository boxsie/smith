package proposal

import (
	"crypto/rand"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/boxsie/smith/internal/tools"
)

// Manifest is the machine-readable proposal definition.
type Manifest struct {
	ID              string           `json:"id"`
	Goal            string           `json:"goal"`
	CreatedAt       string           `json:"created_at"`
	Planner         PlannerInfo      `json:"planner"`
	ObservedState   ObservedState    `json:"observed_state"`
	ModulesResolved []ModuleRecord   `json:"modules_resolved"`
	Operations      []Operation      `json:"operations"`
	Validation      *ValidationResult `json:"validation,omitempty"`
}

// PlannerInfo records how the planner was configured.
type PlannerInfo struct {
	Model        string         `json:"model"`
	Temperature  float64        `json:"temperature"`
	ResolvedFrom string         `json:"resolved_from"`
	ContentHash  string         `json:"content_hash"`
	TokensIn     *int           `json:"tokens_in,omitempty"`
	TokensOut    *int           `json:"tokens_out,omitempty"`
	CostUSD      *float64       `json:"cost_usd,omitempty"`
	DurationMS   *int64         `json:"duration_ms,omitempty"`
	Stages       []StageMetrics `json:"stages,omitempty"`
}

// StageMetrics records per-stage provenance for staged planners.
type StageMetrics struct {
	TaskID     string   `json:"task_id"`
	Model      string   `json:"model"`
	Cached     bool     `json:"cached"`
	TokensIn   *int     `json:"tokens_in,omitempty"`
	TokensOut  *int     `json:"tokens_out,omitempty"`
	CostUSD    *float64 `json:"cost_usd,omitempty"`
	DurationMS *int64   `json:"duration_ms,omitempty"`
}

// ObservedState captures the target project state at planning time.
type ObservedState struct {
	Hash      string `json:"hash"`
	TaskCount int    `json:"task_count"`
	Empty     bool   `json:"empty"`
}

// ModuleRecord records a resolved module reference for provenance.
type ModuleRecord struct {
	Name         string `json:"name"`
	ResolvedFrom string `json:"resolved_from"`
	ContentHash  string `json:"content_hash"`
}

// Operation represents a single file operation in the proposal.
type Operation struct {
	Op       string `json:"op"`
	Path     string `json:"path"`
	Source   string `json:"source,omitempty"`
	BaseHash string `json:"base_hash,omitempty"`
}

// ValidationResult records the pre-validation or post-apply outcome.
type ValidationResult struct {
	Status      string   `json:"status"`
	ValidatedAt string   `json:"validated_at"`
	Errors      []string `json:"errors,omitempty"`
}

// Proposal represents a complete proposal on disk.
type Proposal struct {
	Dir      string
	Manifest Manifest
	Summary  string
}

// GenerateID produces a unique proposal ID in the format YYYYMMDD-HHMMSS-<6-hex>.
func GenerateID() string {
	now := time.Now().UTC()
	b := make([]byte, 3)
	rand.Read(b)
	return fmt.Sprintf("%s-%x", now.Format("20060102-150405"), b)
}

// ProposalDir returns the proposal directory path for a given ID.
func ProposalDir(projectRoot, id string) string {
	return filepath.Join(projectRoot, ".smith", "proposals", id)
}

// FilesDir returns the staged files directory within a proposal.
func FilesDir(proposalDir string) string {
	return filepath.Join(proposalDir, "files")
}

// ValidateOperationPaths checks that all operation paths are safe:
// no path traversal outside targetDir, no writes into .smith/.
func ValidateOperationPaths(targetDir, proposalDir string, ops []Operation) error {
	for _, op := range ops {
		if op.Path == "" {
			return fmt.Errorf("operation has empty path")
		}

		// Reject path traversal and .smith/ targets.
		if _, err := tools.SafeResolve(targetDir, op.Path); err != nil {
			return fmt.Errorf("operation path %q: %w", op.Path, err)
		}
		if tools.HasSmithComponent(op.Path) {
			return fmt.Errorf("operation path %q: targets .smith/ directory", op.Path)
		}

		// For write ops, validate source resolves inside files/.
		if op.Op == "write" {
			if op.Source == "" {
				return fmt.Errorf("write operation for %q has empty source", op.Path)
			}
			// Source must start with the files/ prefix.
			if !strings.HasPrefix(op.Source, "files/") {
				return fmt.Errorf("operation source %q must start with files/", op.Source)
			}
			// The path after the prefix must resolve inside the files/ directory.
			// SafeResolve handles symlink resolution and containment checking.
			filesDir := FilesDir(proposalDir)
			relInFiles := strings.TrimPrefix(op.Source, "files/")
			if _, err := tools.SafeResolve(filesDir, relInFiles); err != nil {
				return fmt.Errorf("operation source %q: %w", op.Source, err)
			}
		}
	}
	return nil
}
