package renderer

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/contextsource"
	"github.com/boxsie/smith/internal/runtime"
)

type fakeRunner struct {
	requests []runtime.ProcessRequest
	results  []runtime.ProcessResult
	errors   []error
}

func (f *fakeRunner) Run(_ context.Context, request runtime.ProcessRequest) (runtime.ProcessResult, error) {
	f.requests = append(f.requests, request)
	index := len(f.requests) - 1
	var err error
	if index < len(f.errors) {
		err = f.errors[index]
	}
	return f.results[index], err
}

func TestProviderUsesPlainSubagentRenderWithOnlyExplicitContext(t *testing.T) {
	render := &fakeRunner{results: []runtime.ProcessResult{{
		Stdout: []byte("minted persona\n"),
		Stderr: []byte("render: voice        10 chars  ~     3 tokens  1 item(s)\nrender: context      20 chars  ~     5 tokens  2 item(s)\nrender: total        30 chars  ~     8 tokens\n"),
	}}}
	git := &fakeRunner{results: []runtime.ProcessResult{{Stdout: []byte("abc123\n")}}}
	provider := New(Config{Executable: "/bin/memory-render", MemoryDir: "/tmp/memory", Runner: render, GitRunner: git})
	factory := contextsource.NewFactory()
	if err := factory.Register(Source, provider); err != nil {
		t.Fatal(err)
	}
	resolutions, err := contextsource.ResolveAll(context.Background(), factory, contextsource.Request{}, []contextsource.Declaration{{
		Source: Source, Options: map[string]any{"body": "subagent", "context": []any{"reference_renderer", "project_smith", "project_smith"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"render", "--body", "subagent", "--format", "plain", "--context", "project_smith,reference_renderer", "/tmp/memory"}
	if !reflect.DeepEqual(render.requests[0].Args, wantArgs) {
		t.Fatalf("render args = %#v, want %#v", render.requests[0].Args, wantArgs)
	}
	artifact := resolutions[0].Artifacts[0]
	if artifact.Content != "minted persona\n" || artifact.Revision != "abc123" || artifact.Source != Source || artifact.SHA256 == "" {
		t.Fatalf("artifact = %#v", artifact)
	}
	for key, value := range map[string]string{"body": "subagent", "format": "plain", "context": "project_smith,reference_renderer", "voice_items": "1", "context_items": "2", "total_chars": "30"} {
		if artifact.Metadata[key] != value {
			t.Fatalf("metadata[%q] = %q, want %q; all = %#v", key, artifact.Metadata[key], value, artifact.Metadata)
		}
	}
}

func TestProviderChangedRenderChangesArtifactHash(t *testing.T) {
	render := &fakeRunner{results: []runtime.ProcessResult{{Stdout: []byte("before")}, {Stdout: []byte("after")}}}
	git := &fakeRunner{results: []runtime.ProcessResult{{Stdout: []byte("same-rev\n")}, {Stdout: []byte("same-rev\n")}}}
	factory := contextsource.NewFactory()
	_ = factory.Register(Source, New(Config{Executable: "/bin/memory-render", MemoryDir: "/tmp/memory", Runner: render, GitRunner: git}))
	declarations := []contextsource.Declaration{{Source: Source, Options: map[string]any{"body": "subagent"}}}
	first, err := contextsource.ResolveAll(context.Background(), factory, contextsource.Request{}, declarations)
	if err != nil {
		t.Fatal(err)
	}
	second, err := contextsource.ResolveAll(context.Background(), factory, contextsource.Request{}, declarations)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Artifacts[0].SHA256 == second[0].Artifacts[0].SHA256 {
		t.Fatal("changed minted prompt kept the same hash")
	}
}

func TestProviderLoadsOnlyExplicitRegularMemoryFiles(t *testing.T) {
	memoryDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(memoryDir, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(memoryDir, "memory", "project_smith.md")
	if err := os.WriteFile(selected, []byte("full selected memory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "memory", "project_unrelated.md"), []byte("must stay out"), 0o600); err != nil {
		t.Fatal(err)
	}
	render := &fakeRunner{results: []runtime.ProcessResult{{Stdout: []byte("minted identity")}}}
	git := &fakeRunner{results: []runtime.ProcessResult{{Stdout: []byte("revision\n")}}}
	factory := contextsource.NewFactory()
	_ = factory.Register(Source, New(Config{Executable: "/bin/memory-render", MemoryDir: memoryDir, Runner: render, GitRunner: git}))
	declaration := []contextsource.Declaration{{Source: Source, Options: map[string]any{"body": "subagent", "memories": []any{"project_smith"}}}}
	first, err := contextsource.ResolveAll(context.Background(), factory, contextsource.Request{}, declaration)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := first[0].Artifacts
	if len(artifacts) != 2 || artifacts[1].Name != "project_smith" || artifacts[1].URI != selected || artifacts[1].Content != "full selected memory" {
		t.Fatalf("artifacts = %#v", artifacts)
	}
	for _, artifact := range artifacts {
		if strings.Contains(artifact.Content, "must stay out") {
			t.Fatalf("unrelated memory leaked into %#v", artifact)
		}
	}
	firstHash := artifacts[1].SHA256
	if err := os.WriteFile(selected, []byte("changed selected memory"), 0o600); err != nil {
		t.Fatal(err)
	}
	render.results = append(render.results, runtime.ProcessResult{Stdout: []byte("minted identity")})
	git.results = append(git.results, runtime.ProcessResult{Stdout: []byte("revision\n")})
	second, err := contextsource.ResolveAll(context.Background(), factory, contextsource.Request{}, declaration)
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Artifacts[1].SHA256 == firstHash {
		t.Fatal("changed explicit memory kept the same artifact hash")
	}
}

func TestProviderRejectsMissingAndSymlinkedExplicitMemories(t *testing.T) {
	memoryDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(memoryDir, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "project_target.md")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(memoryDir, "memory", "project_link.md")); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"project_missing", "project_link"} {
		render := &fakeRunner{results: []runtime.ProcessResult{{Stdout: []byte("minted")}}}
		git := &fakeRunner{results: []runtime.ProcessResult{{Stdout: []byte("revision\n")}}}
		provider := New(Config{Executable: "/bin/memory-render", MemoryDir: memoryDir, Runner: render, GitRunner: git})
		_, err := provider.Resolve(context.Background(), contextsource.Request{Options: map[string]any{"body": "subagent", "memories": []any{slug}}})
		if err == nil || (!strings.Contains(err.Error(), "no such file") && !strings.Contains(err.Error(), "not a regular file")) {
			t.Fatalf("slug %q error = %v", slug, err)
		}
	}
}

func TestProviderRejectsAmbientOrUnrelatedContextSelection(t *testing.T) {
	provider := New(Config{MemoryDir: "/tmp/memory"})
	for _, selection := range []any{[]any{"all"}, []any{"feedback_guard_honesty"}, []any{"../secret"}} {
		_, err := provider.Resolve(context.Background(), contextsource.Request{Options: map[string]any{"body": "subagent", "context": selection}})
		if err == nil || !strings.Contains(err.Error(), "explicit project_ or reference_") {
			t.Fatalf("selection %#v error = %v", selection, err)
		}
	}
}

func TestProviderRejectsUnknownOptions(t *testing.T) {
	provider := New(Config{MemoryDir: "/tmp/memory"})
	_, err := provider.Resolve(context.Background(), contextsource.Request{Options: map[string]any{"body": "subagent", "surprise": true}})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v", err)
	}
}

func TestProviderRejectsBodyPathsBeforeInvokingRenderer(t *testing.T) {
	render := &fakeRunner{}
	provider := New(Config{MemoryDir: "/tmp/memory", Runner: render})
	for _, body := range []string{"", "../secret", "nested/body", "subagent.md"} {
		_, err := provider.Resolve(context.Background(), contextsource.Request{Options: map[string]any{"body": body}})
		if err == nil || !strings.Contains(err.Error(), "body name, not a path") {
			t.Fatalf("body %q error = %v", body, err)
		}
	}
	if len(render.requests) != 0 {
		t.Fatalf("invalid body invoked the renderer: %#v", render.requests)
	}
}
