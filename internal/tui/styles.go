package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	colorGreen  = lipgloss.Color("#00d787")
	colorRed    = lipgloss.Color("#ff5f87")
	colorYellow = lipgloss.Color("#ffd75f")
	colorBlue   = lipgloss.Color("#5fafff")
	colorDim    = lipgloss.Color("#666666")
	colorWhite  = lipgloss.Color("#ffffff")
	colorBg     = lipgloss.Color("#1a1a2e")
	colorAccent = lipgloss.Color("#7b68ee")

	// Header
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent).
			PaddingLeft(1)

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorWhite).
			Background(colorAccent).
			Padding(0, 2)

	// Table
	colHeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorBlue).
			Underline(true)

	rowStyle = lipgloss.NewStyle().
			PaddingLeft(1)

	selectedRowStyle = lipgloss.NewStyle().
				PaddingLeft(1).
				Background(lipgloss.Color("#2a2a4e")).
				Bold(true)

	// Status badges
	runningStyle = lipgloss.NewStyle().
			Foreground(colorGreen).
			Bold(true)

	stoppedStyle = lipgloss.NewStyle().
			Foreground(colorRed)

	startingStyle = lipgloss.NewStyle().
			Foreground(colorYellow)

	// Footer
	footerStyle = lipgloss.NewStyle().
			Foreground(colorDim).
			PaddingLeft(1).
			PaddingTop(1)

	helpKeyStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	helpDescStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	// Filter
	filterStyle = lipgloss.NewStyle().
			Foreground(colorYellow).
			PaddingLeft(1)

	errorStyle = lipgloss.NewStyle().
			Foreground(colorRed).
			PaddingLeft(1)
)
