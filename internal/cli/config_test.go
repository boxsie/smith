package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/config"
)

// pipeInput creates an *os.File with the given content available to read.
func pipeInput(t *testing.T, content string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(content); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return r
}

func TestConfig_FreshSetup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	stdin := pipeInput(t, "sk-ant-test-key-fresh\n")
	defer stdin.Close()

	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()

	if err := doConfig(stdin, stdout, -1); err != nil {
		t.Fatalf("doConfig: %v", err)
	}

	cfg, err := config.LoadFrom(filepath.Join(dir, ".smith"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnthropicAPIKey != "sk-ant-test-key-fresh" {
		t.Errorf("got %q, want %q", cfg.AnthropicAPIKey, "sk-ant-test-key-fresh")
	}
}

func TestConfig_ReplaceExisting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	smithDir := filepath.Join(dir, ".smith")
	if err := config.SaveTo(smithDir, config.Config{AnthropicAPIKey: "old-key"}); err != nil {
		t.Fatal(err)
	}

	// "y" to replace, then new key
	stdin := pipeInput(t, "y\nsk-ant-new-key\n")
	defer stdin.Close()

	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()

	if err := doConfig(stdin, stdout, -1); err != nil {
		t.Fatalf("doConfig: %v", err)
	}

	cfg, err := config.LoadFrom(smithDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnthropicAPIKey != "sk-ant-new-key" {
		t.Errorf("got %q, want %q", cfg.AnthropicAPIKey, "sk-ant-new-key")
	}
}

func TestConfig_DeclineReplace(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	smithDir := filepath.Join(dir, ".smith")
	if err := config.SaveTo(smithDir, config.Config{AnthropicAPIKey: "keep-this"}); err != nil {
		t.Fatal(err)
	}

	stdin := pipeInput(t, "n\n")
	defer stdin.Close()

	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()

	if err := doConfig(stdin, stdout, -1); err != nil {
		t.Fatalf("doConfig: %v", err)
	}

	cfg, err := config.LoadFrom(smithDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnthropicAPIKey != "keep-this" {
		t.Errorf("key should not have changed, got %q", cfg.AnthropicAPIKey)
	}
}

func TestConfig_EmptyInput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	stdin := pipeInput(t, "   \n")
	defer stdin.Close()

	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()

	err = doConfig(stdin, stdout, -1)
	if err == nil {
		t.Fatal("expected error for empty input")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention empty, got: %v", err)
	}
}

func TestConfig_ShowsMaskedKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	smithDir := filepath.Join(dir, ".smith")
	if err := config.SaveTo(smithDir, config.Config{AnthropicAPIKey: "sk-ant-api03-longkey123"}); err != nil {
		t.Fatal(err)
	}

	stdin := pipeInput(t, "n\n")
	defer stdin.Close()

	// Capture stdout to a file we can read back
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := doConfig(stdin, w, -1); err != nil {
		t.Fatalf("doConfig: %v", err)
	}
	w.Close()

	buf := make([]byte, 1024)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if !strings.Contains(output, "sk-a...123") {
		t.Errorf("output should contain masked key, got: %s", output)
	}
}
