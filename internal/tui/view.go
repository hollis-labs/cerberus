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
	colWidthFlags  = 4
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
	subtitle := headerStyle.Render("Fragments Engine Service Manager")
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
		cell(colWidthFlags, colHeaderStyle.Render("FL")) +
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

	// Rows — viewport scrolling to fit terminal height.
	maxRows := m.maxServiceRows()
	scrollOff := m.clampedScrollOff(maxRows)

	var rows []string
	var totalItems int

	if m.grouped {
		totalItems = len(m.flatItems)
		if totalItems == 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(colorDim).PaddingLeft(3).Render("No services match filter") + "\n")
		} else {
			end := scrollOff + maxRows
			if end > totalItems {
				end = totalItems
			}
			for i := scrollOff; i < end; i++ {
				item := m.flatItems[i]
				if item.isHeader {
					group := m.groups[item.groupIndex]
					rows = append(rows, renderGroupHeader(group, i == m.cursor))
				} else {
					svc := m.groups[item.groupIndex].Services[item.svcIndex]
					rows = append(rows, m.renderRow(svc, i == m.cursor))
				}
			}
		}
	} else {
		visible := m.visibleServices()
		totalItems = len(visible)
		if totalItems == 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(colorDim).PaddingLeft(3).Render("No services match filter") + "\n")
		} else {
			end := scrollOff + maxRows
			if end > totalItems {
				end = totalItems
			}
			for i := scrollOff; i < end; i++ {
				rows = append(rows, m.renderRow(visible[i], i == m.cursor))
			}
		}
	}

	// Render rows with optional scrollbar
	if len(rows) > 0 {
		scrollbar := buildScrollbar(len(rows), totalItems, scrollOff)
		for i, row := range rows {
			b.WriteString(row + scrollbar[i] + "\n")
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
		{"R", "rebuild"},
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
	contentWidth := colWidthName + colWidthFlags + colWidthStatus + colWidthHealth + colWidthPort + colWidthURL + colWidthPID + colWidthAction + 4
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

func (m Model) renderRow(svc *service.ManagedService, selected bool) string {
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
	if urlStr == "" && svc.Def.Port > 0 {
		urlStr = fmt.Sprintf(":%d", svc.Def.Port)
	} else if urlStr == "" {
		urlStr = "—"
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

	// Flags: P=protected, A=auto-restart
	dimFlag := lipgloss.NewStyle().Foreground(colorDim)
	pFlag := dimFlag.Render("P")
	if svc.Def.Protected {
		pFlag = lipgloss.NewStyle().Foreground(colorRed).Render("P")
	}
	aFlag := dimFlag.Render("A")
	if svc.Def.AutoRestart {
		aFlag = lipgloss.NewStyle().Foreground(colorBlue).Render("A")
	}
	flagsStr := pFlag + aFlag

	row := cursor +
		cell(colWidthName, svc.Def.Name) +
		cell(colWidthFlags, flagsStr) +
		cell(colWidthStatus, statusStr) +
		cell(colWidthHealth, healthStr) +
		cell(colWidthPort, func() string {
			if svc.Def.Port > 0 {
				return fmt.Sprintf("%d", svc.Def.Port)
			}
			return "—"
		}()) +
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

// maxServiceRows returns how many service/group rows fit in the terminal.
func (m Model) maxServiceRows() int {
	// Fixed chrome: title(2) + headers(1) + sep(1) + spacer(1) + summary(1) +
	//   message(1) + help(1) + copyright(3) + tag/filter(~1) = ~12-13 lines
	const chromeLines = 13
	rows := m.height - chromeLines
	if rows < 3 {
		rows = 3
	}
	return rows
}

// clampedScrollOff returns a scroll offset that keeps the cursor visible.
func (m Model) clampedScrollOff(maxRows int) int {
	var total int
	if m.grouped {
		total = len(m.flatItems)
	} else {
		total = len(m.visibleServices())
	}

	off := m.scrollOff
	if m.cursor < off {
		off = m.cursor
	}
	if m.cursor >= off+maxRows {
		off = m.cursor - maxRows + 1
	}
	if total > maxRows && off > total-maxRows {
		off = total - maxRows
	}
	if off < 0 {
		off = 0
	}
	return off
}

// buildScrollbar returns a string slice with a scrollbar character for each
// visible row. If all items fit on screen, returns empty strings (no bar).
func buildScrollbar(viewportSize, totalItems, scrollOff int) []string {
	result := make([]string, viewportSize)

	if totalItems <= viewportSize {
		// Everything fits — no scrollbar needed
		for i := range result {
			result[i] = ""
		}
		return result
	}

	trackStyle := lipgloss.NewStyle().Foreground(colorDim)
	thumbStyle := lipgloss.NewStyle().Foreground(colorAccent)

	// Calculate thumb position and size
	thumbSize := viewportSize * viewportSize / totalItems
	if thumbSize < 1 {
		thumbSize = 1
	}
	thumbPos := scrollOff * viewportSize / totalItems
	if thumbPos+thumbSize > viewportSize {
		thumbPos = viewportSize - thumbSize
	}

	for i := range result {
		if i >= thumbPos && i < thumbPos+thumbSize {
			result[i] = " " + thumbStyle.Render("┃")
		} else {
			result[i] = " " + trackStyle.Render("│")
		}
	}
	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
