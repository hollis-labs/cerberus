package hosts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Reachability of a host as last probed.
const (
	StateOnline  = "online"
	StateOffline = "offline"
	StateUnknown = "unknown"
)

// DefaultStaleAfter is how old a probe may be before it reports stale.
const DefaultStaleAfter = 5 * time.Minute

// State is a host's derived record. It is rebuilt by probing and may be
// deleted at any time.
type State struct {
	State           string     `json:"state"`
	OnboardingStage string     `json:"onboarding_stage,omitempty"`
	LastSeen        *time.Time `json:"last_seen,omitempty"`
	// ProbedAt is when Probed was measured; CheckedAt is the last attempt,
	// which may have found the host offline and left Probed as it was.
	ProbedAt  *time.Time `json:"probed_at,omitempty"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	Probed    *Probed    `json:"probed,omitempty"`
	// Reason says why the host is offline or unknown, in Cerberus's words.
	Reason string `json:"reason,omitempty"`
}

// Store keeps derived host state in one JSON file. It is safe for concurrent
// use within a process; the file is replaced atomically.
type Store struct {
	path string
	mu   sync.Mutex
	mem  map[string]State
}

// NewStore returns a store at path ("" keeps state in memory only).
func NewStore(path string) *Store { return &Store{path: path, mem: map[string]State{}} }

type storeFile struct {
	Version int              `json:"version"`
	Hosts   map[string]State `json:"hosts"`
}

func (s *Store) read() (map[string]State, error) {
	if s.path == "" {
		return s.mem, nil
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]State{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f storeFile
	// Derived data: a corrupt file reads as empty and is rebuilt by probing.
	_ = json.Unmarshal(data, &f)
	if f.Hosts == nil {
		f.Hosts = map[string]State{}
	}
	return f.Hosts, nil
}

// Get returns a host's recorded state, or the zero unknown state.
func (s *Store) Get(name string) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, _ := s.read()
	if st, ok := m[name]; ok {
		return st
	}
	return State{State: StateUnknown}
}

// Put records a host's state.
func (s *Store) Put(name string, st State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return err
	}
	m[name] = st
	if s.path == "" {
		return nil
	}
	return s.write(m)
}

// Forget drops state for hosts no longer declared.
func (s *Store) Forget(keep map[string]bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return err
	}
	changed := false
	for name := range m {
		if !keep[name] {
			delete(m, name)
			changed = true
		}
	}
	if !changed || s.path == "" {
		return nil
	}
	return s.write(m)
}

func (s *Store) write(m map[string]State) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(storeFile{Version: 1, Hosts: m}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".hosts-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("write host state: %w", err)
	}
	return nil
}
