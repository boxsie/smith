package renderer

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/boxsie/smith/internal/memorysource"
)

func TestLiveLocalMemoryReadOnly(t *testing.T) {
	if os.Getenv("SMITH_LIVE_MEMORY") != "1" {
		t.Skip("set SMITH_LIVE_MEMORY=1 to read a local memory renderer API")
	}
	endpoint := os.Getenv("SMITH_MEMORY_ENDPOINT")
	body := os.Getenv("SMITH_MEMORY_BODY")
	if endpoint == "" || body == "" {
		t.Fatal("SMITH_MEMORY_ENDPOINT and SMITH_MEMORY_BODY are required")
	}
	reader := New(Config{Endpoint: endpoint, BearerToken: os.Getenv("SMITH_MEMORY_BEARER_TOKEN")})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	snapshot, err := reader.Snapshot(ctx)
	if err != nil || snapshot.Total == 0 {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	page, err := reader.List(ctx, memorysource.ListRequest{Query: "memory", Limit: 3})
	if err != nil || len(page.Memories) == 0 {
		t.Fatalf("memory page = %#v, %v", page, err)
	}
	detail, err := reader.Get(ctx, page.Memories[0].Slug)
	if err != nil || detail.Body == "" {
		t.Fatalf("memory detail = %#v, %v", detail, err)
	}
	if _, err := reader.Review(ctx, memorysource.ReviewRequest{Shelf: "waiting", Limit: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Audit(ctx); err != nil {
		t.Fatal(err)
	}
	rendered, err := reader.Render(ctx, memorysource.RenderRequest{Body: body, Context: "none"})
	if err != nil || !strings.HasPrefix(rendered.Hash, "sha256:") || rendered.Text == "" || rendered.Snapshot != snapshot.Snapshot {
		t.Fatalf("render = %#v, %v", rendered, err)
	}
}
