// Package worksource defines Smith's transport-neutral read view over durable
// work. Implementations may be remote today and in-process later; callers see
// the same Smith-owned types either way.
package worksource

import (
	"context"
	"errors"
	"time"
)

var (
	ErrUnavailable = errors.New("work source is unavailable")
	ErrUpstream    = errors.New("work source upstream failed")
)

type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Project struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	CreatedBy   *Person   `json:"created_by,omitempty"`
}

type Phase struct {
	ID                string    `json:"id"`
	ProjectID         string    `json:"project_id"`
	Number            int       `json:"number"`
	Slug              string    `json:"slug"`
	Name              string    `json:"name"`
	Description       string    `json:"description,omitempty"`
	Summary           string    `json:"summary,omitempty"`
	ActiveTicketCount int       `json:"active_ticket_count"`
	TicketCount       int       `json:"ticket_count"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	CreatedBy         *Person   `json:"created_by,omitempty"`
}

type Ticket struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"project_id"`
	PhaseID         string     `json:"phase_id,omitempty"`
	Number          int        `json:"number,omitempty"`
	Title           string     `json:"title"`
	Body            string     `json:"body,omitempty"`
	Column          string     `json:"column"`
	Kind            string     `json:"kind"`
	Wave            int        `json:"wave"`
	Archived        bool       `json:"archived"`
	BlockedBy       []string   `json:"blocked_by"`
	DependsOn       []string   `json:"depends_on"`
	ParallelWith    []string   `json:"parallelizable_with"`
	Learnings       *string    `json:"learnings,omitempty"`
	TestingEvidence *string    `json:"testing_evidence,omitempty"`
	WorkSummary     *string    `json:"work_summary,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	ArchivedAt      *time.Time `json:"archived_at,omitempty"`
	CreatedBy       *Person    `json:"created_by,omitempty"`
	CompletedBy     *Person    `json:"completed_by,omitempty"`
}

type Comment struct {
	ID         string    `json:"id"`
	TicketID   string    `json:"ticket_id"`
	Kind       string    `json:"kind"`
	Body       string    `json:"body"`
	FromColumn *string   `json:"from_column,omitempty"`
	ToColumn   *string   `json:"to_column,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	Author     *Person   `json:"author,omitempty"`
}

type ListTicketsRequest struct {
	ProjectIDOrSlug string
	PhaseIDOrSlug   string
	Column          string
	ReadyOnly       bool
	IncludeArchived bool
	IncludeIdeas    bool
	Wave            *int
	Cursor          string
	Limit           int
}

type TicketPage struct {
	Tickets    []Ticket `json:"tickets"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

type TicketDetail struct {
	Ticket   Ticket    `json:"ticket"`
	Comments []Comment `json:"comments"`
}

type SearchKind string

const (
	SearchTickets   SearchKind = "tickets"
	SearchLearnings SearchKind = "learnings"
	SearchComments  SearchKind = "comments"
)

type SearchRequest struct {
	ProjectIDOrSlug string
	Kind            SearchKind
	Query           string
	Columns         []string
	TicketID        string
	IncludeArchived bool
	IncludeIdeas    bool
	Limit           int
}

type SearchHit struct {
	EntryKey    string     `json:"entry_key"`
	Kind        SearchKind `json:"kind"`
	TicketID    string     `json:"ticket_id"`
	TicketTitle string     `json:"ticket_title,omitempty"`
	Text        string     `json:"text"`
	Score       float64    `json:"score"`
	Ticket      *Ticket    `json:"ticket,omitempty"`
	Comment     *Comment   `json:"comment,omitempty"`
}

type SearchPage struct {
	Hits         []SearchHit `json:"hits"`
	FeedbackKeys []string    `json:"feedback_keys"`
}

// Reader is the work plane made available to Smith's service and source rack.
// It deliberately contains no mutation or search-rating operation.
type Reader interface {
	ListProjects(context.Context) ([]Project, error)
	ListPhases(context.Context, string) ([]Phase, error)
	ListTickets(context.Context, ListTicketsRequest) (TicketPage, error)
	ListIdeas(context.Context, string, string, int) (TicketPage, error)
	GetTicket(context.Context, string, string) (TicketDetail, error)
	Search(context.Context, SearchRequest) (SearchPage, error)
}
