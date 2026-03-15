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
	services  []*service.ManagedService
	cursor    int
	sortBy    sortField
	filter    string
	filtering bool
	width     int
	height    int
	message   string
	msgExpiry time.Time
	logView   *LogViewModel

	// Version info
	version   string
	buildDate string

	// Grouping state
	groups    []ServiceGroup
	grouped   bool
	tagFilter string
	allTags   []string
	tagIndex  int // current index in allTags for cycling
	flatItems []flatItem
	scrollOff int // first visible row index for viewport scrolling
}

type tickMsg time.Time

func NewModel(services []*service.ManagedService, version, buildDate string) Model {
	// Do initial poll
	for _, s := range services {
		s.Poll()
	}

	m := Model{
		services:  services,
		version:   version,
		buildDate: buildDate,
		width:     120,
		height:    30,
		grouped:   true,
		allTags:   collectUniqueTags(services),
		tagIndex:  -1, // -1 means no tag filter active
	}
	m.rebuildGroups()
	return m
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
	// Handle log view exit
	if _, ok := msg.(logViewExitMsg); ok {
		m.logView = nil
		return m, nil
	}

	// Delegate to log view if active
	if m.logView != nil {
		switch msg := msg.(type) {
		case tea.WindowSizeMsg:
			m.width = msg.Width
			m.height = msg.Height
			m.logView.width = msg.Width
			m.logView.height = msg.Height
			m.logView.clampScroll()
			return m, nil
		case tickMsg:
			// Reload log on tick and also poll services
			m.logView.Reload()
			for _, s := range m.services {
				if s.BuildDone != nil {
					select {
					case <-s.BuildDone:
						s.BuildDone = nil
					default:
					}
				}
				s.Poll()
			}
			return m, tickCmd()
		default:
			lv, cmd := m.logView.Update(msg)
			m.logView = &lv
			return m, cmd
		}
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		for _, s := range m.services {
			// Check for completed builds
			if s.BuildDone != nil {
				select {
				case <-s.BuildDone:
					s.BuildDone = nil
					if s.BuildErr != "" {
						m.setMsg("Build failed: " + s.Def.Name)
					} else {
						m.setMsg("Build complete: " + s.Def.Name)
					}
				default:
				}
			}
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

		// Rebuild groups/flat items on each key press to reflect current state
		m.rebuildGroups()
		visible := m.visibleServices()

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit

		case "j", "down":
			if m.grouped {
				if m.cursor < len(m.flatItems)-1 {
					m.cursor++
				}
			} else {
				if m.cursor < len(visible)-1 {
					m.cursor++
				}
			}

		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}

		case "s":
			if svc := m.selectedService(); svc != nil {
				if err := svc.Start(); err != nil {
					m.setMsg("Error: " + err.Error())
				} else {
					m.setMsg("Starting " + svc.Def.Name + "...")
				}
			}

		case "x":
			if svc := m.selectedService(); svc != nil {
				if err := svc.Stop(); err != nil {
					m.setMsg("Error: " + err.Error())
				} else {
					m.setMsg("Stopping " + svc.Def.Name + "...")
				}
			}

		case "r":
			if svc := m.selectedService(); svc != nil {
				svc.Stop()
				time.Sleep(500 * time.Millisecond)
				if err := svc.Start(); err != nil {
					m.setMsg("Error: " + err.Error())
				} else {
					m.setMsg("Restarting " + svc.Def.Name + "...")
				}
			}

		case "b":
			if svc := m.selectedService(); svc != nil {
				if err := svc.Build(); err != nil {
					m.setMsg("Error: " + err.Error())
				} else {
					m.setMsg("Building " + svc.Def.Name + "...")
				}
			}

		case "R":
			if svc := m.selectedService(); svc != nil {
				if len(svc.Def.Build) == 0 {
					// No build command — just restart
					svc.Stop()
					time.Sleep(500 * time.Millisecond)
					if err := svc.Start(); err != nil {
						m.setMsg("Error: " + err.Error())
					} else {
						m.setMsg("Restarting " + svc.Def.Name + " (no build configured)...")
					}
				} else {
					m.setMsg("Building " + svc.Def.Name + "...")
					go func() {
						out, err := svc.BuildSync()
						if err != nil {
							_ = out
							svc.BuildErr = err.Error()
							return
						}
						svc.Stop()
						time.Sleep(500 * time.Millisecond)
						svc.Start()
					}()
				}
			}

		case "enter":
			if m.grouped && m.cursor >= 0 && m.cursor < len(m.flatItems) {
				item := m.flatItems[m.cursor]
				if item.isHeader {
					m.groups[item.groupIndex].Collapsed = !m.groups[item.groupIndex].Collapsed
					m.flatItems = buildFlatItems(m.groups)
					// Clamp cursor
					if m.cursor >= len(m.flatItems) {
						m.cursor = len(m.flatItems) - 1
					}
					return m, nil
				}
			}
			if svc := m.selectedService(); svc != nil && svc.Def.URL != "" {
				exec.Command("open", svc.Def.URL).Start()
				m.setMsg("Opening " + svc.Def.URL)
			}

		case "l":
			if svc := m.selectedService(); svc != nil {
				lv := NewLogViewModel(svc, m.width, m.height)
				m.logView = &lv
			}

		case "g":
			m.grouped = !m.grouped
			m.cursor = 0
			if m.grouped {
				m.rebuildGroups()
				m.setMsg("Grouped view")
			} else {
				m.setMsg("Flat view")
			}

		case "t":
			// Cycle through tags
			if len(m.allTags) == 0 {
				m.setMsg("No tags available")
			} else {
				m.tagIndex++
				if m.tagIndex >= len(m.allTags) {
					m.tagIndex = -1
					m.tagFilter = ""
					m.setMsg("Tag filter cleared")
				} else {
					m.tagFilter = m.allTags[m.tagIndex]
					m.setMsg("Tag filter: " + m.tagFilter)
				}
				m.cursor = 0
				m.rebuildGroups()
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

	// Keep scroll offset in sync with cursor
	m.scrollOff = m.clampedScrollOff(m.maxServiceRows())

	return m, nil
}

func (m *Model) setMsg(msg string) {
	m.message = msg
	m.msgExpiry = time.Now().Add(3 * time.Second)
}

func (m Model) selected(visible []*service.ManagedService) *service.ManagedService {
	if m.cursor >= 0 && m.cursor < len(visible) {
		return visible[m.cursor]
	}
	return nil
}

// selectedService returns the service under the cursor, respecting grouped mode.
// Returns nil if cursor is on a group header or out of range.
func (m Model) selectedService() *service.ManagedService {
	if m.grouped {
		if m.cursor >= 0 && m.cursor < len(m.flatItems) {
			item := m.flatItems[m.cursor]
			if !item.isHeader {
				return m.groups[item.groupIndex].Services[item.svcIndex]
			}
		}
		return nil
	}
	visible := m.visibleServices()
	return m.selected(visible)
}

// rebuildGroups rebuilds the group list and flat items from current services,
// applying text filter and tag filter.
func (m *Model) rebuildGroups() {
	svcs := m.filteredServices()
	svcs = filterServicesByTag(svcs, m.tagFilter)
	sorted := m.sorted(svcs)
	m.groups = GroupByProject(sorted)
	m.flatItems = buildFlatItems(m.groups)
}

func (m Model) visibleServices() []*service.ManagedService {
	svcs := m.filteredServices()
	svcs = filterServicesByTag(svcs, m.tagFilter)
	return m.sorted(svcs)
}

func (m Model) filteredServices() []*service.ManagedService {
	if m.filter == "" {
		return m.services
	}

	f := strings.ToLower(m.filter)
	var filtered []*service.ManagedService
	for _, s := range m.services {
		name := strings.ToLower(s.Def.Name)
		id := strings.ToLower(s.Def.ID)
		tags := strings.ToLower(strings.Join(s.Def.Tags, " "))
		if strings.Contains(name, f) || strings.Contains(id, f) || strings.Contains(tags, f) {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

func (m Model) sorted(svcs []*service.ManagedService) []*service.ManagedService {
	out := make([]*service.ManagedService, len(svcs))
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
