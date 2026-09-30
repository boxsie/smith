package service

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/boxsie/smith/internal/plan"
)

// PlanRequest describes proposal generation independently of CLI flags.
type PlanRequest struct {
	TargetDir     string
	Goal          string
	ModelOverride string
	MaxCostUSD    float64
}

// PlanResult contains the proposal and non-fatal service warnings.
type PlanResult struct {
	TargetDir string
	Plan      *plan.PlanResult
	Warnings  []error
}

// Plan generates a proposal and records the target as a known Smith project.
func (s *Service) Plan(ctx context.Context, request PlanRequest) (*PlanResult, error) {
	targetDir, err := filepath.Abs(request.TargetDir)
	if err != nil {
		return nil, fmt.Errorf("resolve target path: %w", err)
	}

	s.emit(ctx, Event{Operation: "plan", State: "started", AppRoot: targetDir})
	result, err := s.plan(ctx, plan.PlanInput{
		TargetDir:     targetDir,
		Goal:          request.Goal,
		ModelOverride: request.ModelOverride,
		MaxCostUSD:    request.MaxCostUSD,
		Factory:       s.factory,
	})
	if err != nil {
		s.emit(ctx, Event{Operation: "plan", State: "failed", AppRoot: targetDir, Err: err})
		return nil, err
	}

	s.emit(ctx, Event{Operation: "plan", State: "completed", AppRoot: targetDir})
	return &PlanResult{
		TargetDir: targetDir,
		Plan:      result,
		Warnings:  s.track(targetDir),
	}, nil
}
