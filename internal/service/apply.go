package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/boxsie/smith/internal/proposal"
)

// ApplyRequest describes proposal application independently of CLI flags.
type ApplyRequest struct {
	ProposalDir string
	DryRun      bool
	Force       bool
}

// ApplyResult includes the resolved paths used for proposal application.
type ApplyResult struct {
	ProposalDir string
	TargetDir   string
	Apply       *proposal.ApplyResult
}

// Apply validates and applies a Smith proposal.
func (s *Service) Apply(ctx context.Context, request ApplyRequest) (*ApplyResult, error) {
	proposalDir, err := filepath.Abs(request.ProposalDir)
	if err != nil {
		return nil, fmt.Errorf("resolve proposal path: %w", err)
	}
	if _, err := os.Stat(filepath.Join(proposalDir, "manifest.json")); err != nil {
		return nil, fmt.Errorf("invalid proposal: %s does not contain manifest.json", proposalDir)
	}

	targetDir, err := proposal.FindApplyTarget(proposalDir)
	if err != nil {
		return nil, fmt.Errorf("find apply target: %w", err)
	}

	s.emit(ctx, Event{Operation: "apply", State: "started", AppRoot: targetDir})
	result, err := s.apply(proposal.ApplyInput{
		ProposalDir: proposalDir,
		TargetDir:   targetDir,
		DryRun:      request.DryRun,
		Force:       request.Force,
	})
	if err != nil {
		s.emit(ctx, Event{Operation: "apply", State: "failed", AppRoot: targetDir, Err: err})
		return nil, err
	}
	s.emit(ctx, Event{Operation: "apply", State: "completed", AppRoot: targetDir})

	return &ApplyResult{
		ProposalDir: proposalDir,
		TargetDir:   targetDir,
		Apply:       result,
	}, nil
}
