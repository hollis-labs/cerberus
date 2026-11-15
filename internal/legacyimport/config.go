package legacyimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/settings"
)

// ConfigWriter places R13's output: the old config.yaml, events.yaml and connector-config.yaml
// keys as the new build's cerberus block. Where it goes waits on a lead ruling, so the
// transform is one thing and its output another.
type ConfigWriter interface {
	// Name is the output as the report names it.
	Name() string
	// Stage writes the fragment (YAML, a top-level "cerberus" block) under the staged data
	// directory, and returns its path relative to the data directory.
	Stage(stagedData string, fragment []byte) (rel string, err error)
}

// FragmentFile is the default ConfigWriter: <data>/migration/config-fragment.yaml (0600), for
// the operator to merge into the SUBSTRATE_CONFIG override file. Nothing reads it at start.
type FragmentFile struct{}

// Name implements ConfigWriter.
func (FragmentFile) Name() string { return "fragment file <data>/migration/config-fragment.yaml" }

// Stage implements ConfigWriter.
func (FragmentFile) Stage(stagedData string, fragment []byte) (string, error) {
	rel := filepath.Join("migration", "config-fragment.yaml")
	return rel, writePrivate(filepath.Join(stagedData, rel), fragment)
}

// Config keys: where each old key goes. Every key of the three files is in one of these, or the
// run refuses naming it.
var (
	// unmappedKeys have no new key yet; their values are reported.
	unmappedKeys = map[string]bool{"web.public_url": true, "build.install_after_build": true, "transfers.root": true}
	// listedKeys must be empty on the live tree (registered descriptors replaced them); a value is
	// reported, never carried.
	listedKeys = map[string]bool{"resources": true, "projects": true, "pipelines": true}
)

// oldEvents is events.yaml.
type oldEvents struct {
	Export struct {
		Enabled         bool `yaml:"enabled"`
		GraceSeconds    int  `yaml:"grace_seconds"`
		IntervalSeconds int  `yaml:"interval_seconds"`
		Hub             struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"hub"`
	} `yaml:"export"`
}

// transformConfig reads the three files and builds the cerberus block. An unknown key refuses
// the run; event export is carried with enabled forced off (plan section 5), the old value
// reported. The block is checked with the same parser the server's start uses.
func transformConfig(oldHome, home string, r *Report) (fragment []byte, rep *ConfigReport) {
	rep = &ConfigReport{Unmapped: []string{}, Mapped: []string{}}
	block := map[string]any{}

	if raw, ok := readYAML(filepath.Join(oldHome, "config.yaml"), "config.yaml", r); ok {
		top, isMap := raw.(map[string]any)
		if !isMap {
			r.refuse("config.yaml is not a YAML mapping")
		}
		keys := sortedKeys(top)
		for _, k := range keys {
			v := top[k]
			switch {
			case k == "version":
				if n, _ := v.(int); n != 2 {
					r.refuse("config.yaml version %v: the import reads version 2", v)
				}
			case k == "host_monitor":
				if v == nil {
					continue
				}
				var hm config.HostMonitorConfig
				if err := strictDecode(v, &hm); err != nil {
					r.refuse("config.yaml host_monitor: %v", err)
					continue
				}
				block["host_monitor"] = v
				rep.Mapped = append(rep.Mapped, "config.yaml host_monitor -> cerberus.host_monitor")
			case listedKeys[k]:
				list, _ := v.([]any)
				switch k {
				case "resources":
					rep.ResourcesListed = len(list)
				case "projects":
					rep.ProjectsListed = len(list)
				case "pipelines":
					rep.PipelinesListed = len(list)
				}
				if len(list) > 0 {
					r.warn("config.yaml still lists %d %s; they are not carried (descriptors in the registry replace them), and the owner decides", len(list), k)
				}
			case k == "web" || k == "build" || k == "transfers":
				sub, isSub := v.(map[string]any)
				if v != nil && !isSub {
					r.refuse("config.yaml %s is not a mapping", k)
					continue
				}
				for _, sk := range sortedKeys(sub) {
					full := k + "." + sk
					if !unmappedKeys[full] {
						r.refuse("config.yaml has the unknown key %s", full)
						continue
					}
					rep.Unmapped = append(rep.Unmapped, fmt.Sprintf("%s = %v", full, sub[sk]))
				}
			default:
				r.refuse("config.yaml has the unknown key %s", k)
			}
		}
	}

	if raw, ok := readYAML(filepath.Join(oldHome, "events.yaml"), "events.yaml", r); ok {
		var ev oldEvents
		if err := strictDecode(raw, &ev); err != nil {
			r.refuse("events.yaml: %v", err)
		} else {
			// The old reader's defaults, written out so the new build behaves the same once enabled.
			if ev.Export.GraceSeconds <= 0 {
				ev.Export.GraceSeconds = 60
			}
			if ev.Export.IntervalSeconds <= 0 {
				ev.Export.IntervalSeconds = 5
			}
			rep.EventExportWasEnabled = ev.Export.Enabled
			block["event_export"] = map[string]any{"enabled": false, "grace_seconds": ev.Export.GraceSeconds,
				"interval_seconds": ev.Export.IntervalSeconds, "hub": ev.Export.Hub.Enabled}
			rep.Mapped = append(rep.Mapped, fmt.Sprintf("events.yaml export -> cerberus.event_export (enabled forced false; was %v)", ev.Export.Enabled))
		}
	}

	if raw, ok := readYAML(filepath.Join(oldHome, "connector-config.yaml"), "connector-config.yaml", r); ok && raw != nil {
		if _, isMap := raw.(map[string]any); !isMap {
			r.refuse("connector-config.yaml is not a mapping of plugin id to settings")
		} else {
			block["connectors"] = raw
			rep.Mapped = append(rep.Mapped, "connector-config.yaml -> cerberus.connectors")
		}
	}

	// The block must be one the server's start accepts.
	if _, err := settings.Parse(block, home); err != nil {
		r.refuse("the transformed config does not parse as the cerberus block: %v", err)
		return nil, rep
	}
	data, err := yaml.Marshal(map[string]any{settings.Key: block})
	if err != nil {
		r.refuse("encode the config fragment: %v", err)
		return nil, rep
	}
	header := []byte("# Written by `cerberus migrate-from` from the old config.yaml, events.yaml and connector-config.yaml.\n" +
		"# Merge the cerberus block into the SUBSTRATE_CONFIG override file; event export stays off until you enable it.\n")
	fragment = append(header, data...)
	sum := sha256.Sum256(fragment)
	rep.FragmentSHA256 = "sha256:" + hex.EncodeToString(sum[:])
	return fragment, rep
}

// readYAML reads an optional YAML file of the snapshot. Absent: listed, ok false.
func readYAML(p, rel string, r *Report) (any, bool) {
	data, err := os.ReadFile(p) //nolint:gosec // inside the snapshot
	if errors.Is(err, os.ErrNotExist) {
		r.Listed.Absent = append(r.Listed.Absent, rel)
		return nil, false
	}
	if err != nil {
		r.refuse("%s: %v", rel, err)
		return nil, false
	}
	var v any
	if err = yaml.Unmarshal(data, &v); err != nil {
		r.refuse("%s does not parse: %v", rel, err)
		return nil, false
	}
	return v, true
}

func strictDecode(v any, dst any) error {
	raw, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	// An empty document (a key with no value) decodes to nothing.
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
