package eventexport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Settings are the exporter's switches. Export is off unless a settings file
// turns it on, so shipping this code changes nothing until an operator opts in.
type Settings struct {
	Export ExportSettings `yaml:"export"`
}

// ExportSettings configures the exporter.
type ExportSettings struct {
	Enabled bool `yaml:"enabled"`
	// GraceSeconds is how long an approval event may wait for its audit record
	// before cerberus.audit_gap is raised. Default 60.
	GraceSeconds int `yaml:"grace_seconds"`
	// IntervalSeconds is how often the exporter steps. Default 5.
	IntervalSeconds int `yaml:"interval_seconds"`
	// Hub turns on the relay that ships the outbox to the event hub. It does
	// nothing unless Enabled is also set, and needs HOLLIS_EVENTS_URL (and the
	// app's token file, see mesh/eventbus) in the daemon's environment.
	Hub HubSettings `yaml:"hub"`
}

// HubSettings configures the relay to the event hub. Off by default.
type HubSettings struct {
	Enabled bool `yaml:"enabled"`
}

// DefaultSettingsPath is ~/.cerberus/events.yaml.
func DefaultSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cerberus", "events.yaml"), nil
}

// LoadSettings reads the settings file. A missing file means the defaults:
// disabled.
func LoadSettings(path string) (Settings, error) {
	s := Settings{Export: ExportSettings{GraceSeconds: 60, IntervalSeconds: 5}}
	data, err := os.ReadFile(path) //nolint:gosec // the operator's own settings file
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("eventexport: read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Settings{}, fmt.Errorf("eventexport: parse %s: %w", path, err)
	}
	if s.Export.GraceSeconds <= 0 {
		s.Export.GraceSeconds = 60
	}
	if s.Export.IntervalSeconds <= 0 {
		s.Export.IntervalSeconds = 5
	}
	return s, nil
}

// Grace is the grace window as a duration.
func (e ExportSettings) Grace() time.Duration { return time.Duration(e.GraceSeconds) * time.Second }

// Interval is the step interval as a duration.
func (e ExportSettings) Interval() time.Duration {
	return time.Duration(e.IntervalSeconds) * time.Second
}
