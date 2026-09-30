package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/boxsie/smith/internal/projects"
	"github.com/boxsie/smith/internal/run"
)

// projectEntry holds display data for a single project row.
type projectEntry struct {
	path       string
	lastRun    string // formatted timestamp or "(no runs)"
	status     string // "success", "failed", "(no runs)", "(missing)", "(error reading status)"
	runCount   int
	exists     bool
	statusKind int // 0=ok, 1=warn, 2=error
}

// projectsTab is the Bubble Tea sub-model for the Projects tab.
type projectsTab struct {
	entries  []projectEntry
	cursor   int
	loadErr  string
	width    int
	height   int
	detail   *projectDetailModel // non-nil when viewing run history
}

// Messages for async operations.
type projectsLoadedMsg struct {
	entries []projectEntry
	err     error
}
type projectRemovedMsg struct{ err error }

func newProjectsTab() *projectsTab {
	return &projectsTab{}
}

func (t *projectsTab) Init() tea.Cmd {
	return t.loadProjects()
}

func (t *projectsTab) Editing() bool { return false }

func (t *projectsTab) SetSize(w, h int) {
	t.width = w
	t.height = h
	if t.detail != nil {
		t.detail.SetSize(w, h)
	}
}

func (t *projectsTab) Update(msg tea.Msg) (tabModel, tea.Cmd) {
	// Handle detailBackMsg before detail delegation, otherwise
	// it gets forwarded into the detail model and never reaches us.
	if _, ok := msg.(detailBackMsg); ok {
		t.detail = nil
		return t, t.loadProjects()
	}

	// If detail view is active, delegate to it.
	if t.detail != nil {
		return t.updateDetail(msg)
	}

	switch msg := msg.(type) {
	case projectsLoadedMsg:
		if msg.err != nil {
			t.loadErr = msg.err.Error()
		} else {
			t.entries = msg.entries
			t.loadErr = ""
			if t.cursor >= len(t.entries) {
				t.cursor = max(0, len(t.entries)-1)
			}
		}
		return t, nil

	case projectRemovedMsg:
		if msg.err != nil {
			t.loadErr = msg.err.Error()
		}
		return t, t.loadProjects()

	case tabActivatedMsg:
		return t, t.loadProjects()

	case tea.KeyMsg:
		return t.updateList(msg)
	}

	return t, nil
}

func (t *projectsTab) updateList(msg tea.KeyMsg) (tabModel, tea.Cmd) {
	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("up", "k"))):
		if t.cursor > 0 {
			t.cursor--
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("down", "j"))):
		if t.cursor < len(t.entries)-1 {
			t.cursor++
		}
	case key.Matches(msg, keys.Enter):
		if len(t.entries) > 0 {
			e := t.entries[t.cursor]
			d := newProjectDetailModel(e.path)
			d.SetSize(t.width, t.height)
			t.detail = d
			return t, d.Init()
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("d"))):
		if len(t.entries) > 0 {
			path := t.entries[t.cursor].path
			return t, func() tea.Msg {
				err := projects.Remove(path)
				return projectRemovedMsg{err: err}
			}
		}
	}
	return t, nil
}

func (t *projectsTab) updateDetail(msg tea.Msg) (tabModel, tea.Cmd) {
	updated, cmd := t.detail.Update(msg)
	t.detail = updated
	return t, cmd
}

func (t *projectsTab) loadProjects() tea.Cmd {
	return func() tea.Msg {
		idx, err := projects.Load()
		if err != nil {
			return projectsLoadedMsg{err: err}
		}

		entries := make([]projectEntry, len(idx.Projects))
		for i, p := range idx.Projects {
			entries[i] = resolveProjectEntry(p)
		}
		return projectsLoadedMsg{entries: entries}
	}
}

// resolveProjectEntry builds display data for a project path.
func resolveProjectEntry(path string) projectEntry {
	e := projectEntry{path: path, exists: true}

	// Check if path exists on disk.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		e.exists = false
		e.lastRun = ""
		e.status = "(missing)"
		e.statusKind = 2
		return e
	}

	// Get run list.
	manifests, err := run.ListRuns(path)
	if err != nil {
		e.lastRun = ""
		e.status = "(error reading status)"
		e.statusKind = 2
		return e
	}
	if len(manifests) == 0 {
		e.lastRun = ""
		e.status = "(no runs)"
		e.statusKind = 1
		e.runCount = 0
		return e
	}

	e.runCount = len(manifests)
	latest := manifests[0] // newest first
	e.status = latest.Status

	// Parse timestamp.
	if t, err := time.Parse(time.RFC3339, latest.CompletedAt); err == nil {
		e.lastRun = t.Format("2006-01-02")
	} else if t, err := time.Parse(time.RFC3339, latest.StartedAt); err == nil {
		e.lastRun = t.Format("2006-01-02")
	} else {
		e.lastRun = "?"
	}

	return e
}

func (t *projectsTab) View() string {
	// If detail view is active, render it.
	if t.detail != nil {
		return t.detail.View()
	}

	var b strings.Builder

	b.WriteString(sectionHeader.Render("Known Projects"))
	b.WriteString("\n")

	if t.loadErr != "" {
		b.WriteString(statusErr.Render("  Error: "+t.loadErr) + "\n")
		return b.String()
	}

	if len(t.entries) == 0 {
		b.WriteString(muted.Render("  No projects tracked yet. Run `smith run` or `smith plan` to add projects.") + "\n")
		b.WriteString("\n")
		b.WriteString(helpBar.Render("  Projects are automatically added when you run or plan."))
		return b.String()
	}

	for i, e := range t.entries {
		cursor := "  "
		if i == t.cursor {
			cursor = "> "
		}

		// Shorten path: use ~ for home dir.
		displayPath := shortenPath(e.path)

		var statusStyle func(string) string
		switch e.statusKind {
		case 2:
			statusStyle = func(s string) string { return statusErr.Render(s) }
		case 1:
			statusStyle = func(s string) string { return muted.Render(s) }
		default:
			if e.status == "success" {
				statusStyle = func(s string) string { return statusOK.Render(s) }
			} else if e.status == "failed" {
				statusStyle = func(s string) string { return statusErr.Render(s) }
			} else {
				statusStyle = func(s string) string { return muted.Render(s) }
			}
		}

		var line string
		if e.status == "(no runs)" || e.status == "(missing)" || e.status == "(error reading status)" {
			line = fmt.Sprintf("%-40s %s", displayPath, statusStyle(e.status))
		} else {
			runLabel := "runs"
			if e.runCount == 1 {
				runLabel = "run"
			}
			line = fmt.Sprintf("%-40s last run: %s  status: %-8s %d %s",
				displayPath, e.lastRun, statusStyle(e.status), e.runCount, runLabel)
		}

		b.WriteString(cursor + line + "\n")
	}

	b.WriteString("\n")
	b.WriteString(helpBar.Render("  [Enter] Browse runs  [d] Remove  [↑/↓] Navigate"))

	return b.String()
}

// shortenPath replaces home dir prefix with ~.
func shortenPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
		return "~/" + rel
	}
	return p
}
