// Package settings reads the app's own "cerberus" block from the manifest config. The shell
// modules read theirs; this block holds what only Cerberus has.
//
//	cerberus:
//	  install_root: "~/.cerberus/apps"   # where artifact installs and resource logs live
//	  adopt_root: "~/.cerberus"          # the old state tree files adopted in place live under
//	  mode: observe                      # observe (read and report only) or supervise
//	  self_resource_id: ""               # the resource this process runs as, checked at start
//	  cli_key_file: "~/.config/cerberus/cli.key"   # the API key the cerberus CLI presents
//	  host_monitor: {enabled: false, ...}  # the old config.yaml host_monitor block, same keys
//	  scheduler: {enabled: false, allow_existing: false}
//	  event_export: {enabled: false, grace_seconds: 0, interval_seconds: 0, hub: false}
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
// host_monitor, scheduler and event_export run only in supervise mode, and only when enabled.
// The scheduler and event export are off by default (plan section 4.2); scheduler.allow_existing
// lets an enabled scheduler start over schedule tables that already hold rows.
//
// connectors is the old connector-config.yaml as config keys, the same shape: each managed
// plugin's declared fields, its MCP exposure and its limits (supervise mode, slice 7).
//
// An unknown key or a bad value is refused at start, naming the key.
package settings

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
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

// DefaultCLIKeyFile is cli_key_file when the block does not set it: under the new build's config
// directory, created offline with `cerberus identity key create --out` (mode 0600).
const DefaultCLIKeyFile = "~/.config/cerberus/cli.key"

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
	// CLIKeyFile is absolute.
	CLIKeyFile string
	// HostMonitor is the host_monitor block as the old config.yaml held it; nil when unset
	// (the defaults, which leave monitoring off).
	HostMonitor *config.HostMonitorConfig
	// Scheduler gates the scheduling core; off unless enabled.
	Scheduler Scheduler
	// EventExport gates event export; off unless enabled.
	EventExport EventExport
	// Connectors is the managed plugins' settings, the old connector-config.yaml: never nil
	// entries; SHA256 fingerprints the block as YAML, empty when unset.
	Connectors pluginhost.ConnectorConfig
}

// Scheduler is the scheduler block.
type Scheduler struct {
	Enabled bool `yaml:"enabled"`
	// AllowExisting lets an enabled scheduler start while its tables hold rows.
	AllowExisting bool `yaml:"allow_existing"`
}

// EventExport is the event_export block: the old events.yaml export keys.
type EventExport struct {
	Enabled         bool `yaml:"enabled"`
	GraceSeconds    int  `yaml:"grace_seconds"`
	IntervalSeconds int  `yaml:"interval_seconds"`
	// Hub relays the outbox to the events hub as well.
	Hub bool `yaml:"hub"`
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
	known := map[string]bool{"install_root": true, "adopt_root": true, "mode": true, "self_resource_id": true, "cli_key_file": true,
		"host_monitor": true, "scheduler": true, "event_export": true, "connectors": true}
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
	if s.CLIKeyFile, err = path(block, "cli_key_file", DefaultCLIKeyFile, home); err != nil {
		return s, err
	}
	if v, ok := block["host_monitor"]; ok && v != nil {
		s.HostMonitor = &config.HostMonitorConfig{}
		if err := strict(v, "host_monitor", s.HostMonitor); err != nil {
			return s, err
		}
	}
	if err := strict(block["scheduler"], "scheduler", &s.Scheduler); err != nil {
		return s, err
	}
	if err := strict(block["event_export"], "event_export", &s.EventExport); err != nil {
		return s, err
	}
	if s.EventExport.GraceSeconds < 0 || s.EventExport.IntervalSeconds < 0 {
		return s, fmt.Errorf("%s.event_export: grace_seconds and interval_seconds must not be negative", Key)
	}
	if s.Connectors, err = connectors(block["connectors"]); err != nil {
		return s, err
	}
	return s, nil
}

// connectors reads the connectors block: plugin id to its settings, unknown keys refused.
func connectors(v any) (pluginhost.ConnectorConfig, error) {
	cfg := pluginhost.ConnectorConfig{Path: Key + ".connectors", Entries: map[string]pluginhost.PluginSettings{}}
	if v == nil {
		return cfg, nil
	}
	if _, ok := v.(map[string]any); !ok {
		return cfg, fmt.Errorf("%s.connectors must be a map of plugin id to its settings", Key)
	}
	if err := strict(v, "connectors", &cfg.Entries); err != nil {
		return cfg, err
	}
	if cfg.Entries == nil {
		cfg.Entries = map[string]pluginhost.PluginSettings{}
	}
	raw, err := yaml.Marshal(v)
	if err != nil {
		return cfg, fmt.Errorf("%s.connectors: %w", Key, err)
	}
	sum := sha256.Sum256(raw)
	cfg.SHA256 = hex.EncodeToString(sum[:])
	return cfg, nil
}

// strict decodes a sub-block into dst, refusing unknown keys and wrong types, naming the block.
// A missing block leaves dst as it is.
func strict(v any, name string, dst any) error {
	if v == nil {
		return nil
	}
	raw, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("%s.%s: %w", Key, name, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%s.%s: %w", Key, name, err)
	}
	return nil
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
