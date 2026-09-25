package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hollis-labs/cerberus/internal/pluginhost"
)

// pluginStateVersion is the format of plugin-connectors.json written since
// install review. Version 1 (no version field) held pointers at plugin
// directories and no review; those entries still load, as review_pending.
const pluginStateVersion = 2

type pluginConnectorPersistedState struct {
	Version int                             `json:"version,omitempty"`
	Entries []pluginConnectorPersistedEntry `json:"entries"`
}

type pluginConnectorPersistedEntry struct {
	// ID is the plugin id. Absent in version 1 entries, whose id is read from
	// the plugin.yaml at PluginDir.
	ID string `json:"id,omitempty"`
	// PluginDir is where the plugin runs from: its reviewed copy in the
	// plugin store, a development install's source directory, or, for a
	// version 1 entry still pending review, the directory it was installed
	// from.
	PluginDir string `json:"plugin_dir"`
	// Source is the directory the reviewed bundle was installed from.
	Source  string               `json:"source,omitempty"`
	Options PluginInstallOptions `json:"options"`
	Loaded  bool                 `json:"loaded"`
	// BundleDigest is the accepted bundle digest; load refuses a bundle that
	// does not match it. Empty while review is pending.
	BundleDigest string `json:"bundle_digest,omitempty"`
	// Review is the review the operator accepted: what a later change is
	// shown as a diff against. Nil while review is pending.
	Review     *pluginhost.Review `json:"review,omitempty"`
	AcceptedAt time.Time          `json:"accepted_at,omitzero"`
	// LegacyTrust is the "trust" object written before P0-4. Only its
	// dev_mode is honored; the signing fields it may carry are ignored. It
	// is folded into Options on read and never written back.
	LegacyTrust *legacyInstallOptions `json:"trust,omitempty"`
}

// reviewPending reports an entry installed before install review.
func (e pluginConnectorPersistedEntry) reviewPending() bool {
	return e.Review == nil || e.BundleDigest == ""
}

func PluginConnectorStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".cerberus")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	return filepath.Join(dir, "plugin-connectors.json"), nil
}

// PluginStorePath is ~/.cerberus/plugins, where reviewed bundles live.
func PluginStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cerberus", "plugins"), nil
}

func readPluginConnectorState(path string) (pluginConnectorPersistedState, error) {
	if path == "" {
		return pluginConnectorPersistedState{}, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // the operator's own state file
	if err != nil {
		if os.IsNotExist(err) {
			return pluginConnectorPersistedState{}, nil
		}
		return pluginConnectorPersistedState{}, fmt.Errorf("read plugin connector state: %w", err)
	}
	var state pluginConnectorPersistedState
	if len(data) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return pluginConnectorPersistedState{}, fmt.Errorf("decode plugin connector state: %w", err)
	}
	if state.Version > pluginStateVersion {
		return pluginConnectorPersistedState{}, fmt.Errorf("plugin connector state %s is version %d, newer than this Cerberus understands (%d); upgrade Cerberus", path, state.Version, pluginStateVersion)
	}
	for i := range state.Entries {
		state.Entries[i].Options = mergeLegacyOptions(state.Entries[i].Options, state.Entries[i].LegacyTrust)
		state.Entries[i].LegacyTrust = nil
	}
	return state, nil
}

func writePluginConnectorState(path string, state pluginConnectorPersistedState) error {
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
	// Written whole and renamed into place, so a reader never sees half a
	// file and a crash leaves the previous state.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil { //nolint:gosec // the operator's own state file
		return fmt.Errorf("write plugin connector state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil { //nolint:gosec // the operator's own state file
		return fmt.Errorf("write plugin connector state: %w", err)
	}
	return nil
}

// updatePluginConnectorState is a read-modify-write of the state file under
// an exclusive lock. The daemon and an in-process install both write the file
// now, so neither may write a snapshot of its own view: each changes only
// what it owns and keeps the rest.
func updatePluginConnectorState(path string, fn func(*pluginConnectorPersistedState) error) error {
	if path == "" {
		return nil
	}
	unlock, err := lockPluginConnectorState(path)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := readPluginConnectorState(path)
	if err != nil {
		return err
	}
	if err := fn(&state); err != nil {
		return err
	}
	return writePluginConnectorState(path, state)
}

func lockPluginConnectorState(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { //nolint:gosec // the operator's own state directory
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

// findEntry returns the index of the entry for id, matching a version 1
// entry (no id) by the id its plugin.yaml declares.
func (s *pluginConnectorPersistedState) findEntry(id string) int {
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

func restoreManagedPlugins(ctx context.Context, service *ManagedPluginConnectorService, path string) error {
	state, err := readPluginConnectorState(path)
	if err != nil {
		return err
	}
	// A plugin that cannot be restored is skipped, never fatal. Returning an
	// error here took the whole daemon down: the launchd job failed to start and
	// KeepAlive crash-looped, so deleting a plugin directory — `make clean` in a
	// plugin repo, or a fresh clone with a gitignored dist/ — bricked Cerberus
	// entirely. A plugin is optional by definition and must not be able to take
	// the host with it. The entry stays in the file either way: the daemon
	// only ever updates what it owns.
	for _, entry := range state.Entries {
		installed, err := service.register(entry)
		if err != nil {
			service.warnf("skipping plugin %q: %v\n  the registration is kept; reinstall or run `cerberus connectors plugin managed uninstall <id>` to drop it", entry.PluginDir, err)
			continue
		}
		if installed.ReviewPending {
			service.warnf("plugin %q was installed before install review and loads unchecked until reviewed; run `cerberus connectors plugin managed review %s` in a terminal", installed.ID, installed.ID)
		}
		if entry.Loaded {
			if err := service.manager.Load(ctx, installed.ID); err != nil {
				service.warnf("plugin %q installed but failed to load: %v", installed.ID, err)
			}
		}
	}
	return nil
}
