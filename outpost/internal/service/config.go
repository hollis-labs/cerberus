package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// Config is operator-owned machine configuration. Child environments are exact;
// plugins receive only their own configuration and state directories.
type Config struct {
	Address         string   `json:"address"`
	StateDir        string   `json:"state_dir"`
	CredentialsFile string   `json:"credentials_file"`
	Plugins         []Plugin `json:"plugins"`
}

type Plugin struct {
	Optional bool              `json:"optional,omitempty"`
	ID       string            `json:"id"`
	Command  string            `json:"command"`
	Args     []string          `json:"args"`
	Dir      string            `json:"dir"`
	Env      []string          `json:"env"`
	Config   map[string]string `json:"config"`
}

func ReadConfig(path string) (Config, error) {
	var cfg Config
	f, err := os.Open(path) //nolint:gosec // The local operator selects the inventory file; no remote input reaches this path.
	if err != nil {
		return cfg, err
	}
	defer func() { _ = f.Close() }()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return cfg, err
	}
	// A second document or trailing garbage usually means the operator edited
	// the wrong inventory; do not silently run with only its first fragment.
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return cfg, fmt.Errorf("configuration must contain exactly one JSON object")
	}
	if cfg.Address == "" {
		cfg.Address = "127.0.0.1:8977"
	}
	if !filepath.IsAbs(cfg.StateDir) || !filepath.IsAbs(cfg.CredentialsFile) {
		return cfg, fmt.Errorf("state_dir and credentials_file must be absolute paths")
	}
	seen := map[string]bool{}
	for _, p := range cfg.Plugins {
		if !identifier.MatchString(p.ID) || seen[p.ID] {
			return cfg, fmt.Errorf("invalid or duplicate plugin id %q", p.ID)
		}
		seen[p.ID] = true
		if !filepath.IsAbs(p.Command) || !filepath.IsAbs(p.Dir) {
			return cfg, fmt.Errorf("plugin %s: command and dir must be absolute paths", p.ID)
		}
	}
	if len(cfg.Plugins) == 0 {
		return cfg, fmt.Errorf("at least one plugin is required")
	}
	return cfg, nil
}
