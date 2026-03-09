package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	groupHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#7b68ee"))

	groupHeaderSelectedStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(lipgloss.Color("#7b68ee")).
					Background(lipgloss.Color("#2a2a4e"))

	groupCountStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))

	tagFilterStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ffd75f")).
			PaddingLeft(1)
)

// renderGroupHeader renders a group header row with collapse indicator and service count.
func renderGroupHeader(group ServiceGroup, selected bool) string {
	indicator := "▾"
	if group.Collapsed {
		indicator = "▸"
	}

	name := strings.ToUpper(group.Name[:1]) + group.Name[1:]
	countStr := groupCountStyle.Render(fmt.Sprintf("(%d services)", len(group.Services)))

	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(lipgloss.Color("#7b68ee")).Bold(true).Render("▸ ")
	}

	header := fmt.Sprintf("%s%s %s %s", cursor, indicator, name, countStr)

	if selected {
		return groupHeaderSelectedStyle.Render(header)
	}
	return groupHeaderStyle.Render(header)
}

// renderTagFilter renders the current tag filter indicator.
func renderTagFilter(tag string) string {
	if tag == "" {
		return ""
	}
	return tagFilterStyle.Render(fmt.Sprintf("  tag: %s", tag))
}
