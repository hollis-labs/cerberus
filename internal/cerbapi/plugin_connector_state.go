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
	PluginDir string                      `json:"plugin_dir"`
	Trust     PluginConnectorTrustOptions `json:"trust"`
	Loaded    bool                        `json:"loaded"`
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
	for _, entry := range state.Entries {
		installed, err := service.install(entry.PluginDir, entry.Trust)
		if err != nil {
			return fmt.Errorf("restore plugin %q: %w", entry.PluginDir, err)
		}
		service.records[installed.ID] = entry
		if entry.Loaded {
			if err := service.manager.Load(ctx, installed.ID); err != nil {
				return fmt.Errorf("restore load %q: %w", installed.ID, err)
			}
		}
	}
	return nil
}
