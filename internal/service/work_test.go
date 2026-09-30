package service

import (
	"context"
	"errors"
	"testing"

	"github.com/boxsie/smith/internal/worksource"
)

type fakeWorkReader struct {
	lastProject string
	lastSearch  worksource.SearchRequest
}

func (f *fakeWorkReader) ListProjects(context.Context) ([]worksource.Project, error) {
	return []worksource.Project{{ID: "project-1", Slug: "smith", Name: "smith"}}, nil
}

func (f *fakeWorkReader) ListPhases(_ context.Context, project string) ([]worksource.Phase, error) {
	f.lastProject = project
	return []worksource.Phase{{ID: "phase-7", Slug: "one-instrument"}}, nil
}

func (f *fakeWorkReader) ListTickets(_ context.Context, request worksource.ListTicketsRequest) (worksource.TicketPage, error) {
	f.lastProject = request.ProjectIDOrSlug
	return worksource.TicketPage{Tickets: []worksource.Ticket{{ID: "ticket-1"}}}, nil
}

func (f *fakeWorkReader) ListIdeas(_ context.Context, project, cursor string, limit int) (worksource.TicketPage, error) {
	f.lastProject = project
	return worksource.TicketPage{Tickets: []worksource.Ticket{{ID: "idea-1", Kind: "idea"}}, NextCursor: cursor}, nil
}

func (f *fakeWorkReader) GetTicket(_ context.Context, project, ticketID string) (worksource.TicketDetail, error) {
	f.lastProject = project
	return worksource.TicketDetail{Ticket: worksource.Ticket{ID: ticketID}}, nil
}

func (f *fakeWorkReader) Search(_ context.Context, request worksource.SearchRequest) (worksource.SearchPage, error) {
	f.lastSearch = request
	return worksource.SearchPage{Hits: []worksource.SearchHit{{EntryKey: "ticket:ticket-1"}}}, nil
}

func TestServiceExposesOneWorkReaderBoundary(t *testing.T) {
	reader := &fakeWorkReader{}
	service := New(Dependencies{WorkSource: reader})
	ctx := context.Background()
	if !service.WorkSourceAvailable() {
		t.Fatal("configured work source is unavailable")
	}
	if projects, err := service.ListWorkProjects(ctx); err != nil || len(projects) != 1 {
		t.Fatalf("projects = %#v, %v", projects, err)
	}
	if phases, err := service.ListWorkPhases(ctx, "smith"); err != nil || len(phases) != 1 || reader.lastProject != "smith" {
		t.Fatalf("phases = %#v, project=%q, err=%v", phases, reader.lastProject, err)
	}
	if page, err := service.ListWorkTickets(ctx, worksource.ListTicketsRequest{ProjectIDOrSlug: "smith"}); err != nil || len(page.Tickets) != 1 {
		t.Fatalf("tickets = %#v, %v", page, err)
	}
	if page, err := service.ListWorkIdeas(ctx, "smith", "cursor", 2); err != nil || page.NextCursor != "cursor" {
		t.Fatalf("ideas = %#v, %v", page, err)
	}
	if detail, err := service.InspectWorkTicket(ctx, "smith", "ticket-1"); err != nil || detail.Ticket.ID != "ticket-1" {
		t.Fatalf("ticket = %#v, %v", detail, err)
	}
	request := worksource.SearchRequest{ProjectIDOrSlug: "smith", Kind: worksource.SearchTickets, Query: "instrument"}
	if page, err := service.SearchWork(ctx, request); err != nil || len(page.Hits) != 1 || reader.lastSearch.Query != "instrument" {
		t.Fatalf("search = %#v, request=%#v, err=%v", page, reader.lastSearch, err)
	}
}

func TestServiceReportsMissingWorkSource(t *testing.T) {
	service := New(Dependencies{})
	if service.WorkSourceAvailable() {
		t.Fatal("unconfigured work source is available")
	}
	if _, err := service.ListWorkProjects(context.Background()); !errors.Is(err, worksource.ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
}
