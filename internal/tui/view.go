package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/chrispian/cerberus/internal/service"
)

// Column widths for consistent alignment
const (
	colWidthName   = 20
	colWidthStatus = 12
	colWidthHealth = 10
	colWidthPort   = 6
	colWidthURL    = 28
	colWidthPID    = 7
	colWidthAction = 8
)

// cell renders text into a fixed-width column using lipgloss so that
// ANSI escape codes do not throw off alignment.
func cell(width int, text string) string {
	return lipgloss.NewStyle().Width(width).Render(text)
}

func (m Model) View() string {
	// Delegate to log view if active
	if m.logView != nil {
		return m.logView.View()
	}

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

	headers := "  " +
		cell(colWidthName, colHeaderStyle.Render("SERVICE"+sortIndicator(sortName))) +
		cell(colWidthStatus, colHeaderStyle.Render("STATUS"+sortIndicator(sortStatus))) +
		cell(colWidthHealth, colHeaderStyle.Render("HEALTH")) +
		cell(colWidthPort, colHeaderStyle.Render("PORT"+sortIndicator(sortPort))) +
		cell(colWidthURL, colHeaderStyle.Render("URL")) +
		cell(colWidthPID, colHeaderStyle.Render("PID")) +
		cell(colWidthAction, colHeaderStyle.Render("ACTION"))
	b.WriteString(headers + "\n")

	// Separator
	sep := lipgloss.NewStyle().Foreground(colorDim).Render(strings.Repeat("─", min(m.width, 100)))
	b.WriteString("  " + sep + "\n")

	// Rows
	if m.grouped {
		for i, item := range m.flatItems {
			if item.isHeader {
				group := m.groups[item.groupIndex]
				b.WriteString(renderGroupHeader(group, i == m.cursor) + "\n")
			} else {
				svc := m.groups[item.groupIndex].Services[item.svcIndex]
				b.WriteString(m.renderRow(svc, i == m.cursor) + "\n")
			}
		}
		if len(m.flatItems) == 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(colorDim).PaddingLeft(3).Render("No services match filter") + "\n")
		}
	} else {
		visible := m.visibleServices()
		for i, svc := range visible {
			row := m.renderRow(svc, i == m.cursor)
			b.WriteString(row + "\n")
		}
		if len(visible) == 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(colorDim).PaddingLeft(3).Render("No services match filter") + "\n")
		}
	}

	// Tag filter indicator
	if m.tagFilter != "" {
		b.WriteString(renderTagFilter(m.tagFilter) + "\n")
	}

	// Spacer
	b.WriteString("\n")

	// Summary bar
	running, stopped, starting, building, healthy, unhealthy, failed := 0, 0, 0, 0, 0, 0, 0
	for _, s := range m.services {
		switch s.Status {
		case service.StatusRunning:
			running++
		case service.StatusStarting:
			starting++
		case service.StatusBuilding:
			building++
		case service.StatusHealthy:
			healthy++
		case service.StatusUnhealthy:
			unhealthy++
		case service.StatusFailed:
			failed++
		default:
			stopped++
		}
	}
	summary := fmt.Sprintf("  %s %d  %s %d  %s %d  %s %d  %s %d  %s %d",
		runningStyle.Render("●"), running,
		healthyStyle.Render("●"), healthy,
		startingStyle.Render("●"), starting+building,
		unhealthyStyle.Render("●"), unhealthy,
		failedStyle.Render("●"), failed,
		stoppedStyle.Render("●"), stopped,
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
		{"l", "logs"},
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

	// Copyright + version
	contentWidth := colWidthName + colWidthStatus + colWidthHealth + colWidthPort + colWidthURL + colWidthPID + colWidthAction + 4
	copyLine := "(c) HOLLIS LABS"
	versionLine := fmt.Sprintf("cerberus %s (built %s)", m.version, m.buildDate)
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	b.WriteString("\n" + lipgloss.NewStyle().Width(contentWidth).Align(lipgloss.Center).Render(dimStyle.Render(copyLine)) + "\n")
	b.WriteString(lipgloss.NewStyle().Width(contentWidth).Align(lipgloss.Center).Render(dimStyle.Render(versionLine)) + "\n")

	// Center the content block within the terminal by padding left.
	content := b.String()
	pad := (m.width - contentWidth) / 2
	if pad < 0 {
		pad = 0
	}
	padStr := strings.Repeat(" ", pad)
	var out strings.Builder
	for _, line := range strings.Split(content, "\n") {
		out.WriteString(padStr + line + "\n")
	}
	return out.String()
}

func (m Model) renderRow(svc *service.Service, selected bool) string {
	// Status badge
	var statusStr string
	switch svc.Status {
	case service.StatusRunning:
		statusStr = runningStyle.Render("● running")
	case service.StatusHealthy:
		statusStr = healthyStyle.Render("● healthy")
	case service.StatusUnhealthy:
		statusStr = unhealthyStyle.Render("● unhealthy")
	case service.StatusStarting:
		statusStr = startingStyle.Render("◐ starting")
	case service.StatusBuilding:
		statusStr = startingStyle.Render("⚙ building")
	case service.StatusFailed:
		statusStr = failedStyle.Render("✖ failed")
	default:
		statusStr = stoppedStyle.Render("○ stopped")
	}

	// Append restart count if > 0
	if svc.RestartCount > 0 && (svc.Status == service.StatusRunning || svc.Status == service.StatusHealthy) {
		statusStr += lipgloss.NewStyle().Foreground(colorDim).Render(fmt.Sprintf(" (%dx)", svc.RestartCount))
	}

	// Health column
	var healthStr string
	if svc.Status == service.StatusRunning || svc.Status == service.StatusHealthy || svc.Status == service.StatusUnhealthy {
		if svc.HealthStatus.Healthy {
			healthStr = healthyStyle.Render("✔")
		} else if svc.HealthStatus.LastError != "" {
			healthStr = unhealthyStyle.Render("✖")
		} else {
			healthStr = lipgloss.NewStyle().Foreground(colorDim).Render("—")
		}
	} else {
		healthStr = lipgloss.NewStyle().Foreground(colorDim).Render("—")
	}

	// PID
	pidStr := "—"
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
	} else if svc.Status == service.StatusHealthy && svc.Def.URL != "" {
		action = lipgloss.NewStyle().Foreground(colorBlue).Render("[open]")
	} else if svc.Status == service.StatusStopped {
		action = lipgloss.NewStyle().Foreground(colorDim).Render("[start]")
	}

	// Cursor
	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸ ")
	}

	row := cursor +
		cell(colWidthName, svc.Def.Name) +
		cell(colWidthStatus, statusStr) +
		cell(colWidthHealth, healthStr) +
		cell(colWidthPort, fmt.Sprintf("%d", svc.Def.Port)) +
		cell(colWidthURL, lipgloss.NewStyle().Foreground(colorDim).Render(urlStr)) +
		cell(colWidthPID, pidStr) +
		cell(colWidthAction, action)

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
	// Show health error on hover if unhealthy
	if svc.HealthStatus.LastError != "" && selected && !svc.HealthStatus.Healthy {
		errLine := errorStyle.Render("    ⚠ health: " + svc.HealthStatus.LastError)
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
