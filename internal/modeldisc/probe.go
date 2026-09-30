package modeldisc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/boxsie/smith/internal/config"
)

// DiscoveredModel represents a model available at a provider.
type DiscoveredModel struct {
	ID       string // "anthropic/claude-sonnet-4-6" or "ollama/llama3.1:latest"
	Provider string // "anthropic" or "ollama"
	SizeTier string // "small", "medium", "large", or "" if unknown
}

// ProviderStatus describes the state of a single provider.
type ProviderStatus struct {
	Configured bool   // true if credentials/endpoint are present
	Reachable  bool   // true if provider responded
	Error      string // non-empty on probe failure
	Models     []DiscoveredModel
}

// ProbeResult aggregates status across all providers.
type ProbeResult struct {
	Anthropic ProviderStatus
	Ollama    ProviderStatus
}

// AllModels returns the combined model list from all providers.
func (r *ProbeResult) AllModels() []DiscoveredModel {
	out := make([]DiscoveredModel, 0, len(r.Anthropic.Models)+len(r.Ollama.Models))
	out = append(out, r.Anthropic.Models...)
	out = append(out, r.Ollama.Models...)
	return out
}

// knownAnthropicModels is a curated catalog of known Anthropic model IDs.
var knownAnthropicModels = []string{
	"anthropic/claude-sonnet-4-6",
	"anthropic/claude-haiku-4-5",
	"anthropic/claude-opus-4-6",
}

// Probe tests provider connectivity and discovers available models.
func Probe(cfg config.Config) *ProbeResult {
	r := &ProbeResult{}
	r.Anthropic = probeAnthropic(cfg)
	r.Ollama = probeOllama(cfg)
	return r
}

func probeAnthropic(cfg config.Config) ProviderStatus {
	key := os.Getenv("SMITH_ANTHROPIC_API_KEY")
	if key == "" {
		key = cfg.AnthropicAPIKey
	}
	if key == "" {
		return ProviderStatus{
			Configured: false,
			Error:      "no API key configured",
		}
	}
	models := make([]DiscoveredModel, len(knownAnthropicModels))
	for i, id := range knownAnthropicModels {
		models[i] = DiscoveredModel{ID: id, Provider: "anthropic", SizeTier: InferSizeTier(id)}
	}
	return ProviderStatus{
		Configured: true,
		Reachable:  true,
		Models:     models,
	}
}

// InferSizeTier returns "small", "medium", "large", or "" based on the model ID.
// Pure string parsing — no network calls.
func InferSizeTier(modelID string) string {
	switch modelID {
	case "anthropic/claude-haiku-4-5":
		return "small"
	case "anthropic/claude-sonnet-4-6":
		return "medium"
	case "anthropic/claude-opus-4-6":
		return "large"
	}
	if strings.HasPrefix(modelID, "anthropic/") {
		name := modelID[len("anthropic/"):]
		if strings.Contains(name, "haiku") {
			return "small"
		}
		if strings.Contains(name, "opus") {
			return "large"
		}
		return "medium"
	}
	if strings.HasPrefix(modelID, "ollama/") {
		name := modelID[len("ollama/"):]
		return inferOllamaSizeTier(name)
	}
	return ""
}

func inferOllamaSizeTier(name string) string {
	parts := strings.Split(name, ":")
	if len(parts) < 2 {
		return ""
	}
	tag := strings.ToLower(parts[len(parts)-1])
	if !strings.HasSuffix(tag, "b") {
		return ""
	}
	numStr := strings.TrimSuffix(tag, "b")
	params, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return ""
	}
	switch {
	case params <= 8:
		return "small"
	case params < 32:
		return "medium"
	default:
		return "large"
	}
}

// ollamaTagsResponse matches the JSON schema of Ollama's GET /api/tags.
type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

func probeOllama(cfg config.Config) ProviderStatus {
	endpoint := os.Getenv("SMITH_OLLAMA_ENDPOINT")
	if endpoint == "" {
		endpoint = cfg.OllamaEndpoint
	}
	if endpoint == "" {
		return ProviderStatus{Configured: false}
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(endpoint + "/api/tags")
	if err != nil {
		return ProviderStatus{
			Configured: true,
			Reachable:  false,
			Error:      fmt.Sprintf("probe failed: %v", err),
		}
	}
	defer resp.Body.Close()

	var tags ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return ProviderStatus{
			Configured: true,
			Reachable:  false,
			Error:      fmt.Sprintf("parse /api/tags: %v", err),
		}
	}

	models := make([]DiscoveredModel, len(tags.Models))
	for i, m := range tags.Models {
		id := "ollama/" + m.Name
		models[i] = DiscoveredModel{
			ID:       id,
			Provider: "ollama",
			SizeTier: InferSizeTier(id),
		}
	}
	return ProviderStatus{
		Configured: true,
		Reachable:  true,
		Models:     models,
	}
}
