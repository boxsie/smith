package tui

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/boxsie/smith/internal/lib"
	"github.com/boxsie/smith/internal/tools"
)

// provenance describes where a module or tool came from.
type provenance int

const (
	provenanceEmbedded provenance = iota
	provenanceUserModified
	provenanceUserAdded
)

func (p provenance) String() string {
	switch p {
	case provenanceEmbedded:
		return "embedded"
	case provenanceUserModified:
		return "user-modified"
	case provenanceUserAdded:
		return "user-added"
	default:
		return "unknown"
	}
}

// moduleEntry holds display data for a lib module.
type moduleEntry struct {
	name        string
	provenance  provenance
	description string
	err         string // non-empty if the module is corrupt
}

// toolEntry holds display data for a lib tool.
type toolEntry struct {
	id          string
	toolType    string // shell, task, native
	provenance  provenance
	description string
	err         string
}

// packagesViewMode tracks what the Packages tab is showing.
type packagesViewMode int

const (
	packagesViewList packagesViewMode = iota
	packagesViewFiles
	packagesViewInfo
)

// packagesTab is the Bubble Tea sub-model for the Packages tab.
type packagesTab struct {
	modules []moduleEntry
	tools   []toolEntry
	cursor  int
	loadErr string
	width   int
	height  int

	// Detail/info view state.
	viewMode    packagesViewMode
	detailTitle string
	detailBody  string

	// Update feedback.
	updateMsg string
	updateErr string
}

type packagesLoadedMsg struct {
	modules []moduleEntry
	tools   []toolEntry
	err     error
}

type libUpdateDoneMsg struct {
	report string
	err    error
}

func newPackagesTab() *packagesTab {
	return &packagesTab{}
}

func (t *packagesTab) Init() tea.Cmd {
	return t.loadPackages()
}

func (t *packagesTab) Editing() bool { return false }

func (t *packagesTab) SetSize(w, h int) {
	t.width = w
	t.height = h
}

// totalItems returns the combined count of modules and tools for cursor navigation.
func (t *packagesTab) totalItems() int {
	return len(t.modules) + len(t.tools)
}

func (t *packagesTab) Update(msg tea.Msg) (tabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case packagesLoadedMsg:
		if msg.err != nil {
			t.loadErr = msg.err.Error()
		} else {
			t.modules = msg.modules
			t.tools = msg.tools
			t.loadErr = ""
		}
		return t, nil

	case libUpdateDoneMsg:
		if msg.err != nil {
			t.updateErr = msg.err.Error()
			t.updateMsg = ""
		} else {
			t.updateMsg = msg.report
			t.updateErr = ""
		}
		// Reload packages to reflect changes.
		return t, t.loadPackages()

	case tabActivatedMsg:
		return t, t.loadPackages()

	case tea.KeyMsg:
		// In detail/info view, only Esc goes back.
		if t.viewMode != packagesViewList {
			if key.Matches(msg, keys.Escape) {
				t.viewMode = packagesViewList
				t.detailTitle = ""
				t.detailBody = ""
			}
			return t, nil
		}

		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("up", "k"))):
			if t.cursor > 0 {
				t.cursor--
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("down", "j"))):
			if t.cursor < t.totalItems()-1 {
				t.cursor++
			}
		case key.Matches(msg, keys.Enter):
			t.showFiles()
		case key.Matches(msg, key.NewBinding(key.WithKeys("i"))):
			t.showInfo()
		case key.Matches(msg, key.NewBinding(key.WithKeys("u"))):
			return t, t.doUpdate()
		}
		return t, nil
	}

	return t, nil
}

// selectedDir returns the absolute path and display name of the currently selected item.
func (t *packagesTab) selectedDir() (dir, name string, ok bool) {
	if t.totalItems() == 0 {
		return "", "", false
	}
	userLib, err := lib.UserLibDir()
	if err != nil {
		return "", "", false
	}

	if t.cursor < len(t.modules) {
		m := t.modules[t.cursor]
		return filepath.Join(userLib, m.name), m.name, true
	}
	toolIdx := t.cursor - len(t.modules)
	if toolIdx < len(t.tools) {
		tl := t.tools[toolIdx]
		return filepath.Join(userLib, "tools", tl.id), tl.id, true
	}
	return "", "", false
}

// showFiles populates the detail view with a file listing for the selected item.
func (t *packagesTab) showFiles() {
	dir, name, ok := t.selectedDir()
	if !ok {
		return
	}

	var b strings.Builder
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if d.IsDir() {
			if rel != "." {
				b.WriteString("  " + rel + "/\n")
			}
		} else {
			info, _ := d.Info()
			size := ""
			if info != nil {
				size = fmt.Sprintf("  (%d bytes)", info.Size())
			}
			b.WriteString("  " + rel + size + "\n")
		}
		return nil
	})

	t.viewMode = packagesViewFiles
	t.detailTitle = name + " — Files"
	t.detailBody = b.String()
}

// showInfo populates the detail view with the primary definition file content.
func (t *packagesTab) showInfo() {
	dir, name, ok := t.selectedDir()
	if !ok {
		return
	}

	// For modules: show task.md. For tools: show tool.yaml.
	var infoFile string
	if t.cursor < len(t.modules) {
		infoFile = filepath.Join(dir, "task.md")
	} else {
		infoFile = filepath.Join(dir, "tool.yaml")
	}

	data, err := os.ReadFile(infoFile)
	if err != nil {
		t.detailBody = statusErr.Render("Error reading: " + err.Error())
	} else {
		t.detailBody = string(data)
	}

	t.viewMode = packagesViewInfo
	t.detailTitle = name + " — Info"
}

// doUpdate triggers a lib update (re-extract embedded files).
func (t *packagesTab) doUpdate() tea.Cmd {
	return func() tea.Msg {
		userLib, err := lib.UserLibDir()
		if err != nil {
			return libUpdateDoneMsg{err: err}
		}
		report, err := lib.Update(userLib, false)
		if err != nil {
			return libUpdateDoneMsg{err: err}
		}
		summary := fmt.Sprintf("Updated %d, created %d, skipped %d (user-modified), pruned %d",
			len(report.Updated), len(report.Created), len(report.Skipped), len(report.Pruned))
		return libUpdateDoneMsg{report: summary}
	}
}

func (t *packagesTab) loadPackages() tea.Cmd {
	return func() tea.Msg {
		userLib, err := lib.UserLibDir()
		if err != nil {
			return packagesLoadedMsg{err: err}
		}

		manifest, err := lib.ReadManifest(userLib)
		if err != nil {
			return packagesLoadedMsg{err: fmt.Errorf("read manifest: %w", err)}
		}

		modules := loadModules(userLib, manifest)
		libTools := loadTools(userLib, manifest)

		return packagesLoadedMsg{modules: modules, tools: libTools}
	}
}

// loadModules scans ~/.smith/lib/ for module directories (excluding tools/).
func loadModules(libDir string, manifest lib.Manifest) []moduleEntry {
	entries, err := os.ReadDir(libDir)
	if err != nil {
		return nil
	}

	var modules []moduleEntry
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "tools" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		name := e.Name()
		modDir := filepath.Join(libDir, name)

		// Must have task.md to be a valid module.
		taskPath := filepath.Join(modDir, "task.md")
		if _, err := os.Stat(taskPath); err != nil {
			continue
		}

		me := moduleEntry{
			name:        name,
			provenance:  detectProvenance(modDir, name, manifest),
			description: extractModuleDescription(taskPath),
		}
		modules = append(modules, me)
	}
	return modules
}

// loadTools scans ~/.smith/lib/tools/ for tool directories.
func loadTools(libDir string, manifest lib.Manifest) []toolEntry {
	toolsDir := filepath.Join(libDir, "tools")
	entries, err := os.ReadDir(toolsDir)
	if err != nil {
		return nil
	}

	var result []toolEntry
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		id := e.Name()
		toolDir := filepath.Join(toolsDir, id)

		te := toolEntry{
			id:         id,
			provenance: detectProvenance(toolDir, filepath.Join("tools", id), manifest),
		}

		def, err := tools.ParseToolDir(toolDir)
		if err != nil {
			te.err = err.Error()
			te.description = "(error)"
			te.toolType = "?"
		} else {
			te.description = def.ToolYAML.Description
			te.toolType = def.ToolYAML.Type
		}

		result = append(result, te)
	}
	return result
}

// detectProvenance classifies a module/tool directory against the embedded manifest.
// prefix is the relative path prefix within the lib dir (e.g. "planner" or "tools/web.lookup").
func detectProvenance(dir, prefix string, manifest lib.Manifest) provenance {
	// Collect all manifest entries that belong to this directory.
	manifestEntries := make(map[string]string) // relPath -> expected hash
	for relPath, hash := range manifest {
		if strings.HasPrefix(relPath, prefix+"/") || relPath == prefix {
			manifestEntries[relPath] = hash
		}
	}

	// If no manifest entries exist for this directory, it's user-added.
	if len(manifestEntries) == 0 {
		return provenanceUserAdded
	}

	// Check all manifest entries: each file must exist and match its hash.
	for relPath, expectedHash := range manifestEntries {
		// relPath is relative to the lib root; compute path relative to this dir.
		subPath := strings.TrimPrefix(relPath, prefix+"/")
		filePath := filepath.Join(dir, subPath)

		data, err := os.ReadFile(filePath)
		if err != nil {
			return provenanceUserModified
		}

		h := sha256.Sum256(data)
		if hex.EncodeToString(h[:]) != expectedHash {
			return provenanceUserModified
		}
	}

	// Check for extra files not in the manifest.
	hasExtras := false
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		// Build the manifest key: prefix + "/" + relative path within dir.
		manifestKey := filepath.ToSlash(filepath.Join(prefix, rel))
		if _, inManifest := manifest[manifestKey]; !inManifest {
			hasExtras = true
		}
		return nil
	})

	if hasExtras {
		return provenanceUserModified
	}

	return provenanceEmbedded
}

// extractModuleDescription reads the first non-empty line after frontmatter from task.md.
func extractModuleDescription(taskPath string) string {
	f, err := os.Open(taskPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	inFrontmatter := false
	frontmatterDone := false

	for scanner.Scan() {
		line := scanner.Text()

		if !frontmatterDone {
			trimmed := strings.TrimSpace(line)
			if trimmed == "---" {
				if !inFrontmatter {
					inFrontmatter = true
					continue
				}
				// End of frontmatter.
				frontmatterDone = true
				continue
			}
			if inFrontmatter {
				continue
			}
			// No frontmatter at all — first line is content.
			frontmatterDone = true
		}

		// Skip empty lines after frontmatter.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Return first non-empty content line, truncated.
		desc := trimmed
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		return desc
	}

	return ""
}

func (t *packagesTab) View() string {
	// Detail/info sub-views.
	if t.viewMode != packagesViewList {
		return t.viewDetail()
	}

	var b strings.Builder

	b.WriteString(sectionHeader.Render("Packages"))
	b.WriteString("\n")

	if t.loadErr != "" {
		b.WriteString(statusErr.Render("  Error: "+t.loadErr) + "\n")
		return b.String()
	}

	if len(t.modules) == 0 && len(t.tools) == 0 {
		b.WriteString(muted.Render("  No packages installed. Run `smith init` to extract built-in modules.") + "\n")
		return b.String()
	}

	// Modules section.
	idx := 0
	if len(t.modules) > 0 {
		b.WriteString(sectionHeader.Render("Modules"))
		b.WriteString("\n")

		for _, m := range t.modules {
			cursor := "  "
			if idx == t.cursor {
				cursor = "> "
			}
			idx++

			prov := provenanceStyle(m.provenance)
			desc := m.description
			if m.err != "" {
				desc = statusErr.Render(m.err)
			}

			line := fmt.Sprintf("%-20s %-15s %s", m.name, prov, desc)
			b.WriteString(cursor + line + "\n")
		}
		b.WriteString("\n")
	}

	// Tools section.
	if len(t.tools) > 0 {
		b.WriteString(sectionHeader.Render("Tools"))
		b.WriteString("\n")

		for _, tl := range t.tools {
			cursor := "  "
			if idx == t.cursor {
				cursor = "> "
			}
			idx++

			prov := provenanceStyle(tl.provenance)
			desc := tl.description
			if tl.err != "" {
				desc = statusErr.Render(tl.err)
			}

			line := fmt.Sprintf("%-20s %-8s %-15s %s", tl.id, tl.toolType, prov, desc)
			b.WriteString(cursor + line + "\n")
		}
		b.WriteString("\n")
	}

	// Update feedback.
	if t.updateErr != "" {
		b.WriteString(statusErr.Render("  Update error: "+t.updateErr) + "\n")
	}
	if t.updateMsg != "" {
		b.WriteString(statusOK.Render("  "+t.updateMsg) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(helpBar.Render("  [Enter] View files  [i] Info  [u] Update all  [↑/↓] Navigate"))

	return b.String()
}

func (t *packagesTab) viewDetail() string {
	var b strings.Builder

	b.WriteString(sectionHeader.Render(t.detailTitle))
	b.WriteString("\n")
	b.WriteString(t.detailBody)
	b.WriteString("\n")
	b.WriteString(helpBar.Render("  [Esc] Back"))

	return b.String()
}

// provenanceStyle renders a provenance label with appropriate styling.
func provenanceStyle(p provenance) string {
	switch p {
	case provenanceEmbedded:
		return statusOK.Render(p.String())
	case provenanceUserModified:
		return muted.Render(p.String())
	case provenanceUserAdded:
		return fieldValue.Render(p.String())
	default:
		return p.String()
	}
}
