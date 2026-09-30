package service

import (
	"context"
	"fmt"

	"github.com/boxsie/smith/internal/memorysource"
)

func (s *Service) MemorySourceAvailable() bool { return s != nil && s.memorySource != nil }

func (s *Service) MemorySnapshot(ctx context.Context) (memorysource.Snapshot, error) {
	reader, err := s.memoryReader()
	if err != nil {
		return memorysource.Snapshot{}, err
	}
	return reader.Snapshot(ctx)
}

func (s *Service) ListMemories(ctx context.Context, request memorysource.ListRequest) (memorysource.MemoryPage, error) {
	reader, err := s.memoryReader()
	if err != nil {
		return memorysource.MemoryPage{}, err
	}
	return reader.List(ctx, request)
}

func (s *Service) InspectMemory(ctx context.Context, slug string) (memorysource.Memory, error) {
	reader, err := s.memoryReader()
	if err != nil {
		return memorysource.Memory{}, err
	}
	return reader.Get(ctx, slug)
}

func (s *Service) MemoryReview(ctx context.Context, request memorysource.ReviewRequest) (memorysource.ReviewPage, error) {
	reader, err := s.memoryReader()
	if err != nil {
		return memorysource.ReviewPage{}, err
	}
	return reader.Review(ctx, request)
}

func (s *Service) MemoryAudit(ctx context.Context) (memorysource.AuditState, error) {
	reader, err := s.memoryReader()
	if err != nil {
		return memorysource.AuditState{}, err
	}
	return reader.Audit(ctx)
}

func (s *Service) RenderMemory(ctx context.Context, request memorysource.RenderRequest) (memorysource.RenderArtifact, error) {
	reader, err := s.memoryReader()
	if err != nil {
		return memorysource.RenderArtifact{}, err
	}
	return reader.Render(ctx, request)
}

func (s *Service) memoryReader() (memorysource.Reader, error) {
	if s == nil || s.memorySource == nil {
		return nil, fmt.Errorf("%w: configure a memory renderer engine or remote adapter", memorysource.ErrUnavailable)
	}
	return s.memorySource, nil
}
