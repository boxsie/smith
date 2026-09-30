package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds user-level Smith configuration.
type Config struct {
	AnthropicAPIKey     string `json:"anthropic_api_key"`
	OllamaEndpoint      string `json:"ollama_endpoint,omitempty"`
	DefaultPlannerModel string `json:"default_planner_model,omitempty"`
	DefaultTaskModel    string `json:"default_task_model,omitempty"`
	SearXNGEndpoint     string `json:"searxng_endpoint,omitempty"`
}

// Dir returns the Smith config directory (~/.smith).
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".smith"), nil
}

// Path returns the full path to config.json.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads config from the default directory.
func Load() (Config, error) {
	dir, err := Dir()
	if err != nil {
		return Config{}, err
	}
	return LoadFrom(dir)
}

// LoadFrom reads config.json from the given directory.
// Returns zero Config and nil error if the file does not exist.
// Returns an error on malformed JSON or permission problems.
func LoadFrom(dir string) (Config, error) {
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// Save writes config to the default directory.
func Save(cfg Config) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	return SaveTo(dir, cfg)
}

// SaveTo writes config.json to the given directory.
// Creates the directory (0700) if it doesn't exist.
// Writes the file with 0600 permissions.
func SaveTo(dir string, cfg Config) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		return fmt.Errorf("chmod config file: %w", err)
	}
	return nil
}

// MaskKey returns a masked version of an API key for display.
// Shows the last 3 characters, or fully masks short keys.
func MaskKey(key string) string {
	if len(key) <= 6 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-3:]
}

// knownProviders lists the recognised model provider prefixes.
var knownProviders = map[string]bool{
	"anthropic": true,
	"ollama":    true,
}

// ValidateModelID checks that a model string has the form "provider/name"
// with a known provider prefix (anthropic, ollama). Returns nil for empty
// strings (empty means "use default"). Returns an error for malformed or
// unrecognised providers.
func ValidateModelID(s string) error {
	if s == "" {
		return nil
	}
	slash := strings.IndexByte(s, '/')
	if slash < 0 {
		return fmt.Errorf("invalid model ID %q: expected provider/name format", s)
	}
	provider := s[:slash]
	name := s[slash+1:]
	if provider == "" {
		return fmt.Errorf("invalid model ID %q: empty provider", s)
	}
	if name == "" {
		return fmt.Errorf("invalid model ID %q: empty model name", s)
	}
	if !knownProviders[provider] {
		return fmt.Errorf("invalid model ID %q: unsupported provider %q (known: anthropic, ollama)", s, provider)
	}
	return nil
}
