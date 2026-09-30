package renderer

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/contextsource"
)

func TestLiveMemoryRender(t *testing.T) {
	if os.Getenv("SMITH_LIVE_MEMORY") != "1" {
		t.Skip("set SMITH_LIVE_MEMORY=1 to invoke the installed memory renderer")
	}
	memoryDir := os.Getenv("SMITH_MEMORY_DIR")
	if memoryDir == "" {
		t.Fatal("SMITH_MEMORY_DIR is required")
	}
	factory := contextsource.NewFactory()
	if err := factory.Register(Source, New(Config{Executable: os.Getenv("SMITH_MEMORY_RENDERER"), MemoryDir: memoryDir})); err != nil {
		t.Fatal(err)
	}
	resolved, err := contextsource.ResolveAll(context.Background(), factory, contextsource.Request{}, []contextsource.Declaration{{
		Source: Source, Options: map[string]any{
			"body": "subagent", "context": []any{"project_smith"},
			"memories": []any{"project_smith_maxmsp_vision"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	artifact := resolved[0].Artifacts[0]
	if artifact.SHA256 == "" || artifact.Revision == "" || !strings.Contains(artifact.Content, "## where you are") || artifact.Metadata["body"] != "subagent" || artifact.Metadata["context_items"] == "0" {
		t.Fatalf("live artifact lacks expected provenance: %#v", artifact)
	}
	if len(resolved[0].Artifacts) != 2 || resolved[0].Artifacts[1].Name != "project_smith_maxmsp_vision" || resolved[0].Artifacts[1].SHA256 == "" || !strings.Contains(strings.ToLower(resolved[0].Artifacts[1].Content), "smith") {
		t.Fatalf("live explicit memory was not resolved: count=%d", len(resolved[0].Artifacts))
	}
}
