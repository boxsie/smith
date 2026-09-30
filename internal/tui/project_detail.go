package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/boxsie/smith/internal/run"
)

// detailBackMsg signals the Projects tab to return to the project list.
type detailBackMsg struct{}

// runEntry holds display data for a single run row.
type runEntry struct {
	runID    string
	status   string
	duration string
	cost     string
	tasks    int
}

// projectDetailModel shows run history for a single project.
type projectDetailModel struct {
	projectPath string
	runs        []runEntry
	cursor      int
	loadErr     string
	width       int
	height      int
}

// runsLoadedMsg carries the result of loading runs.
type runsLoadedMsg struct {
	runs []runEntry
	err  error
}

func newProjectDetailModel(projectPath string) *projectDetailModel {
	return &projectDetailModel{projectPath: projectPath}
}

func (m *projectDetailModel) Init() tea.Cmd {
	return m.loadRuns()
}

func (m *projectDetailModel) Editing() bool { return false }

func (m *projectDetailModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

func (m *projectDetailModel) Update(msg tea.Msg) (*projectDetailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case runsLoadedMsg:
		if msg.err != nil {
			m.loadErr = msg.err.Error()
		} else {
			m.runs = msg.runs
			m.loadErr = ""
		}
		return m, nil

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keys.Escape):
			return m, func() tea.Msg { return detailBackMsg{} }
		case key.Matches(msg, key.NewBinding(key.WithKeys("up", "k"))):
			if m.cursor > 0 {
				m.cursor--
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("down", "j"))):
			if m.cursor < len(m.runs)-1 {
				m.cursor++
			}
		}
	}

	return m, nil
}

func (m *projectDetailModel) loadRuns() tea.Cmd {
	path := m.projectPath
	return func() tea.Msg {
		manifests, err := run.ListRuns(path)
		if err != nil {
			return runsLoadedMsg{err: err}
		}

		entries := make([]runEntry, len(manifests))
		for i, mf := range manifests {
			entries[i] = manifestToRunEntry(mf)
		}
		return runsLoadedMsg{runs: entries}
	}
}

// manifestToRunEntry converts a run.Manifest into display data.
func manifestToRunEntry(m run.Manifest) runEntry {
	e := runEntry{
		runID:  m.RunID,
		status: m.Status,
		tasks:  len(m.Tasks),
	}

	// Calculate duration.
	if m.StartedAt != "" && m.CompletedAt != "" {
		start, err1 := time.Parse(time.RFC3339, m.StartedAt)
		end, err2 := time.Parse(time.RFC3339, m.CompletedAt)
		if err1 == nil && err2 == nil {
			dur := end.Sub(start)
			if dur < time.Minute {
				e.duration = fmt.Sprintf("%.1fs", dur.Seconds())
			} else {
				e.duration = fmt.Sprintf("%.1fm", dur.Minutes())
			}
		}
	}
	if e.duration == "" {
		e.duration = "-"
	}

	// Calculate cost.
	var totalCost float64
	for _, t := range m.Tasks {
		totalCost += t.CostUSD
	}
	if totalCost > 0 {
		e.cost = fmt.Sprintf("$%.2f", totalCost)
	} else {
		e.cost = "-"
	}

	return e
}

func (m *projectDetailModel) View() string {
	var b strings.Builder

	displayPath := shortenPath(m.projectPath)
	b.WriteString(sectionHeader.Render(displayPath+" — Run History"))
	b.WriteString("\n")

	if m.loadErr != "" {
		b.WriteString(statusErr.Render("  Error: "+m.loadErr) + "\n")
		b.WriteString("\n")
		b.WriteString(helpBar.Render("  [Esc] Back to projects"))
		return b.String()
	}

	if len(m.runs) == 0 {
		b.WriteString(muted.Render("  No runs found.") + "\n")
		b.WriteString("\n")
		b.WriteString(helpBar.Render("  [Esc] Back to projects"))
		return b.String()
	}

	// Header row.
	header := fmt.Sprintf("  %-38s %-9s %8s %8s %7s", "Run ID", "Status", "Duration", "Cost", "Tasks")
	b.WriteString(muted.Render(header) + "\n")

	for i, r := range m.runs {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}

		var styledStatus string
		switch r.status {
		case "success":
			styledStatus = statusOK.Render(r.status)
		case "failed":
			styledStatus = statusErr.Render(r.status)
		case "running":
			styledStatus = fmt.Sprintf("%-9s", r.status)
		default:
			styledStatus = muted.Render(r.status)
		}

		taskLabel := "tasks"
		if r.tasks == 1 {
			taskLabel = "task"
		}

		line := fmt.Sprintf("%-38s %-9s %8s %8s %d %s",
			r.runID, styledStatus, r.duration, r.cost, r.tasks, taskLabel)

		b.WriteString(cursor + line + "\n")
	}

	b.WriteString("\n")
	b.WriteString(helpBar.Render("  [Esc] Back to projects  [↑/↓] Navigate"))

	return b.String()
}
