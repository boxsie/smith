package modeldisc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/boxsie/smith/internal/config"
)

func TestProbeAnthropic_KeyPresent(t *testing.T) {
	t.Setenv("SMITH_ANTHROPIC_API_KEY", "")
	cfg := config.Config{AnthropicAPIKey: "sk-test"}
	r := Probe(cfg)
	if !r.Anthropic.Configured {
		t.Fatal("expected Configured=true")
	}
	if !r.Anthropic.Reachable {
		t.Fatal("expected Reachable=true")
	}
	if r.Anthropic.Error != "" {
		t.Fatalf("unexpected error: %s", r.Anthropic.Error)
	}
	if len(r.Anthropic.Models) == 0 {
		t.Fatal("expected at least one model")
	}
	for _, m := range r.Anthropic.Models {
		if m.Provider != "anthropic" {
			t.Errorf("model %q has provider %q, want anthropic", m.ID, m.Provider)
		}
	}
}

func TestProbeAnthropic_KeyFromEnv(t *testing.T) {
	t.Setenv("SMITH_ANTHROPIC_API_KEY", "sk-env-key")
	cfg := config.Config{} // no key in config
	r := Probe(cfg)
	if !r.Anthropic.Configured {
		t.Fatal("expected Configured=true from env var")
	}
}

func TestProbeAnthropic_NoKey(t *testing.T) {
	t.Setenv("SMITH_ANTHROPIC_API_KEY", "")
	cfg := config.Config{}
	r := Probe(cfg)
	if r.Anthropic.Configured {
		t.Fatal("expected Configured=false")
	}
	if r.Anthropic.Error == "" {
		t.Fatal("expected an error message")
	}
}

func TestProbeOllama_NotConfigured(t *testing.T) {
	t.Setenv("SMITH_OLLAMA_ENDPOINT", "")
	cfg := config.Config{}
	r := Probe(cfg)
	if r.Ollama.Configured {
		t.Fatal("expected Configured=false when no endpoint")
	}
	if r.Ollama.Error != "" {
		t.Fatalf("expected no error, got: %s", r.Ollama.Error)
	}
}

func TestProbeOllama_Running(t *testing.T) {
	t.Setenv("SMITH_OLLAMA_ENDPOINT", "")
	t.Setenv("SMITH_ANTHROPIC_API_KEY", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		resp := map[string]any{
			"models": []map[string]any{
				{"name": "llama3.1:latest"},
				{"name": "codellama:7b"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := config.Config{OllamaEndpoint: srv.URL}
	r := Probe(cfg)
	if !r.Ollama.Configured {
		t.Fatal("expected Configured=true")
	}
	if !r.Ollama.Reachable {
		t.Fatal("expected Reachable=true")
	}
	if len(r.Ollama.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(r.Ollama.Models))
	}
	if r.Ollama.Models[0].ID != "ollama/llama3.1:latest" {
		t.Errorf("model[0].ID = %q, want ollama/llama3.1:latest", r.Ollama.Models[0].ID)
	}
	if r.Ollama.Models[1].ID != "ollama/codellama:7b" {
		t.Errorf("model[1].ID = %q, want ollama/codellama:7b", r.Ollama.Models[1].ID)
	}
	for _, m := range r.Ollama.Models {
		if m.Provider != "ollama" {
			t.Errorf("model %q has provider %q, want ollama", m.ID, m.Provider)
		}
	}
}

func TestProbeOllama_Empty(t *testing.T) {
	t.Setenv("SMITH_OLLAMA_ENDPOINT", "")
	t.Setenv("SMITH_ANTHROPIC_API_KEY", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"models": []any{}})
	}))
	defer srv.Close()

	cfg := config.Config{OllamaEndpoint: srv.URL}
	r := Probe(cfg)
	if !r.Ollama.Reachable {
		t.Fatal("expected Reachable=true for empty models list")
	}
	if len(r.Ollama.Models) != 0 {
		t.Fatalf("expected 0 models, got %d", len(r.Ollama.Models))
	}
}

func TestProbeOllama_Unreachable(t *testing.T) {
	t.Setenv("SMITH_OLLAMA_ENDPOINT", "")
	t.Setenv("SMITH_ANTHROPIC_API_KEY", "")

	cfg := config.Config{OllamaEndpoint: "http://127.0.0.1:1"}
	r := Probe(cfg)
	if !r.Ollama.Configured {
		t.Fatal("expected Configured=true")
	}
	if r.Ollama.Reachable {
		t.Fatal("expected Reachable=false")
	}
	if r.Ollama.Error == "" {
		t.Fatal("expected an error")
	}
}

func TestProbeOllama_EndpointFromEnv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"models": []any{}})
	}))
	defer srv.Close()

	t.Setenv("SMITH_OLLAMA_ENDPOINT", srv.URL)
	t.Setenv("SMITH_ANTHROPIC_API_KEY", "")

	cfg := config.Config{} // no endpoint in config
	r := Probe(cfg)
	if !r.Ollama.Configured {
		t.Fatal("expected Configured=true from env var")
	}
	if !r.Ollama.Reachable {
		t.Fatalf("expected Reachable=true, error: %s", r.Ollama.Error)
	}
}

func TestInferSizeTier(t *testing.T) {
	tests := []struct {
		modelID string
		want    string
	}{
		// Anthropic known catalog
		{"anthropic/claude-haiku-4-5", "small"},
		{"anthropic/claude-sonnet-4-6", "medium"},
		{"anthropic/claude-opus-4-6", "large"},
		// Anthropic future models (prefix match)
		{"anthropic/claude-haiku-4-5-20251001", "small"},
		{"anthropic/claude-opus-5-0", "large"},
		// Ollama with explicit size tags
		{"ollama/qwen2.5:7b", "small"},
		{"ollama/qwen2.5:8b", "small"},
		{"ollama/qwen2.5:14b", "medium"},
		{"ollama/gemma4:26b", "medium"},
		{"ollama/qwen2.5:32b", "large"},
		{"ollama/qwen2.5:72b", "large"},
		{"ollama/llama3.1:405b", "large"},
		// Ollama with decimal tags
		{"ollama/phi3:3.8b", "small"},
		// Ollama without size info
		{"ollama/llama3.1:latest", ""},
		{"ollama/llama3", ""},
		// Unknown provider
		{"unknown/model", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := InferSizeTier(tt.modelID)
		if got != tt.want {
			t.Errorf("InferSizeTier(%q) = %q, want %q", tt.modelID, got, tt.want)
		}
	}
}

func TestAllModels(t *testing.T) {
	t.Setenv("SMITH_ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("SMITH_OLLAMA_ENDPOINT", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"models": []map[string]any{
				{"name": "llama3:latest"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := config.Config{
		AnthropicAPIKey: "sk-test",
		OllamaEndpoint:  srv.URL,
	}
	r := Probe(cfg)
	all := r.AllModels()
	if len(all) != len(r.Anthropic.Models)+len(r.Ollama.Models) {
		t.Errorf("AllModels() returned %d, expected %d", len(all), len(r.Anthropic.Models)+len(r.Ollama.Models))
	}
}
