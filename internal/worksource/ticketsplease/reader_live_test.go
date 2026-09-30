package ticketsplease

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/worksource"
)

func TestLiveSmithProjectReadOnly(t *testing.T) {
	if os.Getenv("SMITH_LIVE_TICKETS_PLEASE") != "1" {
		t.Skip("set SMITH_LIVE_TICKETS_PLEASE=1 to read the authoritative work service")
	}
	endpoint := os.Getenv("SMITH_TICKETS_PLEASE_ENDPOINT")
	project := os.Getenv("SMITH_TICKETS_PLEASE_PROJECT")
	if endpoint == "" || project == "" {
		t.Fatal("SMITH_TICKETS_PLEASE_ENDPOINT and SMITH_TICKETS_PLEASE_PROJECT are required")
	}
	reader := New(Config{
		Endpoint: endpoint, BootstrapProject: project,
		BearerToken: os.Getenv("SMITH_TICKETS_PLEASE_BEARER_TOKEN"),
		AgentName:   "Smith live work source",
	})
	t.Cleanup(func() { _ = reader.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	projects, err := reader.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(projects, func(value worksource.Project) bool { return value.Slug == project }) {
		t.Fatalf("project %q absent from %d projects", project, len(projects))
	}
	phases, err := reader.ListPhases(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if project == "smith" && !slices.ContainsFunc(phases, func(value worksource.Phase) bool { return value.Slug == "one-instrument" }) {
		t.Fatalf("one-instrument absent from %d Smith phases", len(phases))
	}
	page, err := reader.ListTickets(ctx, worksource.ListTicketsRequest{ProjectIDOrSlug: project, ReadyOnly: true, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tickets) == 0 {
		t.Fatal("authoritative project returned no ready work")
	}
	search, err := reader.Search(ctx, worksource.SearchRequest{
		ProjectIDOrSlug: project, Kind: worksource.SearchTickets,
		Query: "source work memory instrument", Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Hits) == 0 || len(search.FeedbackKeys) != len(search.Hits) {
		t.Fatalf("search hits=%d feedback_keys=%d", len(search.Hits), len(search.FeedbackKeys))
	}
	// This is a read proof. Returning feedback keys is explicit; the reader has
	// no method that can rate them and therefore cannot mutate search feedback.
}
