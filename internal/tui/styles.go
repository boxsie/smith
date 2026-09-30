package tui

import "github.com/charmbracelet/lipgloss"

// Colors.
var (
	colorPrimary   = lipgloss.Color("62")  // soft purple
	colorSecondary = lipgloss.Color("241") // muted gray
	colorSuccess   = lipgloss.Color("78")  // green
	colorError     = lipgloss.Color("196") // red
	colorMuted     = lipgloss.Color("240") // dim gray
)

// Tab bar styles.
var (
	tabActive = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("255")).
			Background(colorPrimary).
			Padding(0, 2)

	tabInactive = lipgloss.NewStyle().
			Foreground(colorSecondary).
			Padding(0, 2)

	bottomBar = lipgloss.NewStyle().
		BorderTop(true).
		BorderStyle(lipgloss.NormalBorder()).
		BorderTopForeground(colorSecondary)
)

// Content styles.
var (
	sectionHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPrimary).
			MarginBottom(1)

	statusOK = lipgloss.NewStyle().
			Foreground(colorSuccess)

	statusErr = lipgloss.NewStyle().
			Foreground(colorError)

	muted = lipgloss.NewStyle().
		Foreground(colorMuted)

	fieldLabel = lipgloss.NewStyle().
			Width(24)

	fieldValue = lipgloss.NewStyle()

	contentPane = lipgloss.NewStyle().
			Padding(1, 2)

	helpBar = lipgloss.NewStyle().
		Foreground(colorMuted).
		MarginTop(1)
)
