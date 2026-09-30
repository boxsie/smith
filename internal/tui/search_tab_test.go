package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/boxsie/smith/internal/projects"
)

func setupSearchFixture(t *testing.T) (string, string) {
	t.Helper()

	// Create a temporary smith dir for projects.json.
	smithDir := t.TempDir()

	// Create a fixture project with some files.
	projDir := t.TempDir()

	// task.md with searchable content.
	os.WriteFile(filepath.Join(projDir, "task.md"), []byte("Search for Premier League results from March 26 2026."), 0o644)

	// Subtask directory.
	subDir := filepath.Join(projDir, "subtasks", "01-search")
	os.MkdirAll(subDir, 0o755)
	os.WriteFile(filepath.Join(subDir, "task.md"), []byte("Find all Premier League football news."), 0o644)

	// A binary file (should be skipped).
	os.WriteFile(filepath.Join(projDir, "binary.dat"), []byte{0x00, 0x01, 0x02, 0x03}, 0o644)

	// A .git directory (should be skipped).
	gitDir := filepath.Join(projDir, ".git")
	os.MkdirAll(gitDir, 0o755)
	os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("Premier League ref"), 0o644)

	// Track the project.
	projects.TrackIn(smithDir, projDir)

	return smithDir, projDir
}

func TestSearchProject(t *testing.T) {
	_, projDir := setupSearchFixture(t)

	matches := searchProject(projDir, "premier league")

	if len(matches) == 0 {
		t.Fatal("expected matches for 'premier league'")
	}

	// Should find content in task.md and subtask.
	foundRoot := false
	foundSub := false
	for _, m := range matches {
		if m.filePath == "task.md" {
			foundRoot = true
		}
		if strings.Contains(m.filePath, "01-search") {
			foundSub = true
		}
	}
	if !foundRoot {
		t.Error("should find match in root task.md")
	}
	if !foundSub {
		t.Error("should find match in subtask task.md")
	}
}

func TestSearchSkipsBinary(t *testing.T) {
	_, projDir := setupSearchFixture(t)

	// binary.dat contains null bytes and should be skipped.
	matches := searchProject(projDir, "premier")

	for _, m := range matches {
		if m.filePath == "binary.dat" {
			t.Error("binary file should be skipped")
		}
	}
}

func TestSearchSkipsGitDir(t *testing.T) {
	_, projDir := setupSearchFixture(t)

	matches := searchProject(projDir, "premier")

	for _, m := range matches {
		if strings.HasPrefix(m.filePath, ".git") {
			t.Error(".git directory should be skipped")
		}
	}
}

func TestSearchNoMatches(t *testing.T) {
	_, projDir := setupSearchFixture(t)

	matches := searchProject(projDir, "xyznonexistent")

	if len(matches) != 0 {
		t.Errorf("expected no matches, got %d", len(matches))
	}
}

func TestSearchEmptyProjectIndex(t *testing.T) {
	smithDir := t.TempDir()
	idx, _ := projects.LoadFrom(smithDir)

	if len(idx.Projects) != 0 {
		t.Skip("empty project index expected")
	}

	// Search should produce no results without error.
	tab := newSearchTab()
	result, _ := tab.Update(searchDoneMsg{})
	tab = result.(*searchTab)

	if len(tab.results) != 0 {
		t.Error("empty project index should return no results")
	}
}

func TestSearchFileNameMatch(t *testing.T) {
	projDir := t.TempDir()

	// Create a file whose name matches.
	os.WriteFile(filepath.Join(projDir, "premier-league-data.txt"), []byte("some data"), 0o644)

	matches := searchProject(projDir, "premier-league")

	foundName := false
	for _, m := range matches {
		if m.nameMatch && m.filePath == "premier-league-data.txt" {
			foundName = true
		}
	}
	if !foundName {
		t.Error("should match on file name")
	}
}

func TestSearchGroupsByFile(t *testing.T) {
	projDir := t.TempDir()

	// Create a file whose name AND content both match the query.
	// Should produce only ONE fileMatch, not two.
	os.WriteFile(filepath.Join(projDir, "premier-report.md"), []byte("Premier League results today."), 0o644)

	files := searchProject(projDir, "premier")

	count := 0
	for _, f := range files {
		if f.filePath == "premier-report.md" {
			count++
			if !f.nameMatch {
				t.Error("should have nameMatch set")
			}
			if f.snippet == "" {
				t.Error("should have content snippet too")
			}
		}
	}
	if count != 1 {
		t.Errorf("file should appear exactly once, got %d entries", count)
	}
}

func TestSearchCaseInsensitive(t *testing.T) {
	projDir := t.TempDir()
	os.WriteFile(filepath.Join(projDir, "test.md"), []byte("The PREMIER LEAGUE is great."), 0o644)

	matches := searchProject(projDir, "premier league")

	if len(matches) == 0 {
		t.Error("case-insensitive search should find 'PREMIER LEAGUE' with query 'premier league'")
	}
}

func TestSearchTabView(t *testing.T) {
	tab := newSearchTab()

	// Initial view should show help text.
	view := tab.View()
	if !strings.Contains(view, "Type a query") {
		t.Error("initial view should show help text")
	}

	// Empty query.
	if tab.searched {
		t.Error("should not be in searched state initially")
	}
}

func TestSearchTabEscClears(t *testing.T) {
	tab := newSearchTab()
	tab.searched = true
	tab.results = []searchResult{{projectPath: "/test"}}
	tab.total = 1

	result, _ := tab.Update(tea.KeyMsg{Type: tea.KeyEscape})
	tab = result.(*searchTab)

	if tab.searched {
		t.Error("Esc should clear searched state")
	}
	if len(tab.results) != 0 {
		t.Error("Esc should clear results")
	}
}

func TestSearchTabResultsView(t *testing.T) {
	tab := newSearchTab()
	tab.searched = true
	tab.results = []searchResult{
		{
			projectPath: "/home/user/project",
			files: []fileMatch{
				{filePath: "task.md", snippet: "Premier League results"},
			},
		},
	}
	tab.total = 1
	tab.height = 40

	view := tab.View()
	if !strings.Contains(view, "1 matches") {
		t.Error("view should show match count")
	}
	if !strings.Contains(view, "task.md") {
		t.Error("view should show matched file path")
	}
}

func TestSearchTabNoResults(t *testing.T) {
	tab := newSearchTab()
	tab.searched = true

	view := tab.View()
	if !strings.Contains(view, "No matches found") {
		t.Error("view should show 'No matches found' when searched with no results")
	}
}

func TestIsBinary(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		binary bool
	}{
		{"text", []byte("hello world"), false},
		{"null byte", []byte{0x48, 0x00, 0x49}, true},
		{"empty", []byte{}, false},
		{"utf8", []byte("héllo wörld"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBinary(tt.data); got != tt.binary {
				t.Errorf("isBinary(%q) = %v, want %v", tt.data, got, tt.binary)
			}
		})
	}
}

func TestBuildSnippet(t *testing.T) {
	line := "This is a long line with Premier League results in the middle of it for testing"

	snippet := buildSnippet(line, "premier league")
	if !strings.Contains(snippet, "Premier League") {
		t.Errorf("snippet should contain the match, got %q", snippet)
	}
}

func TestBuildSnippetIndentedLine(t *testing.T) {
	// Regression: indented lines must not panic.
	line := "    Premier League results from March 2026"
	snippet := buildSnippet(line, "premier league")
	if !strings.Contains(snippet, "Premier League") {
		t.Errorf("snippet from indented line should contain match, got %q", snippet)
	}
}

func TestBuildSnippetDeepIndent(t *testing.T) {
	line := "                    deeply indented Premier League match"
	snippet := buildSnippet(line, "premier league")
	if !strings.Contains(snippet, "Premier League") {
		t.Errorf("snippet from deeply indented line should contain match, got %q", snippet)
	}
}

func TestSearchTabEditing(t *testing.T) {
	tab := newSearchTab()
	if tab.Editing() {
		t.Error("search tab should start unfocused (Editing = false)")
	}

	// Typing a character auto-focuses.
	result, _ := tab.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	tab = result.(*searchTab)
	if !tab.Editing() {
		t.Error("search tab should be focused after typing a character")
	}

	// Esc blurs the input (when no results).
	result, _ = tab.Update(tea.KeyMsg{Type: tea.KeyEscape})
	tab = result.(*searchTab)
	if tab.Editing() {
		t.Error("search tab should be unfocused after Esc")
	}

	// Enter also focuses from unfocused state.
	result, _ = tab.Update(tea.KeyMsg{Type: tea.KeyEnter})
	tab = result.(*searchTab)
	if !tab.Editing() {
		t.Error("search tab should be focused after Enter")
	}
}

func TestFileCountLimit(t *testing.T) {
	projDir := t.TempDir()

	// Create more files than we'd normally scan in a realistic test.
	// Just verify the limit mechanism exists by checking the constant.
	if maxFilesPerProject != 10000 {
		t.Errorf("maxFilesPerProject = %d, want 10000", maxFilesPerProject)
	}

	// Create a small set and verify it works.
	for i := 0; i < 5; i++ {
		name := filepath.Join(projDir, "file"+string(rune('a'+i))+".txt")
		os.WriteFile(name, []byte("some content"), 0o644)
	}
	matches := searchProject(projDir, "some content")
	if len(matches) == 0 {
		t.Error("should find matches in small file set")
	}
}
