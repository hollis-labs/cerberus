package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/chrispian/cerberus/internal/service"
)

func (m Model) View() string {
	var b strings.Builder

	// Title bar
	title := titleStyle.Render(" CERBERUS ")
	subtitle := headerStyle.Render("Tiamat Service Manager")
	b.WriteString(title + " " + subtitle + "\n\n")

	// Column headers
	sortIndicator := func(f sortField) string {
		if m.sortBy == f {
			return " ▼"
		}
		return ""
	}

	headers := fmt.Sprintf("  %-18s %-10s %6s  %-30s %7s  %s",
		colHeaderStyle.Render("SERVICE"+sortIndicator(sortName)),
		colHeaderStyle.Render("STATUS"+sortIndicator(sortStatus)),
		colHeaderStyle.Render("PORT"+sortIndicator(sortPort)),
		colHeaderStyle.Render("URL"),
		colHeaderStyle.Render("PID"),
		colHeaderStyle.Render("ACTION"),
	)
	b.WriteString(headers + "\n")

	// Separator
	sep := lipgloss.NewStyle().Foreground(colorDim).Render(strings.Repeat("─", min(m.width, 100)))
	b.WriteString("  " + sep + "\n")

	// Rows
	visible := m.visibleServices()
	for i, svc := range visible {
		row := m.renderRow(svc, i == m.cursor)
		b.WriteString(row + "\n")
	}

	if len(visible) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(colorDim).PaddingLeft(3).Render("No services match filter") + "\n")
	}

	// Spacer
	b.WriteString("\n")

	// Summary bar
	running, stopped, starting, building := 0, 0, 0, 0
	for _, s := range m.services {
		switch s.Status {
		case service.StatusRunning:
			running++
		case service.StatusStarting:
			starting++
		case service.StatusBuilding:
			building++
		default:
			stopped++
		}
	}
	_ = building
	summary := fmt.Sprintf("  %s %d  %s %d  %s %d",
		runningStyle.Render("●"), running,
		stoppedStyle.Render("●"), stopped,
		startingStyle.Render("●"), starting,
	)
	b.WriteString(summary + "\n")

	// Message
	if m.message != "" && time.Now().Before(m.msgExpiry) {
		b.WriteString(filterStyle.Render("  "+m.message) + "\n")
	} else {
		b.WriteString("\n")
	}

	// Filter bar
	if m.filtering {
		b.WriteString(filterStyle.Render("  filter: "+m.filter+"█") + "\n")
	} else if m.filter != "" {
		b.WriteString(filterStyle.Render("  filter: "+m.filter+" (esc to clear)") + "\n")
	}

	// Help footer
	help := []struct{ key, desc string }{
		{"s", "start"},
		{"x", "stop"},
		{"r", "restart"},
		{"b", "build"},
		{"enter", "open"},
		{"a", "start all"},
		{"X", "stop all"},
		{"tab", "sort"},
		{"/", "filter"},
		{"q", "quit"},
	}

	var helpParts []string
	for _, h := range help {
		helpParts = append(helpParts,
			helpKeyStyle.Render(h.key)+" "+helpDescStyle.Render(h.desc))
	}
	b.WriteString(footerStyle.Render(strings.Join(helpParts, "  ")) + "\n")

	return b.String()
}

func (m Model) renderRow(svc *service.Service, selected bool) string {
	// Status badge
	var statusStr string
	switch svc.Status {
	case service.StatusRunning:
		statusStr = runningStyle.Render("● running")
	case service.StatusStarting:
		statusStr = startingStyle.Render("◐ starting")
	case service.StatusBuilding:
		statusStr = startingStyle.Render("⚙ building")
	default:
		statusStr = stoppedStyle.Render("○ stopped")
	}

	// PID
	pidStr := "  —"
	if svc.PID > 0 {
		pidStr = fmt.Sprintf("%d", svc.PID)
	}

	// URL
	urlStr := svc.Def.URL
	if urlStr == "" {
		urlStr = fmt.Sprintf(":%d", svc.Def.Port)
	}

	// Action hint
	action := ""
	if svc.Status == service.StatusRunning && svc.Def.URL != "" {
		action = lipgloss.NewStyle().Foreground(colorBlue).Render("[open]")
	} else if svc.Status == service.StatusStopped {
		action = lipgloss.NewStyle().Foreground(colorDim).Render("[start]")
	}

	// Cursor
	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸ ")
	}

	row := fmt.Sprintf("%s%-18s %-10s %6d  %-30s %7s  %s",
		cursor,
		svc.Def.Name,
		statusStr,
		svc.Def.Port,
		lipgloss.NewStyle().Foreground(colorDim).Render(urlStr),
		pidStr,
		action,
	)

	var result string
	if selected {
		result = selectedRowStyle.Render(row)
	} else {
		result = rowStyle.Render(row)
	}

	// Show error on the line below if present
	if svc.Error != "" && selected {
		errLine := errorStyle.Render("    ⚠ " + svc.Error)
		result += "\n" + errLine
	}
	if svc.BuildErr != "" && selected {
		errLine := errorStyle.Render("    ⚠ build: " + svc.BuildErr)
		result += "\n" + errLine
	}

	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
