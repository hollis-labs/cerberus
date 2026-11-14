// Package settings reads the app's own "cerberus" block from the manifest config. The shell
// modules read theirs; this block holds what only Cerberus has.
//
//	cerberus:
//	  install_root: "~/.cerberus/apps"   # where artifact installs and resource logs live
//	  adopt_root: "~/.cerberus"          # the old state tree files adopted in place live under
//	  mode: observe                      # observe (read and report only) or supervise
//	  self_resource_id: ""               # the resource this process runs as, checked at start
//
// install_root is adopted in place from the old build: the live resource units point their
// ExecStart and log paths into it, so moving it would rewrite and restart every unit
// (docs/substrate-port-plan.md, section 3). adopt_root holds the files kept in place in the
// old format because other programs read them directly: today the registry index
// registry.yaml, which tether sysop reads. A leading "~/" is the user's home directory.
//
// mode is observe while the old build still supervises (plan section 7.2 C): the new build reads
// descriptors and unit states and refuses every mutation. supervise arrives with slice 5.
// self_resource_id names the resource the old build runs this process as (cerberus-substrate);
// the start checks it against the process's own systemd unit.
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

// DefaultAdoptRoot is adopt_root when the block does not set it.
const DefaultAdoptRoot = "~/.cerberus"

// Mode is how the build treats the workloads it knows.
type Mode string

const (
	// ModeObserve reads and reports, and refuses every mutation.
	ModeObserve Mode = "observe"
	// ModeSupervise is full supervision (slice 5).
	ModeSupervise Mode = "supervise"
)

// Settings are the parsed block.
type Settings struct {
	// InstallRoot is absolute.
	InstallRoot string
	// AdoptRoot is absolute.
	AdoptRoot string
	// Mode is observe unless the block says supervise.
	Mode Mode
	// SelfResourceID is empty unless set.
	SelfResourceID string
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
	known := map[string]bool{"install_root": true, "adopt_root": true, "mode": true, "self_resource_id": true}
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

	var err error
	if s.InstallRoot, err = path(block, "install_root", DefaultInstallRoot, home); err != nil {
		return s, err
	}
	if s.AdoptRoot, err = path(block, "adopt_root", DefaultAdoptRoot, home); err != nil {
		return s, err
	}
	mode, err := str(block, "mode")
	if err != nil {
		return s, err
	}
	switch Mode(mode) {
	case "", ModeObserve:
		s.Mode = ModeObserve
	case ModeSupervise:
		s.Mode = ModeSupervise
	default:
		return s, fmt.Errorf("%s.mode must be observe or supervise, got %q", Key, mode)
	}
	if s.SelfResourceID, err = str(block, "self_resource_id"); err != nil {
		return s, err
	}
	return s, nil
}

// str reads block[key] as a trimmed string, "" when unset.
func str(block map[string]any, key string) (string, error) {
	v, present := block[key]
	if !present || v == nil {
		return "", nil
	}
	t, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s.%s must be a string", Key, key)
	}
	return strings.TrimSpace(t), nil
}

// path reads block[key] as an absolute or "~/" path, def when unset.
func path(block map[string]any, key, def, home string) (string, error) {
	p := def
	if v, present := block[key]; present && v != nil {
		t, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("%s.%s must be a string", Key, key)
		}
		if p = strings.TrimSpace(t); p == "" {
			return "", fmt.Errorf("%s.%s must not be empty", Key, key)
		}
	}
	abs, err := expand(p, home)
	if err != nil {
		return "", fmt.Errorf("%s.%s: %w", Key, key, err)
	}
	return abs, nil
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
