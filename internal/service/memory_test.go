package service

import (
	"context"
	"errors"
	"testing"

	"github.com/boxsie/smith/internal/memorysource"
)

type fakeMemoryReader struct {
	list   memorysource.ListRequest
	review memorysource.ReviewRequest
	render memorysource.RenderRequest
}

func (f *fakeMemoryReader) Snapshot(context.Context) (memorysource.Snapshot, error) {
	return memorysource.Snapshot{Snapshot: "abc", Total: 2}, nil
}
func (f *fakeMemoryReader) List(_ context.Context, request memorysource.ListRequest) (memorysource.MemoryPage, error) {
	f.list = request
	return memorysource.MemoryPage{Memories: []memorysource.Memory{{Slug: "feedback_test"}}}, nil
}
func (f *fakeMemoryReader) Get(_ context.Context, slug string) (memorysource.Memory, error) {
	return memorysource.Memory{Slug: slug, Body: "body"}, nil
}
func (f *fakeMemoryReader) Review(_ context.Context, request memorysource.ReviewRequest) (memorysource.ReviewPage, error) {
	f.review = request
	return memorysource.ReviewPage{Candidates: []memorysource.ReviewCandidate{{Slug: "project_candidate"}}}, nil
}
func (f *fakeMemoryReader) Audit(context.Context) (memorysource.AuditState, error) {
	return memorysource.AuditState{Available: true, History: []memorysource.AuditRun{}}, nil
}
func (f *fakeMemoryReader) Render(_ context.Context, request memorysource.RenderRequest) (memorysource.RenderArtifact, error) {
	f.render = request
	return memorysource.RenderArtifact{Hash: "sha256:123", Text: "prompt"}, nil
}

func TestServiceExposesOneMemoryReaderBoundary(t *testing.T) {
	reader := &fakeMemoryReader{}
	service := New(Dependencies{MemorySource: reader})
	ctx := context.Background()
	if !service.MemorySourceAvailable() {
		t.Fatal("configured memory source is unavailable")
	}
	if snapshot, err := service.MemorySnapshot(ctx); err != nil || snapshot.Total != 2 {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	if page, err := service.ListMemories(ctx, memorysource.ListRequest{Query: "needle"}); err != nil || len(page.Memories) != 1 || reader.list.Query != "needle" {
		t.Fatalf("memories = %#v, %v", page, err)
	}
	if memory, err := service.InspectMemory(ctx, "feedback_test"); err != nil || memory.Body != "body" {
		t.Fatalf("memory = %#v, %v", memory, err)
	}
	if page, err := service.MemoryReview(ctx, memorysource.ReviewRequest{Shelf: "waiting"}); err != nil || len(page.Candidates) != 1 || reader.review.Shelf != "waiting" {
		t.Fatalf("review = %#v, %v", page, err)
	}
	if audit, err := service.MemoryAudit(ctx); err != nil || !audit.Available {
		t.Fatalf("audit = %#v, %v", audit, err)
	}
	if rendered, err := service.RenderMemory(ctx, memorysource.RenderRequest{Body: "codex"}); err != nil || rendered.Hash != "sha256:123" || reader.render.Body != "codex" {
		t.Fatalf("render = %#v, %v", rendered, err)
	}
}

func TestServiceReportsMissingMemorySource(t *testing.T) {
	service := New(Dependencies{})
	if service.MemorySourceAvailable() {
		t.Fatal("unconfigured memory source is available")
	}
	if _, err := service.MemorySnapshot(context.Background()); !errors.Is(err, memorysource.ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
}
