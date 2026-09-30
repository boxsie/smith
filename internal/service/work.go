package service

import (
	"context"
	"fmt"

	"github.com/boxsie/smith/internal/worksource"
)

func (s *Service) WorkSourceAvailable() bool { return s != nil && s.workSource != nil }

func (s *Service) ListWorkProjects(ctx context.Context) ([]worksource.Project, error) {
	reader, err := s.workReader()
	if err != nil {
		return nil, err
	}
	return reader.ListProjects(ctx)
}

func (s *Service) ListWorkPhases(ctx context.Context, project string) ([]worksource.Phase, error) {
	reader, err := s.workReader()
	if err != nil {
		return nil, err
	}
	return reader.ListPhases(ctx, project)
}

func (s *Service) ListWorkTickets(ctx context.Context, request worksource.ListTicketsRequest) (worksource.TicketPage, error) {
	reader, err := s.workReader()
	if err != nil {
		return worksource.TicketPage{}, err
	}
	return reader.ListTickets(ctx, request)
}

func (s *Service) ListWorkIdeas(ctx context.Context, project, cursor string, limit int) (worksource.TicketPage, error) {
	reader, err := s.workReader()
	if err != nil {
		return worksource.TicketPage{}, err
	}
	return reader.ListIdeas(ctx, project, cursor, limit)
}

func (s *Service) InspectWorkTicket(ctx context.Context, project, ticketID string) (worksource.TicketDetail, error) {
	reader, err := s.workReader()
	if err != nil {
		return worksource.TicketDetail{}, err
	}
	return reader.GetTicket(ctx, project, ticketID)
}

func (s *Service) SearchWork(ctx context.Context, request worksource.SearchRequest) (worksource.SearchPage, error) {
	reader, err := s.workReader()
	if err != nil {
		return worksource.SearchPage{}, err
	}
	return reader.Search(ctx, request)
}

func (s *Service) workReader() (worksource.Reader, error) {
	if s == nil || s.workSource == nil {
		return nil, fmt.Errorf("%w: configure a Smith work engine or remote adapter", worksource.ErrUnavailable)
	}
	return s.workSource, nil
}
