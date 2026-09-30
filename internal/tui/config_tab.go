package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/boxsie/smith/internal/config"
	"github.com/boxsie/smith/internal/modeldisc"
)

// configField identifies an editable field in the Config tab.
type configField int

const (
	fieldAnthropicKey configField = iota
	fieldOllamaEndpoint
	fieldSearXNGEndpoint
	fieldPlannerModel
	fieldTaskModel
	fieldCount // sentinel
)

// configTab is the Bubble Tea sub-model for the Config tab.
type configTab struct {
	cfg     config.Config
	loadErr error // non-nil if config.Load() failed (malformed JSON, permissions, etc.)
	cursor  configField

	// Editing state
	editing  bool
	input    textinput.Model
	editErr  string // validation error shown inline

	// Model picker state (used instead of text input when probe results exist)
	picking    bool
	pickItems  []string // model IDs to choose from
	pickCursor int

	// Probing state
	probing     bool
	probeSpnr   spinner.Model
	probeResult *modeldisc.ProbeResult
	probeErr    string

	// Save feedback
	saveMsg string
	saveErr string // save-specific error (disk write failure, permissions, etc.)

	width  int
	height int
}

// Messages used by async commands.
type probeResultMsg struct{ result *modeldisc.ProbeResult }
type probeErrMsg struct{ err error }
type saveErrMsg struct{ err error }
type saveDoneMsg struct{}
type clearFlashMsg struct{}

func newConfigTab(cfg config.Config, loadErr error) *configTab {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return &configTab{
		cfg:       cfg,
		loadErr:   loadErr,
		probeSpnr: s,
	}
}

func (t *configTab) Init() tea.Cmd { return nil }

func (t *configTab) Editing() bool { return t.editing || t.picking }

func (t *configTab) SetSize(w, h int) {
	t.width = w
	t.height = h
}

func (t *configTab) Update(msg tea.Msg) (tabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case probeResultMsg:
		t.probing = false
		t.probeResult = msg.result
		t.probeErr = ""
		return t, nil

	case probeErrMsg:
		t.probing = false
		t.probeErr = msg.err.Error()
		return t, nil

	case saveErrMsg:
		t.saveErr = msg.err.Error()
		return t, nil

	case saveDoneMsg:
		t.loadErr = nil // config is now valid on disk
		t.saveMsg = "Saved!"
		t.saveErr = ""
		return t, clearFlashAfter()

	case clearFlashMsg:
		t.saveMsg = ""
		t.saveErr = ""
		t.editErr = ""
		return t, nil

	case spinner.TickMsg:
		if t.probing {
			var cmd tea.Cmd
			t.probeSpnr, cmd = t.probeSpnr.Update(msg)
			return t, cmd
		}
		return t, nil

	case tea.KeyMsg:
		if t.picking {
			return t.updatePicking(msg)
		}
		if t.editing {
			return t.updateEditing(msg)
		}
		return t.updateIdle(msg)
	}

	return t, nil
}

func (t *configTab) updateIdle(msg tea.KeyMsg) (tabModel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if t.cursor > 0 {
			t.cursor--
		}
	case "down", "j":
		if t.cursor < fieldCount-1 {
			t.cursor++
		}
	case "enter":
		t.startEditing()
	case "p":
		return t, t.startProbe()
	case "s":
		return t, t.save()
	}
	return t, nil
}

func (t *configTab) updateEditing(msg tea.KeyMsg) (tabModel, tea.Cmd) {
	switch msg.String() {
	case "enter":
		t.commitEdit()
		return t, nil
	case "esc":
		t.editing = false
		t.editErr = ""
		return t, nil
	}

	var cmd tea.Cmd
	t.input, cmd = t.input.Update(msg)
	return t, cmd
}

func (t *configTab) startEditing() {
	// For model fields, use the picker if probe results are available.
	if (t.cursor == fieldPlannerModel || t.cursor == fieldTaskModel) && t.probeResult != nil {
		models := t.probeResult.AllModels()
		if len(models) > 0 {
			t.pickItems = make([]string, len(models))
			for i, m := range models {
				t.pickItems[i] = m.ID
			}
			t.pickCursor = 0
			// Pre-select the current value if it's in the list.
			current := t.cfg.DefaultPlannerModel
			if t.cursor == fieldTaskModel {
				current = t.cfg.DefaultTaskModel
			}
			for i, id := range t.pickItems {
				if id == current {
					t.pickCursor = i
					break
				}
			}
			t.picking = true
			t.editErr = ""
			return
		}
	}

	ti := textinput.New()
	ti.CharLimit = 256
	ti.Width = 50

	switch t.cursor {
	case fieldAnthropicKey:
		ti.Placeholder = "sk-ant-..."
		ti.SetValue(t.cfg.AnthropicAPIKey)
		ti.EchoMode = textinput.EchoPassword
	case fieldOllamaEndpoint:
		ti.Placeholder = "http://localhost:11434"
		ti.SetValue(t.cfg.OllamaEndpoint)
	case fieldSearXNGEndpoint:
		ti.Placeholder = "https://searxng.example.com"
		ti.SetValue(t.cfg.SearXNGEndpoint)
	case fieldPlannerModel:
		ti.Placeholder = "anthropic/claude-sonnet-4-6"
		ti.SetValue(t.cfg.DefaultPlannerModel)
	case fieldTaskModel:
		ti.Placeholder = "anthropic/claude-sonnet-4-6"
		ti.SetValue(t.cfg.DefaultTaskModel)
	}

	ti.Focus()
	t.input = ti
	t.editing = true
	t.editErr = ""
}

func (t *configTab) updatePicking(msg tea.KeyMsg) (tabModel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if t.pickCursor > 0 {
			t.pickCursor--
		}
	case "down", "j":
		if t.pickCursor < len(t.pickItems)-1 {
			t.pickCursor++
		}
	case "enter":
		selected := t.pickItems[t.pickCursor]
		switch t.cursor {
		case fieldPlannerModel:
			t.cfg.DefaultPlannerModel = selected
		case fieldTaskModel:
			t.cfg.DefaultTaskModel = selected
		}
		t.picking = false
		t.pickItems = nil
	case "esc":
		t.picking = false
		t.pickItems = nil
	}
	return t, nil
}

func (t *configTab) commitEdit() {
	val := strings.TrimSpace(t.input.Value())

	switch t.cursor {
	case fieldAnthropicKey:
		t.cfg.AnthropicAPIKey = val
		t.invalidateProbe()
	case fieldOllamaEndpoint:
		t.cfg.OllamaEndpoint = val
		t.invalidateProbe()
	case fieldSearXNGEndpoint:
		t.cfg.SearXNGEndpoint = val
	case fieldPlannerModel:
		if err := config.ValidateModelID(val); err != nil {
			t.editErr = err.Error()
			return
		}
		t.cfg.DefaultPlannerModel = val
	case fieldTaskModel:
		if err := config.ValidateModelID(val); err != nil {
			t.editErr = err.Error()
			return
		}
		t.cfg.DefaultTaskModel = val
	}

	t.editing = false
	t.editErr = ""
}

// invalidateProbe clears stale probe results after a provider field changes.
func (t *configTab) invalidateProbe() {
	t.probeResult = nil
	t.probeErr = ""
}

func (t *configTab) startProbe() tea.Cmd {
	t.probing = true
	t.probeErr = ""
	cfg := t.cfg // snapshot current values
	return tea.Batch(
		t.probeSpnr.Tick,
		func() tea.Msg {
			result := modeldisc.Probe(cfg)
			return probeResultMsg{result: result}
		},
	)
}

func (t *configTab) save() tea.Cmd {
	// Block save if the original config failed to load — we'd overwrite it with garbage
	if t.loadErr != nil {
		t.saveErr = "cannot save: config file is corrupt (" + t.loadErr.Error() + "). Fix it manually or delete it first."
		return nil
	}

	// Validate model fields before saving
	if err := config.ValidateModelID(t.cfg.DefaultPlannerModel); err != nil {
		t.editErr = "Planner model: " + err.Error()
		return nil
	}
	if err := config.ValidateModelID(t.cfg.DefaultTaskModel); err != nil {
		t.editErr = "Task model: " + err.Error()
		return nil
	}

	cfg := t.cfg
	return func() tea.Msg {
		if err := config.Save(cfg); err != nil {
			return saveErrMsg{err: err}
		}
		return saveDoneMsg{}
	}
}

func clearFlashAfter() tea.Cmd {
	return tea.Tick(2*time.Second, func(_ time.Time) tea.Msg {
		return clearFlashMsg{}
	})
}

// View renders the Config tab.
func (t *configTab) View() string {
	var b strings.Builder

	b.WriteString(sectionHeader.Render("Configuration"))
	b.WriteString("\n")

	// --- Providers ---
	b.WriteString(sectionHeader.Render("Providers"))
	b.WriteString("\n")

	fields := []struct {
		label string
		value string
		mask  bool
		field configField
	}{
		{"Anthropic API Key", t.cfg.AnthropicAPIKey, true, fieldAnthropicKey},
		{"Ollama Endpoint", t.cfg.OllamaEndpoint, false, fieldOllamaEndpoint},
		{"SearXNG Endpoint", t.cfg.SearXNGEndpoint, false, fieldSearXNGEndpoint},
	}

	for _, f := range fields {
		cursor := "  "
		if t.cursor == f.field {
			cursor = "> "
		}

		label := fieldLabel.Render(f.label + ":")

		var val string
		if t.editing && t.cursor == f.field {
			val = t.input.View()
		} else if f.mask && f.value != "" {
			val = fieldValue.Render(config.MaskKey(f.value)) + "  " + statusOK.Render("✓ Configured")
		} else if !f.mask && f.value != "" {
			val = fieldValue.Render(f.value)
			if f.field == fieldOllamaEndpoint && t.probeResult != nil && t.probeResult.Ollama.Reachable {
				n := len(t.probeResult.Ollama.Models)
				val += "  " + statusOK.Render(fmt.Sprintf("✓ Running (%d models)", n))
			}
		} else if f.field == fieldOllamaEndpoint && f.value == "" {
			val = muted.Render("http://localhost:11434") + "  " + muted.Render("(not configured)")
		} else {
			val = muted.Render("(not set)")
		}

		b.WriteString(cursor + label + val + "\n")
	}

	// --- Discovered Models ---
	b.WriteString("\n")
	if t.probing {
		b.WriteString(t.probeSpnr.View() + " Probing providers...\n")
	} else if t.probeResult != nil {
		b.WriteString(sectionHeader.Render("Discovered Models (live)"))
		b.WriteString("\n")
		models := t.probeResult.AllModels()
		if len(models) == 0 {
			b.WriteString(muted.Render("  No models discovered") + "\n")
		}
		for _, m := range models {
			loc := "cloud"
			if m.Provider == "ollama" {
				loc = "local"
			}
			b.WriteString(fmt.Sprintf("  %-42s %s  %s\n",
				m.ID,
				statusOK.Render("✓"),
				muted.Render(loc),
			))
		}
	}
	if t.probeErr != "" {
		b.WriteString(statusErr.Render("  Probe error: "+t.probeErr) + "\n")
	}

	// --- Defaults ---
	b.WriteString("\n")
	b.WriteString(sectionHeader.Render("Defaults"))
	b.WriteString("\n")

	defaults := []struct {
		label string
		value string
		field configField
	}{
		{"Default planner model", t.cfg.DefaultPlannerModel, fieldPlannerModel},
		{"Default task model", t.cfg.DefaultTaskModel, fieldTaskModel},
	}

	for _, f := range defaults {
		cursor := "  "
		if t.cursor == f.field {
			cursor = "> "
		}

		label := fieldLabel.Render(f.label + ":")

		var val string
		if t.picking && t.cursor == f.field {
			val = muted.Render("(select below)")
		} else if t.editing && t.cursor == f.field {
			val = t.input.View()
		} else if f.value != "" {
			val = fieldValue.Render(f.value)
		} else {
			val = muted.Render("(default)")
		}

		b.WriteString(cursor + label + val + "\n")

		// Render picker inline below the active field.
		if t.picking && t.cursor == f.field {
			for i, id := range t.pickItems {
				pick := "    "
				if i == t.pickCursor {
					pick = "  > "
				}
				b.WriteString(pick + fieldValue.Render(id) + "\n")
			}
		}
	}

	// --- Errors / flash ---
	if t.loadErr != nil {
		b.WriteString("\n" + statusErr.Render("  Config load error: "+t.loadErr.Error()) + "\n")
		b.WriteString(statusErr.Render("  Saving is disabled until the config file is fixed or deleted.") + "\n")
	}
	if t.editErr != "" {
		b.WriteString("\n" + statusErr.Render("  Error: "+t.editErr) + "\n")
	}
	if t.saveErr != "" {
		b.WriteString("\n" + statusErr.Render("  Save error: "+t.saveErr) + "\n")
	}
	if t.saveMsg != "" {
		b.WriteString("\n" + statusOK.Render("  "+t.saveMsg) + "\n")
	}

	// --- Help bar ---
	b.WriteString("\n")
	if t.picking {
		b.WriteString(helpBar.Render("  [Enter] Select  [Esc] Cancel  [↑/↓] Navigate"))
	} else {
		b.WriteString(helpBar.Render("  [Enter] Edit  [p] Probe providers  [s] Save  [Esc] Cancel edit"))
	}

	return b.String()
}
