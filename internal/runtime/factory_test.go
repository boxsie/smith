package runtime

import (
	"strings"
	"testing"
)

func TestFactory_AnthropicResolution(t *testing.T) {
	t.Setenv("SMITH_ANTHROPIC_API_KEY", "sk-test")
	f := DefaultFactory()
	p, err := f.Resolve("anthropic/claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ap, ok := p.(*AnthropicProvider)
	if !ok {
		t.Fatalf("expected *AnthropicProvider, got %T", p)
	}
	if ap.Model != "claude-sonnet-4-6" {
		t.Errorf("model: got %q", ap.Model)
	}
}

func TestFactory_MockResolution(t *testing.T) {
	f := DefaultFactory()
	p, err := f.Resolve("mock/static")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mp, ok := p.(*MockProvider)
	if !ok {
		t.Fatalf("expected *MockProvider, got %T", p)
	}
	if !strings.Contains(mp.Default, "static") {
		t.Errorf("default response should reference model name, got %q", mp.Default)
	}
}

func TestFactory_OllamaResolution(t *testing.T) {
	t.Setenv("SMITH_OLLAMA_ENDPOINT", "http://localhost:11434")
	f := DefaultFactory()
	p, err := f.Resolve("ollama/llama3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	op, ok := p.(*OllamaProvider)
	if !ok {
		t.Fatalf("expected *OllamaProvider, got %T", p)
	}
	if op.Model != "llama3" {
		t.Errorf("model: got %q", op.Model)
	}
}

func TestFactory_OllamaWithTag(t *testing.T) {
	t.Setenv("SMITH_OLLAMA_ENDPOINT", "http://localhost:11434")
	f := DefaultFactory()
	p, err := f.Resolve("ollama/codellama:7b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	op, ok := p.(*OllamaProvider)
	if !ok {
		t.Fatalf("expected *OllamaProvider, got %T", p)
	}
	if op.Model != "codellama:7b" {
		t.Errorf("model: got %q, want codellama:7b", op.Model)
	}
}

func TestFactory_OllamaEmptyName(t *testing.T) {
	f := DefaultFactory()
	_, err := f.Resolve("ollama/")
	if err == nil {
		t.Fatal("expected error for empty ollama model name")
	}
}

func TestFactory_UnknownPrefix(t *testing.T) {
	f := DefaultFactory()
	_, err := f.Resolve("unknown/foo")
	if err == nil {
		t.Fatal("expected error for unknown prefix")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("error should mention unsupported, got: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error should name the prefix, got: %v", err)
	}
}

func TestFactory_NoSlash(t *testing.T) {
	f := DefaultFactory()
	_, err := f.Resolve("invalidformat")
	if err == nil {
		t.Fatal("expected error for no-slash format")
	}
	if !strings.Contains(err.Error(), "prefix/name") {
		t.Errorf("error should mention expected format, got: %v", err)
	}
}

func TestFactory_Override(t *testing.T) {
	mock := &MockProvider{Default: "overridden"}
	f := &Factory{
		Override: func(model string) (Provider, error) {
			return mock, nil
		},
	}

	p, err := f.Resolve("anthropic/claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != mock {
		t.Error("override should be used instead of normal dispatch")
	}
}
