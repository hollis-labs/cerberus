package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hollis-labs/cerberus/internal/pluginhost"
)

// plugin-connectors.json is shared with the old build (plan section 3): the new build reads it
// in place and writes it in the old format, with the old lock (<path>.lock, flock) and an
// atomic rename of a 0600 file, so either build reads what the other wrote. Only one of them
// runs plugins at a time.

// pluginStateVersion is the format of plugin-connectors.json written since install review.
// Version 1 (no version field) entries still load, as review_pending.
const pluginStateVersion = 2

// InstallOptions are how a plugin was installed. DevMode selects a development install, which
// is a restriction (pluginhost.OriginDev); there is no trust option.
type InstallOptions struct {
	DevMode bool `json:"dev_mode,omitempty"`
}

// legacyInstallOptions is the "trust" object older state files stored; only dev_mode is read.
type legacyInstallOptions struct {
	DevMode bool `json:"dev_mode,omitempty"`
}

func mergeLegacyOptions(options InstallOptions, legacy *legacyInstallOptions) InstallOptions {
	if legacy != nil && legacy.DevMode {
		options.DevMode = true
	}
	return options
}

type persistedState struct {
	Version int              `json:"version,omitempty"`
	Entries []persistedEntry `json:"entries"`
}

type persistedEntry struct {
	// ID is the plugin id; absent in version 1 entries, whose id is read from plugin.yaml.
	ID string `json:"id,omitempty"`
	// PluginDir is where the plugin runs from: its reviewed copy in the plugin store, a
	// development install's source, or a pending version 1 entry's install directory.
	PluginDir string `json:"plugin_dir"`
	// Source is the directory the reviewed bundle was installed from.
	Source  string         `json:"source,omitempty"`
	Options InstallOptions `json:"options"`
	Loaded  bool           `json:"loaded"`
	// BundleDigest is the accepted bundle digest; load refuses a bundle that does not match.
	BundleDigest string `json:"bundle_digest,omitempty"`
	// Review is the review the operator accepted; nil while review is pending.
	Review     *pluginhost.Review `json:"review,omitempty"`
	AcceptedAt time.Time          `json:"accepted_at,omitzero"`
	// LegacyTrust is folded into Options on read and never written back.
	LegacyTrust *legacyInstallOptions `json:"trust,omitempty"`
}

func (e persistedEntry) reviewPending() bool {
	return e.Review == nil || e.BundleDigest == ""
}

func readPluginState(path string) (persistedState, error) {
	if path == "" {
		return persistedState{}, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // the adopted root's own state file
	if err != nil {
		if os.IsNotExist(err) {
			return persistedState{}, nil
		}
		return persistedState{}, fmt.Errorf("read plugin connector state: %w", err)
	}
	var state persistedState
	if len(data) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return persistedState{}, fmt.Errorf("decode plugin connector state: %w", err)
	}
	if state.Version > pluginStateVersion {
		return persistedState{}, fmt.Errorf("plugin connector state %s is version %d, newer than this Cerberus understands (%d); upgrade Cerberus", path, state.Version, pluginStateVersion)
	}
	for i := range state.Entries {
		state.Entries[i].Options = mergeLegacyOptions(state.Entries[i].Options, state.Entries[i].LegacyTrust)
		state.Entries[i].LegacyTrust = nil
	}
	return state, nil
}

func writePluginState(path string, state persistedState) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create plugin connector state dir: %w", err)
	}
	state.Version = pluginStateVersion
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode plugin connector state: %w", err)
	}
	// Written whole and renamed into place, so a reader never sees half a file and a crash
	// leaves the previous state.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write plugin connector state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("write plugin connector state: %w", err)
	}
	return nil
}

// updatePluginState is a read-modify-write of the state file under its exclusive lock: each
// writer changes only what it owns and keeps the rest.
func updatePluginState(path string, fn func(*persistedState) error) error {
	if path == "" {
		return nil
	}
	unlock, err := lockPluginState(path)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := readPluginState(path)
	if err != nil {
		return err
	}
	if err := fn(&state); err != nil {
		return err
	}
	return writePluginState(path, state)
}

func lockPluginState(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create plugin connector state dir: %w", err)
	}
	f, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // the state file's own lock
	if err != nil {
		return nil, fmt.Errorf("lock plugin connector state: %w", err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX) //nolint:gosec // fd fits in int
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock plugin connector state: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // as above
		_ = f.Close()
	}, nil
}

// findEntry is the index of id's entry, matching a version 1 entry (no id) by its plugin.yaml.
func (s *persistedState) findEntry(id string) int {
	for i, e := range s.Entries {
		if e.ID == id {
			return i
		}
	}
	for i, e := range s.Entries {
		if e.ID != "" {
			continue
		}
		if spec, err := pluginhost.ReadPluginYAML(e.PluginDir); err == nil && spec.ID == id {
			return i
		}
	}
	return -1
}
