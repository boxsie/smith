package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTabCycling(t *testing.T) {
	m := initialModel()
	if m.activeTab != 0 {
		t.Fatalf("initial activeTab = %d, want 0", m.activeTab)
	}

	// Tab forward through all tabs
	for i := 1; i < len(tabNames); i++ {
		result, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = result.(model)
		if m.activeTab != i {
			t.Fatalf("after %d Tab presses: activeTab = %d, want %d", i, m.activeTab, i)
		}
	}

	// Wraps around
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = result.(model)
	if m.activeTab != 0 {
		t.Fatalf("after wrap: activeTab = %d, want 0", m.activeTab)
	}
}

func TestShiftTabCycling(t *testing.T) {
	m := initialModel()

	// Shift+Tab from 0 wraps to last tab
	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = result.(model)
	want := len(tabNames) - 1
	if m.activeTab != want {
		t.Fatalf("shift+tab from 0: activeTab = %d, want %d", m.activeTab, want)
	}
}

func TestQuitKey(t *testing.T) {
	m := initialModel()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("q key should produce a quit command")
	}
	// Execute the cmd to check it returns a QuitMsg
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("q key cmd returned %T, want tea.QuitMsg", msg)
	}
}

func TestCtrlCAlwaysQuits(t *testing.T) {
	m := initialModel()

	// Even when the active tab is editing, ctrl+c should quit
	ct := m.tabs[0].(*configTab)
	ct.startEditing()
	if !ct.Editing() {
		t.Fatal("config tab should be editing after startEditing()")
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c should produce a quit command even during editing")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c cmd returned %T, want tea.QuitMsg", msg)
	}
}

func TestQuitSuppressedDuringEditing(t *testing.T) {
	m := initialModel()

	ct := m.tabs[0].(*configTab)
	ct.startEditing()

	result, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = result.(model)

	// q should be forwarded to the tab, not trigger quit
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(tea.QuitMsg); ok {
			t.Fatal("q key during editing should not quit")
		}
	}
}

func TestTabSuppressedDuringEditing(t *testing.T) {
	m := initialModel()

	ct := m.tabs[0].(*configTab)
	ct.startEditing()

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = result.(model)

	if m.activeTab != 0 {
		t.Fatalf("tab key during editing changed activeTab to %d, want 0", m.activeTab)
	}
}

func TestWindowResize(t *testing.T) {
	m := initialModel()
	result, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = result.(model)
	if m.width != 120 {
		t.Fatalf("width = %d, want 120", m.width)
	}
	if m.height != 40 {
		t.Fatalf("height = %d, want 40", m.height)
	}
}

func TestViewContainsAllTabNames(t *testing.T) {
	m := initialModel()
	// Give it a size so rendering works
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	view := m.View()
	for _, name := range tabNames {
		if !strings.Contains(view, name) {
			t.Errorf("view missing tab name %q", name)
		}
	}
}

func TestPlaceholderTabView(t *testing.T) {
	p := placeholderTab{name: "Projects"}
	if p.Editing() {
		t.Fatal("placeholder should not be editing")
	}
	view := p.View()
	if !strings.Contains(view, "coming soon") {
		t.Errorf("placeholder view = %q, want to contain 'coming soon'", view)
	}
}
