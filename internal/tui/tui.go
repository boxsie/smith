package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/boxsie/smith/internal/config"
)

// tabModel is the interface that each tab's sub-model must implement.
type tabModel interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (tabModel, tea.Cmd)
	View() string
	SetSize(width, height int)
	Editing() bool
}

// tabActivatedMsg is sent to a tab when it becomes the active tab.
// Tabs that need to refresh on activation should handle this.
type tabActivatedMsg struct{}

// tabNames is the ordered list of tab labels.
var tabNames = []string{"Config", "Projects", "Packages", "Search"}

type model struct {
	tabs      []tabModel
	activeTab int
	width     int
	height    int
}

func initialModel() model {
	cfg, loadErr := config.Load()
	return model{
		tabs: []tabModel{
			newConfigTab(cfg, loadErr),
			newProjectsTab(),
			newPackagesTab(),
			newSearchTab(),
		},
	}
}

func (m model) Init() tea.Cmd {
	var cmds []tea.Cmd
	for _, t := range m.tabs {
		if c := t.Init(); c != nil {
			cmds = append(cmds, c)
		}
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		contentHeight := m.height - 4 // tab bar + border + margins
		for i := range m.tabs {
			m.tabs[i].SetSize(m.width, contentHeight)
		}
		return m, nil

	case tea.KeyMsg:
		// ctrl+c always quits
		if key.Matches(msg, keys.ForceQuit) {
			return m, tea.Quit
		}

		active := m.tabs[m.activeTab]

		// When a tab is editing, forward all keys to it
		if active.Editing() {
			updated, cmd := active.Update(msg)
			m.tabs[m.activeTab] = updated
			return m, cmd
		}

		// Global key bindings
		switch {
		case key.Matches(msg, keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, keys.NextTab):
			m.activeTab = (m.activeTab + 1) % len(m.tabs)
			updated, cmd := m.tabs[m.activeTab].Update(tabActivatedMsg{})
			m.tabs[m.activeTab] = updated
			return m, cmd
		case key.Matches(msg, keys.PrevTab):
			m.activeTab = (m.activeTab - 1 + len(m.tabs)) % len(m.tabs)
			updated, cmd := m.tabs[m.activeTab].Update(tabActivatedMsg{})
			m.tabs[m.activeTab] = updated
			return m, cmd
		}
	}

	// Forward all other messages to the active tab
	updated, cmd := m.tabs[m.activeTab].Update(msg)
	m.tabs[m.activeTab] = updated
	return m, cmd
}

func (m model) View() string {
	var b strings.Builder

	// Tab content first — gets the bulk of the vertical space.
	b.WriteString(contentPane.Render(m.tabs[m.activeTab].View()))
	b.WriteString("\n")

	// Bottom bar: tab indicators + navigation hint.
	var tabs []string
	for i, name := range tabNames {
		if i == m.activeTab {
			tabs = append(tabs, tabActive.Render(name))
		} else {
			tabs = append(tabs, tabInactive.Render(name))
		}
	}
	pos := muted.Render(fmt.Sprintf(" %d/%d", m.activeTab+1, len(m.tabs)))
	hint := muted.Render("  Tab/Shift+Tab: switch  q: quit")
	bar := lipgloss.JoinHorizontal(lipgloss.Bottom, tabs...)
	b.WriteString(bottomBar.Render(bar + pos + hint))

	return b.String()
}

// Run launches the TUI. Called from CLI when TTY is detected.
func Run() error {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// --- Placeholder tab for unimplemented tabs ---

type placeholderTab struct {
	name   string
	width  int
	height int
}

func (t placeholderTab) Init() tea.Cmd                         { return nil }
func (t placeholderTab) Update(tea.Msg) (tabModel, tea.Cmd)    { return t, nil }
func (t placeholderTab) SetSize(w, h int)                      { t.width = w; t.height = h }
func (t placeholderTab) Editing() bool                         { return false }

func (t placeholderTab) View() string {
	return muted.Render(t.name + " -- coming soon")
}
