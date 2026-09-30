package tui

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/boxsie/smith/internal/projects"
)

const (
	maxFilesPerProject = 10000
	maxFileSize        = 1 << 20 // 1MB
	snippetRadius      = 40      // chars before/after match
)

// fileMatch holds all match info for a single file.
type fileMatch struct {
	filePath    string
	nameMatch   bool   // true if filename matched
	snippet     string // content match snippet (empty if no content match)
}

// searchResult groups file matches by project path.
type searchResult struct {
	projectPath string
	files       []fileMatch
}

// searchTab is the Bubble Tea sub-model for the Search tab.
type searchTab struct {
	input     textinput.Model
	focused   bool // whether the text input has focus (captures keys)
	results   []searchResult
	total     int // total match count
	searched  bool
	searching bool
	loadErr   string
	cursor    int // scroll position in results view
	width     int
	height    int
}

type searchDoneMsg struct {
	results []searchResult
	total   int
	err     error
}

func newSearchTab() *searchTab {
	ti := textinput.New()
	ti.Placeholder = "Search across projects..."
	ti.CharLimit = 256
	ti.Width = 60

	return &searchTab{input: ti}
}

func (t *searchTab) Init() tea.Cmd { return nil }

func (t *searchTab) Editing() bool { return t.focused }

func (t *searchTab) SetSize(w, h int) {
	t.width = w
	t.height = h
}

func (t *searchTab) Update(msg tea.Msg) (tabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case searchDoneMsg:
		t.searching = false
		if msg.err != nil {
			t.loadErr = msg.err.Error()
		} else {
			t.results = msg.results
			t.total = msg.total
			t.loadErr = ""
		}
		return t, nil

	case tabActivatedMsg:
		// Don't auto-focus — let the user press Enter or start typing to focus.
		// This allows Tab/Shift+Tab to switch tabs normally.
		return t, nil

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keys.Enter):
			if !t.focused {
				// Focus the input on Enter.
				t.focused = true
				t.input.Focus()
				return t, nil
			}
			query := strings.TrimSpace(t.input.Value())
			if query == "" {
				return t, nil
			}
			t.searching = true
			t.searched = true
			t.cursor = 0
			return t, t.doSearch(query)

		case key.Matches(msg, keys.Escape):
			if t.searched {
				// Clear results and return to input.
				t.results = nil
				t.total = 0
				t.searched = false
				t.loadErr = ""
				t.cursor = 0
				t.input.SetValue("")
				t.input.Focus()
				t.focused = true
			} else {
				// Blur the input so Tab/q work for global navigation.
				t.focused = false
				t.input.Blur()
			}
			return t, nil

		case key.Matches(msg, key.NewBinding(key.WithKeys("up"))):
			if t.cursor > 0 {
				t.cursor--
			}
			return t, nil

		case key.Matches(msg, key.NewBinding(key.WithKeys("down"))):
			t.cursor++
			return t, nil
		}

		// Auto-focus on any printable character key when not focused.
		if !t.focused && msg.Type == tea.KeyRunes {
			t.focused = true
			t.input.Focus()
		}

		// Forward all other keys to the text input.
		var cmd tea.Cmd
		t.input, cmd = t.input.Update(msg)
		return t, cmd
	}

	return t, nil
}

func (t *searchTab) doSearch(query string) tea.Cmd {
	return func() tea.Msg {
		idx, err := projects.Load()
		if err != nil {
			return searchDoneMsg{err: err}
		}

		if len(idx.Projects) == 0 {
			return searchDoneMsg{}
		}

		queryLower := strings.ToLower(query)
		var results []searchResult
		totalMatches := 0

		for _, projPath := range idx.Projects {
			files := searchProject(projPath, queryLower)
			if len(files) > 0 {
				results = append(results, searchResult{
					projectPath: projPath,
					files:       files,
				})
				totalMatches += len(files)
			}
		}

		return searchDoneMsg{results: results, total: totalMatches}
	}
}

// searchProject walks a project directory searching for matches.
// Results are grouped by file — each file appears at most once.
func searchProject(projPath, queryLower string) []fileMatch {
	var files []fileMatch
	fileCount := 0

	filepath.WalkDir(projPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}

		// Skip .git directories.
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}

		if d.IsDir() {
			return nil
		}

		fileCount++
		if fileCount > maxFilesPerProject {
			return filepath.SkipAll
		}

		// Get relative path for display.
		relPath, _ := filepath.Rel(projPath, path)
		if relPath == "" {
			relPath = path
		}

		fm := fileMatch{filePath: relPath}
		matched := false

		// Check filename match.
		if strings.Contains(strings.ToLower(d.Name()), queryLower) {
			fm.nameMatch = true
			matched = true
		}

		// Skip large files for content search.
		info, err := d.Info()
		if err != nil || info.Size() > maxFileSize {
			if matched {
				files = append(files, fm)
			}
			return nil
		}

		// Read and check for binary content.
		data, err := os.ReadFile(path)
		if err != nil {
			if matched {
				files = append(files, fm)
			}
			return nil
		}

		if isBinary(data) {
			if matched {
				files = append(files, fm)
			}
			return nil
		}

		// Search content line by line.
		contentLines := strings.Split(string(data), "\n")
		for _, cl := range contentLines {
			ci := strings.Index(strings.ToLower(cl), queryLower)
			if ci >= 0 {
				fm.snippet = buildSnippet(cl, queryLower)
				matched = true
				break // one snippet per file
			}
		}

		if matched {
			files = append(files, fm)
		}
		return nil
	})

	return files
}

// isBinary checks if data looks like binary content (null bytes in first 512 bytes).
func isBinary(data []byte) bool {
	checkLen := 512
	if len(data) < checkLen {
		checkLen = len(data)
	}
	for i := 0; i < checkLen; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// buildSnippet creates a context snippet around a match.
// query is the lowercase search term used to locate the match in the line.
func buildSnippet(line, query string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return ""
	}

	// Find match position in the trimmed text.
	matchIdx := strings.Index(strings.ToLower(trimmed), query)
	if matchIdx < 0 {
		if len(trimmed) > 80 {
			return trimmed[:80] + "..."
		}
		return trimmed
	}

	matchLen := len(query)
	start := matchIdx - snippetRadius
	end := matchIdx + matchLen + snippetRadius

	prefix := ""
	suffix := ""

	if start < 0 {
		start = 0
	} else {
		prefix = "..."
	}

	if end > len(trimmed) {
		end = len(trimmed)
	} else {
		suffix = "..."
	}

	return prefix + trimmed[start:end] + suffix
}

func (t *searchTab) View() string {
	var b strings.Builder

	b.WriteString(sectionHeader.Render("Search"))
	b.WriteString("\n")

	b.WriteString("  " + t.input.View())
	b.WriteString("\n\n")

	if t.searching {
		b.WriteString(muted.Render("  Searching...") + "\n")
		return b.String()
	}

	if t.loadErr != "" {
		b.WriteString(statusErr.Render("  Error: "+t.loadErr) + "\n")
		return b.String()
	}

	if !t.searched {
		b.WriteString(muted.Render("  Type a query and press Enter to search across all known projects.") + "\n")
		b.WriteString("\n")
		b.WriteString(helpBar.Render("  [Enter] Search  [Esc] Clear"))
		return b.String()
	}

	if len(t.results) == 0 {
		b.WriteString(muted.Render("  No matches found.") + "\n")
		b.WriteString("\n")
		b.WriteString(helpBar.Render("  [Enter] Search  [Esc] Clear"))
		return b.String()
	}

	// Summary line.
	projectWord := "projects"
	if len(t.results) == 1 {
		projectWord = "project"
	}
	b.WriteString(fmt.Sprintf("  Results (%d matches in %d %s)\n", t.total, len(t.results), projectWord))
	b.WriteString(muted.Render("  "+strings.Repeat("─", 40)) + "\n")

	// Build flat list of renderable lines for scrolling.
	var lines []string
	for _, r := range t.results {
		displayPath := shortenPath(r.projectPath)
		lines = append(lines, sectionHeader.Render("  "+displayPath))
		for _, f := range r.files {
			lines = append(lines, "    "+f.filePath)
			if f.snippet != "" {
				lines = append(lines, "      "+muted.Render("\""+f.snippet+"\""))
			}
		}
		lines = append(lines, "")
	}

	// Apply scroll offset.
	viewHeight := t.height - 12 // account for header, input, summary, help
	if viewHeight < 5 {
		viewHeight = 5
	}
	if t.cursor >= len(lines) {
		t.cursor = len(lines) - 1
	}

	start := t.cursor
	end := start + viewHeight
	if end > len(lines) {
		end = len(lines)
	}

	for _, line := range lines[start:end] {
		b.WriteString(line + "\n")
	}

	b.WriteString("\n")
	b.WriteString(helpBar.Render("  [Enter] Search  [↑/↓] Scroll  [Esc] Clear"))

	return b.String()
}
