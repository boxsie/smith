package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/boxsie/smith/internal/config"
	"github.com/boxsie/smith/internal/modeldisc"
)

func fixtureConfig() config.Config {
	return config.Config{
		AnthropicAPIKey:     "sk-ant-test-key-123456789",
		OllamaEndpoint:      "http://localhost:11434",
		DefaultPlannerModel: "anthropic/claude-sonnet-4-6",
		DefaultTaskModel:    "anthropic/claude-sonnet-4-6",
	}
}

func TestConfigTabRendersValues(t *testing.T) {
	ct := newConfigTab(fixtureConfig(), nil)
	view := ct.View()

	// API key should be masked
	if strings.Contains(view, "sk-ant-test-key-123456789") {
		t.Error("view should not contain raw API key")
	}
	if !strings.Contains(view, "Configured") {
		t.Error("view should show Configured status for set API key")
	}

	// Ollama endpoint should be visible
	if !strings.Contains(view, "http://localhost:11434") {
		t.Error("view should contain Ollama endpoint")
	}

	// Model defaults should be visible
	if !strings.Contains(view, "anthropic/claude-sonnet-4-6") {
		t.Error("view should contain default model strings")
	}
}

func TestConfigTabEditModeToggle(t *testing.T) {
	ct := newConfigTab(fixtureConfig(), nil)

	if ct.Editing() {
		t.Fatal("should not be editing initially")
	}

	// Enter activates editing
	ct.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}) // move to Ollama
	result, _ := ct.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ct = result.(*configTab)

	if !ct.Editing() {
		t.Fatal("should be editing after Enter")
	}

	// Esc cancels editing
	result, _ = ct.Update(tea.KeyMsg{Type: tea.KeyEscape})
	ct = result.(*configTab)

	if ct.Editing() {
		t.Fatal("should not be editing after Esc")
	}
}

func TestConfigTabProbeCompletion(t *testing.T) {
	ct := newConfigTab(fixtureConfig(), nil)

	// Simulate probe result arriving
	result, _ := ct.Update(probeResultMsg{
		result: &modeldisc.ProbeResult{},
	})
	ct = result.(*configTab)

	if ct.probing {
		t.Fatal("should not be probing after result")
	}
	if ct.probeResult == nil {
		t.Fatal("probeResult should be set")
	}
}

func TestConfigTabSaveInvalidModel(t *testing.T) {
	cfg := fixtureConfig()
	cfg.DefaultPlannerModel = "invalid-no-slash"
	ct := newConfigTab(cfg, nil)

	cmd := ct.save()
	if cmd != nil {
		t.Fatal("save with invalid model should return nil cmd (blocked)")
	}
	if ct.editErr == "" {
		t.Fatal("save with invalid model should set editErr")
	}
}

func TestConfigTabSaveEmptyModels(t *testing.T) {
	cfg := fixtureConfig()
	cfg.DefaultPlannerModel = ""
	cfg.DefaultTaskModel = ""
	ct := newConfigTab(cfg, nil)

	cmd := ct.save()
	if cmd == nil {
		t.Fatal("save with empty models should succeed (empty = use default)")
	}
	if ct.editErr != "" {
		t.Fatalf("unexpected editErr: %s", ct.editErr)
	}
}

func TestConfigTabCommitEditValidation(t *testing.T) {
	ct := newConfigTab(fixtureConfig(), nil)

	// Navigate to planner model field
	ct.cursor = fieldPlannerModel
	ct.startEditing()

	// Type an invalid model ID
	ct.input.SetValue("bad-model")

	ct.commitEdit()

	if !ct.editing {
		t.Fatal("should still be editing after invalid commit")
	}
	if ct.editErr == "" {
		t.Fatal("should have editErr after invalid model commit")
	}
}

func TestConfigTabCommitEditValid(t *testing.T) {
	ct := newConfigTab(fixtureConfig(), nil)

	ct.cursor = fieldPlannerModel
	ct.startEditing()
	ct.input.SetValue("ollama/llama3:latest")
	ct.commitEdit()

	if ct.editing {
		t.Fatal("should not be editing after valid commit")
	}
	if ct.editErr != "" {
		t.Fatalf("unexpected editErr: %s", ct.editErr)
	}
	if ct.cfg.DefaultPlannerModel != "ollama/llama3:latest" {
		t.Fatalf("planner model = %q, want ollama/llama3:latest", ct.cfg.DefaultPlannerModel)
	}
}

func TestConfigTabLoadErrorBlocksSave(t *testing.T) {
	loadErr := fmt.Errorf("parse config: unexpected end of JSON input")
	ct := newConfigTab(config.Config{}, loadErr)

	cmd := ct.save()
	if cmd != nil {
		t.Fatal("save should be blocked when loadErr is set")
	}
	if ct.saveErr == "" {
		t.Fatal("saveErr should be set when save is blocked by loadErr")
	}
	if !strings.Contains(ct.saveErr, "corrupt") {
		t.Errorf("saveErr = %q, want it to mention 'corrupt'", ct.saveErr)
	}
}

func TestConfigTabLoadErrorShownInView(t *testing.T) {
	loadErr := fmt.Errorf("parse config: bad json")
	ct := newConfigTab(config.Config{}, loadErr)

	view := ct.View()
	if !strings.Contains(view, "Config load error") {
		t.Error("view should show config load error")
	}
	if !strings.Contains(view, "bad json") {
		t.Error("view should contain the original error message")
	}
}

func TestConfigTabSaveErrorShownSeparately(t *testing.T) {
	ct := newConfigTab(fixtureConfig(), nil)

	// Simulate a save error arriving
	result, _ := ct.Update(saveErrMsg{err: fmt.Errorf("permission denied")})
	ct = result.(*configTab)

	view := ct.View()
	if !strings.Contains(view, "Save error") {
		t.Error("view should show 'Save error', not 'Probe error'")
	}
	if strings.Contains(view, "Probe error") {
		t.Error("save failure should not be reported as probe error")
	}
}

func TestConfigTabProviderEditClearsProbe(t *testing.T) {
	ct := newConfigTab(fixtureConfig(), nil)

	// Simulate a probe result being present
	ct.probeResult = &modeldisc.ProbeResult{
		Anthropic: modeldisc.ProviderStatus{
			Configured: true,
			Reachable:  true,
			Models:     []modeldisc.DiscoveredModel{{ID: "anthropic/claude-sonnet-4-6", Provider: "anthropic"}},
		},
	}
	ct.probeErr = ""

	// Edit the Anthropic key
	ct.cursor = fieldAnthropicKey
	ct.startEditing()
	ct.input.SetValue("sk-ant-new-key")
	ct.commitEdit()

	if ct.probeResult != nil {
		t.Fatal("probeResult should be cleared after editing a provider field")
	}
}

func TestConfigTabOllamaEditClearsProbe(t *testing.T) {
	ct := newConfigTab(fixtureConfig(), nil)

	ct.probeResult = &modeldisc.ProbeResult{}
	ct.probeErr = "old error"

	// Edit the Ollama endpoint
	ct.cursor = fieldOllamaEndpoint
	ct.startEditing()
	ct.input.SetValue("http://other:11434")
	ct.commitEdit()

	if ct.probeResult != nil {
		t.Fatal("probeResult should be cleared after editing Ollama endpoint")
	}
	if ct.probeErr != "" {
		t.Fatal("probeErr should be cleared after editing Ollama endpoint")
	}
}

func TestConfigTabModelEditDoesNotClearProbe(t *testing.T) {
	ct := newConfigTab(fixtureConfig(), nil)

	ct.probeResult = &modeldisc.ProbeResult{}

	// Edit a model default (not a provider field)
	ct.cursor = fieldPlannerModel
	ct.startEditing()
	ct.input.SetValue("ollama/llama3:latest")
	ct.commitEdit()

	if ct.probeResult == nil {
		t.Fatal("probeResult should NOT be cleared when editing model defaults")
	}
}

func TestConfigTabSaveSuccessClearsSaveErr(t *testing.T) {
	ct := newConfigTab(fixtureConfig(), nil)

	// Simulate a previous save error
	ct.saveErr = "permission denied"

	// Simulate a successful save
	result, _ := ct.Update(saveDoneMsg{})
	ct = result.(*configTab)

	if ct.saveErr != "" {
		t.Fatalf("saveErr should be cleared on save success, got %q", ct.saveErr)
	}
	if ct.saveMsg != "Saved!" {
		t.Fatalf("saveMsg should be 'Saved!', got %q", ct.saveMsg)
	}
}

func TestConfigTabOllamaPlaceholderWhenEmpty(t *testing.T) {
	cfg := fixtureConfig()
	cfg.OllamaEndpoint = ""
	ct := newConfigTab(cfg, nil)

	view := ct.View()
	if !strings.Contains(view, "http://localhost:11434") {
		t.Error("empty Ollama endpoint should show localhost placeholder in read-only view")
	}
	if !strings.Contains(view, "not configured") {
		t.Error("empty Ollama endpoint should show '(not configured)' hint")
	}
}
