package lib

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedWebToolAssetsExist(t *testing.T) {
	required := []string{
		"builtin/tools/web.lookup/tool.yaml",
		"builtin/tools/web.lookup/input.schema.json",
		"builtin/tools/web.lookup/output.schema.json",
		"builtin/tools/web.fetch/tool.yaml",
		"builtin/tools/web.fetch/input.schema.json",
		"builtin/tools/web.fetch/output.schema.json",
		"builtin/tools/web.fetch_markdown/tool.yaml",
		"builtin/tools/web.fetch_markdown/input.schema.json",
		"builtin/tools/web.fetch_markdown/output.schema.json",
		"builtin/tools/web.summarize/tool.yaml",
		"builtin/tools/web.summarize/input.schema.json",
		"builtin/tools/web.summarize/output.schema.json",
		"builtin/web-summarize/agent.md",
		"builtin/web-summarize/task.md",
		"builtin/web-summarize/tools.md",
		"builtin/web-summarize/schema.md",
	}
	for _, path := range required {
		if _, err := fs.ReadFile(embeddedFS, path); err != nil {
			t.Errorf("missing embedded web asset %s: %v", path, err)
		}
	}
}

func TestEmbeddedWebToolsDoNotShipRunSh(t *testing.T) {
	unexpected := []string{
		"builtin/tools/web.lookup/run.sh",
		"builtin/tools/web.fetch/run.sh",
		"builtin/tools/web.fetch_markdown/run.sh",
		"builtin/tools/web.summarize/run.sh",
	}
	for _, path := range unexpected {
		if _, err := fs.ReadFile(embeddedFS, path); err == nil {
			t.Errorf("unexpected embedded file %s", path)
		}
	}
}

func TestEmbeddedWebSummarizeModuleContract(t *testing.T) {
	agent, err := fs.ReadFile(embeddedFS, "builtin/web-summarize/agent.md")
	if err != nil {
		t.Fatalf("read agent.md: %v", err)
	}
	for _, want := range []string{
		"model: anthropic/claude-sonnet-4-6",
		"temperature: 0",
		"max_tokens: 4096",
	} {
		if !strings.Contains(string(agent), want) {
			t.Errorf("agent.md missing %q", want)
		}
	}

	toolsMD, err := fs.ReadFile(embeddedFS, "builtin/web-summarize/tools.md")
	if err != nil {
		t.Fatalf("read tools.md: %v", err)
	}
	if strings.TrimSpace(string(toolsMD)) != "- web.fetch_markdown" {
		t.Fatalf("tools.md = %q, want only web.fetch_markdown", strings.TrimSpace(string(toolsMD)))
	}

	toolSchema, err := fs.ReadFile(embeddedFS, "builtin/tools/web.summarize/output.schema.json")
	if err != nil {
		t.Fatalf("read web.summarize output.schema.json: %v", err)
	}
	moduleSchema, err := fs.ReadFile(embeddedFS, "builtin/web-summarize/schema.md")
	if err != nil {
		t.Fatalf("read web-summarize/schema.md: %v", err)
	}
	if strings.TrimSpace(string(toolSchema)) != strings.TrimSpace(string(moduleSchema)) {
		t.Fatal("web.summarize output.schema.json must match web-summarize/schema.md")
	}
}

func TestEmbeddedWebSummarizeTaskPromptContract(t *testing.T) {
	taskMD, err := fs.ReadFile(embeddedFS, "builtin/web-summarize/task.md")
	if err != nil {
		t.Fatalf("read task.md: %v", err)
	}
	content := string(taskMD)

	wants := []string{
		"Call `web.fetch_markdown` exactly once",
		"If `web.fetch_markdown` returns a non-2xx `status` or empty `markdown`, do not fail.",
		"`summary: \"\"`",
		"`key_points: []`",
		"propagated `warnings`",
		"if `focus` is present, use it to steer emphasis without inventing facts",
		"Return only JSON matching `schema.md`",
	}
	for _, want := range wants {
		if !strings.Contains(content, want) {
			t.Errorf("task.md missing %q", want)
		}
	}
}
