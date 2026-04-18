package config

import (
	"fmt"
	"sync"
)

// Source returns a live snapshot of service definitions.
//
// Every call to Snapshot() re-parses the underlying storage (typically
// ~/.cerberus/config.yaml). This keeps the daemon and MCP handlers from
// caching stale config: a single place owns the "read fresh from disk"
// discipline, and every lifecycle entry point goes through it.
type Source interface {
	Snapshot() (*Config, error)
	Path() string
}

// FileSource reads the Cerberus config from a YAML file on each Snapshot()
// call. The file is small (<10KB in practice); re-parsing per lifecycle op
// costs microseconds and removes an entire class of "daemon had stale config"
// bugs.
type FileSource struct {
	path string
}

// NewFileSource constructs a Source that reads from the given path.
func NewFileSource(path string) *FileSource {
	return &FileSource{path: path}
}

// Snapshot re-reads and parses the config file from disk. Callers MUST NOT
// cache the returned *Config across lifecycle operations — re-call Snapshot()
// each time.
func (f *FileSource) Snapshot() (*Config, error) {
	if f.path == "" {
		return nil, fmt.Errorf("config source has no path configured")
	}
	cfg, err := Load(f.path)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", f.path, err)
	}
	return cfg, nil
}

// Path returns the filesystem path this source reads from.
func (f *FileSource) Path() string { return f.path }

// StaticSource is an in-memory Source used by tests. It always returns the
// same *Config (callers can swap the pointer via Set to simulate a reload).
type StaticSource struct {
	mu  sync.RWMutex
	cfg *Config
	err error
}

// NewStaticSource constructs a fake Source for testing.
func NewStaticSource(cfg *Config) *StaticSource {
	return &StaticSource{cfg: cfg}
}

// Set atomically swaps the snapshot this source returns.
func (s *StaticSource) Set(cfg *Config) {
	s.mu.Lock()
	s.cfg = cfg
	s.err = nil
	s.mu.Unlock()
}

// SetError forces Snapshot() to return the given error on the next call.
func (s *StaticSource) SetError(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// Snapshot returns the stored *Config (deep-copied to prevent callers from
// mutating the test fixture). Matches the contract of FileSource.
func (s *StaticSource) Snapshot() (*Config, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.err != nil {
		return nil, s.err
	}
	if s.cfg == nil {
		return nil, fmt.Errorf("static source has no config")
	}
	return cloneConfig(s.cfg), nil
}

// Path returns a synthetic path for static sources (tests occasionally log it).
func (s *StaticSource) Path() string { return "static://in-memory" }

// cloneConfig deep-copies a Config so test fixtures aren't shared by
// reference across Snapshot() callers.
func cloneConfig(src *Config) *Config {
	if src == nil {
		return nil
	}
	out := &Config{Version: src.Version}
	if src.Services == nil {
		return out
	}
	out.Services = make([]ServiceDef, len(src.Services))
	for i, svc := range src.Services {
		clone := svc
		// Slices + maps need independent backing storage.
		if svc.Command != nil {
			clone.Command = append([]string(nil), svc.Command...)
		}
		if svc.Tags != nil {
			clone.Tags = append([]string(nil), svc.Tags...)
		}
		if svc.Build != nil {
			clone.Build = append([]string(nil), svc.Build...)
		}
		if svc.DependsOn != nil {
			clone.DependsOn = append([]string(nil), svc.DependsOn...)
		}
		if svc.Profiles != nil {
			clone.Profiles = append([]string(nil), svc.Profiles...)
		}
		if svc.HealthCheckCfg.Command != nil {
			clone.HealthCheckCfg.Command = append([]string(nil), svc.HealthCheckCfg.Command...)
		}
		if svc.Env != nil {
			clone.Env = make(map[string]string, len(svc.Env))
			for k, v := range svc.Env {
				clone.Env[k] = v
			}
		}
		out.Services[i] = clone
	}
	return out
}
