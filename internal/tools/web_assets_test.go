package tools

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNativeCatalogIncludesWebToolIDs(t *testing.T) {
	ids := NativeToolIDs()
	for _, want := range []string{"web.lookup", "web.fetch", "web.fetch_markdown"} {
		if !slices.Contains(ids, want) {
			t.Fatalf("NativeToolIDs() missing %q", want)
		}
		reg, ok := lookupNativeRegistration(want)
		if !ok || reg.Version == "" || reg.Func == nil {
			t.Fatalf("native registration for %q is incomplete: %+v", want, reg)
		}
	}
}

func TestDiscoverAndExtract_SharedWebToolLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectRoot := t.TempDir()

	resolved, err := DiscoverAndExtract(projectRoot, BuiltinToolIDs, NativeToolIDs())
	if err != nil {
		t.Fatalf("DiscoverAndExtract: %v", err)
	}

	for _, id := range []string{"web.lookup", "web.fetch", "web.fetch_markdown", "web.summarize"} {
		def := resolved.AppDefs[id]
		if def == nil {
			t.Fatalf("missing discovered web tool %q", id)
		}
		if def.OutputSchema == nil {
			t.Fatalf("%s should have output.schema.json loaded", id)
		}
		if def.RunShPath != "" {
			t.Fatalf("%s should not ship run.sh", id)
		}
	}

	if got := resolved.AppDefs["web.lookup"].ToolYAML.Type; got != "native" {
		t.Fatalf("web.lookup type = %q, want native", got)
	}
	if got := resolved.AppDefs["web.fetch"].ToolYAML.Type; got != "native" {
		t.Fatalf("web.fetch type = %q, want native", got)
	}
	if got := resolved.AppDefs["web.fetch_markdown"].ToolYAML.Type; got != "native" {
		t.Fatalf("web.fetch_markdown type = %q, want native", got)
	}
	if got := resolved.AppDefs["web.summarize"].ToolYAML.Type; got != "task" {
		t.Fatalf("web.summarize type = %q, want task", got)
	}
	if resolved.AppDefs["web.summarize"].ToolYAML.Source != "web-summarize" {
		t.Fatalf("web.summarize source = %q, want web-summarize", resolved.AppDefs["web.summarize"].ToolYAML.Source)
	}
	if base := filepath.Base(resolved.AppDefs["web.summarize"].ResolvedSource); base != "web-summarize" {
		t.Fatalf("web.summarize resolved source = %q, want basename web-summarize", resolved.AppDefs["web.summarize"].ResolvedSource)
	}

	userLibDir := filepath.Join(home, ".smith", "lib")
	for _, rel := range []string{
		"tools/web.lookup/tool.yaml",
		"tools/web.fetch/tool.yaml",
		"tools/web.fetch_markdown/tool.yaml",
		"tools/web.summarize/tool.yaml",
		"web-summarize/task.md",
		"web-summarize/agent.md",
		"web-summarize/tools.md",
		"web-summarize/schema.md",
	} {
		if _, err := os.Stat(filepath.Join(userLibDir, rel)); err != nil {
			t.Fatalf("expected extracted asset %s: %v", rel, err)
		}
	}
}

func TestWebFixturesExist(t *testing.T) {
	paths := []string{
		filepath.Join("testdata", "web.lookup", "duckduckgo-html-results.html"),
		filepath.Join("testdata", "web.lookup", "duckduckgo-html-empty.html"),
		filepath.Join("testdata", "web.lookup", "duckduckgo-html-parse-failure.html"),
		filepath.Join("testdata", "web.lookup", "duckduckgo-challenge.html"),
		filepath.Join("testdata", "web.fetch", "page-200.html"),
		filepath.Join("testdata", "web.fetch", "page-404.html"),
		filepath.Join("testdata", "web.fetch", "redirect-target.html"),
		filepath.Join("testdata", "web.fetch", "large-body.txt"),
		filepath.Join("testdata", "web.fetch", "binary-response.bin"),
		filepath.Join("testdata", "web.fetch", "boilerplate-heavy.html"),
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read fixture %s: %v", path, err)
		}
		if len(data) == 0 {
			t.Fatalf("fixture %s should not be empty", path)
		}
	}

	largeBody, err := os.ReadFile(filepath.Join("testdata", "web.fetch", "large-body.txt"))
	if err != nil {
		t.Fatalf("read large-body fixture: %v", err)
	}
	if !strings.Contains(string(largeBody), "large response body") {
		t.Fatal("large-body.txt should describe the large-body scenario")
	}
}
