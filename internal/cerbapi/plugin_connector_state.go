package cerbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type pluginConnectorPersistedState struct {
	Entries []pluginConnectorPersistedEntry `json:"entries"`
}

type pluginConnectorPersistedEntry struct {
	PluginDir string               `json:"plugin_dir"`
	Options   PluginInstallOptions `json:"options"`
	Loaded    bool                 `json:"loaded"`
	// LegacyTrust is the "trust" object written before P0-4. Only its
	// dev_mode is honored; the signing fields it may carry are ignored. It
	// is folded into Options on read and never written back.
	LegacyTrust *legacyInstallOptions `json:"trust,omitempty"`
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

func readPluginConnectorState(path string) (pluginConnectorPersistedState, error) {
	if path == "" {
		return pluginConnectorPersistedState{}, nil
	}
	data, err := os.ReadFile(path)
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
	return state, nil
}

func writePluginConnectorState(path string, state pluginConnectorPersistedState) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create plugin connector state dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode plugin connector state: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write plugin connector state: %w", err)
	}
	return nil
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
	// the host with it.
	for _, entry := range state.Entries {
		entry.Options = mergeLegacyOptions(entry.Options, entry.LegacyTrust)
		entry.LegacyTrust = nil
		installed, err := service.install(entry.PluginDir, entry.Options)
		if err != nil {
			service.warnf("skipping plugin %q: %v\n  the registration is kept; reinstall or run `cerberus connectors plugin managed uninstall <id>` to drop it", entry.PluginDir, err)
			service.unrestored = append(service.unrestored, entry)
			continue
		}
		service.records[installed.ID] = entry
		if entry.Loaded {
			if err := service.manager.Load(ctx, installed.ID); err != nil {
				service.warnf("plugin %q installed but failed to load: %v", installed.ID, err)
			}
		}
	}
	return nil
}
