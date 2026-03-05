package tui

import (
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/chrispian/cerberus/internal/service"
)

type sortField int

const (
	sortName sortField = iota
	sortStatus
	sortPort
)

type Model struct {
	services  []*service.Service
	cursor    int
	sortBy    sortField
	filter    string
	filtering bool
	width     int
	height    int
	message   string
	msgExpiry time.Time
}

type tickMsg time.Time

func NewModel(services []*service.Service) Model {
	// Do initial poll
	for _, s := range services {
		s.Poll()
	}

	return Model{
		services: services,
		width:    120,
		height:   30,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(tickCmd(), tea.WindowSize())
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		for _, s := range m.services {
			s.Poll()
		}
		return m, tickCmd()

	case tea.KeyMsg:
		// If filtering, handle text input
		if m.filtering {
			switch msg.String() {
			case "enter", "esc":
				m.filtering = false
				if msg.String() == "esc" {
					m.filter = ""
				}
			case "backspace":
				if len(m.filter) > 0 {
					m.filter = m.filter[:len(m.filter)-1]
				}
			default:
				if len(msg.String()) == 1 {
					m.filter += msg.String()
				}
			}
			return m, nil
		}

		visible := m.visibleServices()

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit

		case "j", "down":
			if m.cursor < len(visible)-1 {
				m.cursor++
			}

		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}

		case "s":
			if svc := m.selected(visible); svc != nil {
				if err := svc.Start(); err != nil {
					m.setMsg("Error: " + err.Error())
				} else {
					m.setMsg("Starting " + svc.Def.Name + "...")
				}
			}

		case "x":
			if svc := m.selected(visible); svc != nil {
				if err := svc.Stop(); err != nil {
					m.setMsg("Error: " + err.Error())
				} else {
					m.setMsg("Stopping " + svc.Def.Name + "...")
				}
			}

		case "r":
			if svc := m.selected(visible); svc != nil {
				svc.Stop()
				time.Sleep(500 * time.Millisecond)
				if err := svc.Start(); err != nil {
					m.setMsg("Error: " + err.Error())
				} else {
					m.setMsg("Restarting " + svc.Def.Name + "...")
				}
			}

		case "enter", "l":
			if svc := m.selected(visible); svc != nil && svc.Def.URL != "" {
				exec.Command("open", svc.Def.URL).Start()
				m.setMsg("Opening " + svc.Def.URL)
			}

		case "tab":
			m.sortBy = (m.sortBy + 1) % 3
			m.cursor = 0

		case "/":
			m.filtering = true
			m.filter = ""

		case "a":
			// Start all
			for _, svc := range m.services {
				if svc.Status == service.StatusStopped {
					svc.Start()
				}
			}
			m.setMsg("Starting all services...")

		case "X":
			// Stop all
			for _, svc := range m.services {
				if svc.Status == service.StatusRunning {
					svc.Stop()
				}
			}
			m.setMsg("Stopping all services...")
		}
	}

	return m, nil
}

func (m *Model) setMsg(msg string) {
	m.message = msg
	m.msgExpiry = time.Now().Add(3 * time.Second)
}

func (m Model) selected(visible []*service.Service) *service.Service {
	if m.cursor >= 0 && m.cursor < len(visible) {
		return visible[m.cursor]
	}
	return nil
}

func (m Model) visibleServices() []*service.Service {
	if m.filter == "" {
		return m.sorted(m.services)
	}

	f := strings.ToLower(m.filter)
	var filtered []*service.Service
	for _, s := range m.services {
		name := strings.ToLower(s.Def.Name)
		id := strings.ToLower(s.Def.ID)
		tags := strings.ToLower(strings.Join(s.Def.Tags, " "))
		if strings.Contains(name, f) || strings.Contains(id, f) || strings.Contains(tags, f) {
			filtered = append(filtered, s)
		}
	}
	return m.sorted(filtered)
}

func (m Model) sorted(svcs []*service.Service) []*service.Service {
	out := make([]*service.Service, len(svcs))
	copy(out, svcs)

	for i := 0; i < len(out)-1; i++ {
		for j := i + 1; j < len(out); j++ {
			swap := false
			switch m.sortBy {
			case sortName:
				swap = out[i].Def.Name > out[j].Def.Name
			case sortStatus:
				swap = out[i].Status < out[j].Status // running first
			case sortPort:
				swap = out[i].Def.Port > out[j].Def.Port
			}
			if swap {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
