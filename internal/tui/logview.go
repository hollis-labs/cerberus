package tui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/chrispian/cerberus/internal/service"
)

// logViewExitMsg signals that the log viewer should close and return to the main view.
type logViewExitMsg struct{}

// Inline styles for the log viewer (not modifying styles.go).
var (
	logHeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#ffffff")).
			Background(lipgloss.Color("#7b68ee")).
			Padding(0, 1)

	logPathStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666")).
			PaddingLeft(1)

	logFooterStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666")).
			PaddingTop(0)

	logFooterKeyStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#7b68ee")).
				Bold(true)

	logFooterDescStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#666666"))

	logLineNumberStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#444444")).
				Width(6).
				Align(lipgloss.Right)

	logContentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#cccccc"))

	logEmptyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666")).
			PaddingLeft(2).
			PaddingTop(1)

	logFollowBadge = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00d787")).
			Bold(true).
			PaddingLeft(1)

	logScrollInfoStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#666666")).
				PaddingLeft(1)
)

// LogViewModel is the sub-model for viewing per-service log files.
type LogViewModel struct {
	serviceID   string
	serviceName string
	logPath     string
	lines       []string
	scrollPos   int
	following   bool
	width       int
	height      int
}

// NewLogViewModel creates a LogViewModel for the given service.
func NewLogViewModel(svc *service.Service, width, height int) LogViewModel {
	lv := LogViewModel{
		serviceID:   svc.Def.ID,
		serviceName: svc.Def.Name,
		logPath:     svc.LogPath(),
		following:   true,
		width:       width,
		height:      height,
	}
	lv.loadLog()
	// Start at bottom if following
	if lv.following {
		lv.scrollToBottom()
	}
	return lv
}

// visibleLines returns how many log lines fit between the header and footer.
func (lv *LogViewModel) visibleLines() int {
	// header (2 lines: title + separator) + footer (2 lines: separator + help)
	overhead := 4
	v := lv.height - overhead
	if v < 1 {
		v = 1
	}
	return v
}

// scrollToBottom sets scrollPos so the last lines are visible.
func (lv *LogViewModel) scrollToBottom() {
	maxScroll := len(lv.lines) - lv.visibleLines()
	if maxScroll < 0 {
		maxScroll = 0
	}
	lv.scrollPos = maxScroll
}

// clampScroll ensures scrollPos is within valid bounds.
func (lv *LogViewModel) clampScroll() {
	maxScroll := len(lv.lines) - lv.visibleLines()
	if maxScroll < 0 {
		maxScroll = 0
	}
	if lv.scrollPos > maxScroll {
		lv.scrollPos = maxScroll
	}
	if lv.scrollPos < 0 {
		lv.scrollPos = 0
	}
}

// loadLog reads the log file from disk and splits it into lines.
func (lv *LogViewModel) loadLog() {
	data, err := os.ReadFile(lv.logPath)
	if err != nil {
		lv.lines = nil
		return
	}
	content := string(data)
	if content == "" {
		lv.lines = nil
		return
	}
	lv.lines = strings.Split(content, "\n")
	// Remove trailing empty line from final newline
	if len(lv.lines) > 0 && lv.lines[len(lv.lines)-1] == "" {
		lv.lines = lv.lines[:len(lv.lines)-1]
	}
}

// Reload reloads the log file and auto-scrolls if following.
func (lv *LogViewModel) Reload() {
	lv.loadLog()
	if lv.following {
		lv.scrollToBottom()
	}
	lv.clampScroll()
}

// Update handles key input for the log viewer.
func (lv LogViewModel) Update(msg tea.Msg) (LogViewModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		lv.width = msg.Width
		lv.height = msg.Height
		lv.clampScroll()
		return lv, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc":
			return lv, func() tea.Msg { return logViewExitMsg{} }

		case "j", "down":
			lv.scrollPos++
			lv.following = false
			maxScroll := len(lv.lines) - lv.visibleLines()
			if maxScroll < 0 {
				maxScroll = 0
			}
			if lv.scrollPos >= maxScroll {
				lv.scrollPos = maxScroll
				lv.following = true
			}
			lv.clampScroll()

		case "k", "up":
			lv.scrollPos--
			lv.following = false
			lv.clampScroll()

		case "G":
			lv.scrollToBottom()
			lv.following = true

		case "g":
			lv.scrollPos = 0
			lv.following = false

		case "pgdown", "ctrl+d":
			halfPage := lv.visibleLines() / 2
			lv.scrollPos += halfPage
			lv.following = false
			maxScroll := len(lv.lines) - lv.visibleLines()
			if maxScroll < 0 {
				maxScroll = 0
			}
			if lv.scrollPos >= maxScroll {
				lv.scrollPos = maxScroll
				lv.following = true
			}
			lv.clampScroll()

		case "pgup", "ctrl+u":
			halfPage := lv.visibleLines() / 2
			lv.scrollPos -= halfPage
			lv.following = false
			lv.clampScroll()
		}
	}

	return lv, nil
}

// View renders the log viewer.
func (lv LogViewModel) View() string {
	var b strings.Builder

	// Header
	header := logHeaderStyle.Render(" Logs: " + lv.serviceName + " ")
	pathInfo := logPathStyle.Render(lv.logPath)
	followInfo := ""
	if lv.following {
		followInfo = logFollowBadge.Render("[FOLLOW]")
	}
	scrollInfo := ""
	if len(lv.lines) > 0 {
		scrollInfo = logScrollInfoStyle.Render(
			fmt.Sprintf("(%d/%d)", lv.scrollPos+lv.visibleLines(), len(lv.lines)),
		)
	}
	b.WriteString(header + pathInfo + followInfo + scrollInfo + "\n")

	// Separator
	sep := lipgloss.NewStyle().Foreground(lipgloss.Color("#444444")).
		Render(strings.Repeat("─", maxInt(lv.width, 40)))
	b.WriteString(sep + "\n")

	// Content
	if len(lv.lines) == 0 {
		b.WriteString(logEmptyStyle.Render("No log file available") + "\n")
	} else {
		visible := lv.visibleLines()
		start := lv.scrollPos
		end := start + visible
		if end > len(lv.lines) {
			end = len(lv.lines)
		}

		for i := start; i < end; i++ {
			lineNum := logLineNumberStyle.Render(fmt.Sprintf("%d", i+1))
			content := logContentStyle.Render(" " + lv.lines[i])
			b.WriteString(lineNum + content + "\n")
		}

		// Pad remaining lines if content is shorter than viewport
		for i := end - start; i < visible; i++ {
			b.WriteString("\n")
		}
	}

	// Separator
	b.WriteString(sep + "\n")

	// Footer
	help := []struct{ key, desc string }{
		{"j/k", "scroll"},
		{"G", "bottom"},
		{"g", "top"},
		{"PgDn/PgUp", "page"},
		{"q/Esc", "back"},
	}
	var helpParts []string
	for _, h := range help {
		helpParts = append(helpParts,
			logFooterKeyStyle.Render(h.key)+" "+logFooterDescStyle.Render(h.desc))
	}
	b.WriteString(logFooterStyle.Render(" " + strings.Join(helpParts, "  ")) + "\n")

	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
