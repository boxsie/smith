package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/boxsie/smith/internal/runtime"
)

// ProposalWrite implements the proposal.write tool.
// Writes files to a proposal's staged files/ directory.
type ProposalWrite struct{}

func (p *ProposalWrite) RequiredScope() []string {
	return []string{ScopeRoot, ScopeProposalID}
}

func (p *ProposalWrite) Definition() runtime.ToolDef {
	return runtime.ToolDef{
		ID:          "proposal.write",
		Description: "Write a file to the proposal staging area",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Relative file path to write"},"content":{"type":"string","description":"File content to write"}},"required":["path","content"]}`),
	}
}

func (p *ProposalWrite) Execute(_ context.Context, input json.RawMessage, scope map[string]string) (json.RawMessage, error) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return nil, fmt.Errorf("invalid input: %w", err)
	}
	if req.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	// Validate proposal_id format to prevent path injection.
	proposalID := scope[ScopeProposalID]
	if err := ValidateProposalID(proposalID); err != nil {
		return nil, err
	}

	// Reject reserved top-level filenames after normalization.
	// Clean the path first so aliases like ./manifest.json or sub/../summary.md
	// don't bypass the guard.
	cleaned := filepath.Clean(filepath.ToSlash(req.Path))
	if cleaned == "manifest.json" || cleaned == "summary.md" {
		return nil, fmt.Errorf("cannot write reserved file: %q", req.Path)
	}

	// Construct and create the base directory for proposal files.
	root := scope[ScopeRoot]
	baseDir := filepath.Join(root, ".smith", "proposals", proposalID, "files")

	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create proposal directory: %w", err)
	}

	// Verify the constructed base directory stays within root (belt-and-suspenders
	// with the proposal_id format validation above).
	if _, err := SafeResolve(root, filepath.Join(".smith", "proposals", proposalID, "files")); err != nil {
		return nil, fmt.Errorf("proposal base directory escapes root: %w", err)
	}

	// Resolve the target file path within the files/ directory.
	resolved, err := SafeResolve(baseDir, req.Path)
	if err != nil {
		return nil, err
	}

	// Create intermediate directories for the target file.
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return nil, fmt.Errorf("create directories: %w", err)
	}
	if err := os.WriteFile(resolved, []byte(req.Content), 0o644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	type result struct {
		Path    string `json:"path"`
		Written bool   `json:"written"`
	}
	return json.Marshal(result{Path: req.Path, Written: true})
}
