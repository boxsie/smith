package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{AnthropicAPIKey: "sk-ant-test-key-123"}

	if err := SaveTo(dir, cfg); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	loaded, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if loaded.AnthropicAPIKey != cfg.AnthropicAPIKey {
		t.Errorf("got %q, want %q", loaded.AnthropicAPIKey, cfg.AnthropicAPIKey)
	}
}

func TestLoadMissing(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("expected nil error for missing config, got: %v", err)
	}
	if cfg.AnthropicAPIKey != "" {
		t.Errorf("expected empty key, got %q", cfg.AnthropicAPIKey)
	}
}

func TestLoadMalformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFrom(dir)
	if err == nil {
		t.Fatal("expected error for malformed config")
	}
}

func TestLoadBadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission test not applicable on Windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"anthropic_api_key":"sk-test"}`), 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFrom(dir)
	if err == nil {
		t.Fatal("expected error for unreadable config")
	}
}

func TestPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission test not applicable on Windows")
	}

	dir := t.TempDir()
	smithDir := filepath.Join(dir, "smithcfg")
	cfg := Config{AnthropicAPIKey: "sk-test"}

	if err := SaveTo(smithDir, cfg); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	dirInfo, err := os.Stat(smithDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("directory permissions %o allow group/other access", perm)
	}

	fileInfo, err := os.Stat(filepath.Join(smithDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileInfo.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("file permissions %o allow group/other access", perm)
	}
}

func TestNewFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		AnthropicAPIKey:     "sk-ant-test",
		DefaultPlannerModel: "anthropic/claude-sonnet-4-6",
		DefaultTaskModel:    "ollama/llama3.1:latest",
	}
	if err := SaveTo(dir, cfg); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	loaded, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if loaded.DefaultPlannerModel != cfg.DefaultPlannerModel {
		t.Errorf("DefaultPlannerModel: got %q, want %q", loaded.DefaultPlannerModel, cfg.DefaultPlannerModel)
	}
	if loaded.DefaultTaskModel != cfg.DefaultTaskModel {
		t.Errorf("DefaultTaskModel: got %q, want %q", loaded.DefaultTaskModel, cfg.DefaultTaskModel)
	}
}

func TestLoadLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte(`{"anthropic_api_key":"sk-old"}`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.AnthropicAPIKey != "sk-old" {
		t.Errorf("AnthropicAPIKey: got %q, want %q", cfg.AnthropicAPIKey, "sk-old")
	}
	if cfg.DefaultPlannerModel != "" {
		t.Errorf("DefaultPlannerModel should be empty, got %q", cfg.DefaultPlannerModel)
	}
	if cfg.DefaultTaskModel != "" {
		t.Errorf("DefaultTaskModel should be empty, got %q", cfg.DefaultTaskModel)
	}
}

func TestValidateModelID(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
		errMsg  string // substring to check in error message
	}{
		{"", false, ""},
		{"anthropic/claude-sonnet-4-6", false, ""},
		{"ollama/llama3.1:latest", false, ""},
		{"claude-sonnet-4-6", true, "provider/name"},
		{"anthropic/", true, "empty model name"},
		{"/name", true, "empty provider"},
		{"unknown/model", true, "unsupported provider"},
	}
	for _, tt := range tests {
		err := ValidateModelID(tt.input)
		if tt.wantErr && err == nil {
			t.Errorf("ValidateModelID(%q): expected error, got nil", tt.input)
		} else if !tt.wantErr && err != nil {
			t.Errorf("ValidateModelID(%q): unexpected error: %v", tt.input, err)
		} else if tt.wantErr && err != nil && tt.errMsg != "" {
			if got := err.Error(); !contains(got, tt.errMsg) {
				t.Errorf("ValidateModelID(%q): error %q should contain %q", tt.input, got, tt.errMsg)
			}
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestMaskKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "***"},
		{"abc", "***"},
		{"abcdef", "***"},
		{"sk-ant-api03-abc123xyz", "sk-a...xyz"},
		{"1234567", "1234...567"},
	}
	for _, tt := range tests {
		got := MaskKey(tt.input)
		if got != tt.want {
			t.Errorf("MaskKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
