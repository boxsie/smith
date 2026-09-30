package tui

import "github.com/charmbracelet/bubbles/key"

// keyMap defines the global key bindings for the TUI.
type keyMap struct {
	Quit      key.Binding
	NextTab   key.Binding
	PrevTab   key.Binding
	Enter     key.Binding
	Escape    key.Binding
	ForceQuit key.Binding
}

var keys = keyMap{
	Quit: key.NewBinding(
		key.WithKeys("q"),
		key.WithHelp("q", "quit"),
	),
	NextTab: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "next tab"),
	),
	PrevTab: key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift+tab", "prev tab"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "select"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel"),
	),
	ForceQuit: key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "force quit"),
	),
}
