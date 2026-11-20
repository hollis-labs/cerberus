package app

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/registry"
)

// FleetOptions configures the fleet inventory for the config at configPath.
//
//   - CERBERUS_FLEET_REPO_ROOTS: colon-separated directories searched for local
//     checkouts, to compute how far each build is behind its default branch
//     (default ~/dev).
//   - CERBERUS_FLEET_BIN_DIRS: the local host's installed-binary directories when
//     its hosts.yaml entry has no fleet.bin_dirs label (default ~/.local/bin).
func FleetOptions(configPath string) cerbapi.FleetOptions {
	home, _ := os.UserHomeDir()
	return cerbapi.FleetOptions{
		Resources: func() []config.ResourceDef {
			cfg, err := registry.ResolveConfig(configPath)
			if err != nil {
				return nil
			}
			return cfg.Resources
		},
		RepoRoots:    envDirs("CERBERUS_FLEET_REPO_ROOTS", filepath.Join(home, "dev")),
		LocalBinDirs: envDirs("CERBERUS_FLEET_BIN_DIRS", filepath.Join(home, ".local", "bin")),
	}
}

func envDirs(name, def string) []string {
	v := os.Getenv(name)
	if v == "" {
		if def == "" {
			return nil
		}
		return []string{def}
	}
	var out []string
	for _, d := range strings.Split(v, ":") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}
