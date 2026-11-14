// Package settings reads the app's own "cerberus" block from the manifest config. The shell
// modules read theirs; this block holds what only Cerberus has.
//
//	cerberus:
//	  install_root: "~/.cerberus/apps"   # where artifact installs and resource logs live
//
// install_root is adopted in place from the old build: the live resource units point their
// ExecStart and log paths into it, so moving it would rewrite and restart every unit
// (docs/substrate-port-plan.md, section 3). A leading "~/" is the user's home directory.
//
// An unknown key or a bad value is refused at start, naming the key.
package settings

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Key is the name of the config block.
const Key = "cerberus"

// DefaultInstallRoot is install_root when the block does not set it.
const DefaultInstallRoot = "~/.cerberus/apps"

// Settings are the parsed block.
type Settings struct {
	// InstallRoot is absolute.
	InstallRoot string
}

// Parse reads the block (nil for a missing one). home expands a leading "~/".
func Parse(raw any, home string) (Settings, error) {
	var s Settings
	block := map[string]any{}
	if raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return s, fmt.Errorf("%s: the config block must be a map", Key)
		}
		block = m
	}
	known := map[string]bool{"install_root": true}
	var unknown []string
	for k := range block {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return s, fmt.Errorf("%s: unknown config key(s): %s", Key, strings.Join(unknown, ", "))
	}

	root := DefaultInstallRoot
	if v, present := block["install_root"]; present && v != nil {
		t, ok := v.(string)
		if !ok {
			return s, fmt.Errorf("%s.install_root must be a string", Key)
		}
		if root = strings.TrimSpace(t); root == "" {
			return s, fmt.Errorf("%s.install_root must not be empty", Key)
		}
	}
	abs, err := expand(root, home)
	if err != nil {
		return s, fmt.Errorf("%s.install_root: %w", Key, err)
	}
	s.InstallRoot = abs
	return s, nil
}

func expand(p, home string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home == "" {
			return "", fmt.Errorf("%q starts with ~ but the home directory is unknown", p)
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%q must be absolute or start with ~/", p)
	}
	return filepath.Clean(p), nil
}
