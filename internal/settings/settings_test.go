package settings

import (
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	for _, raw := range []any{nil, map[string]any{}, map[string]any{"install_root": nil}} {
		s, err := Parse(raw, "/u/op")
		if err != nil {
			t.Fatal(err)
		}
		if s.InstallRoot != "/u/op/.cerberus/apps" || s.AdoptRoot != "/u/op/.cerberus" || s.CLIKeyFile != "/u/op/.config/cerberus/cli.key" {
			t.Fatalf("install_root = %q, adopt_root = %q", s.InstallRoot, s.AdoptRoot)
		}
	}
}

func TestInstallRoot(t *testing.T) {
	for in, want := range map[string]string{
		"~/apps":         "/u/op/apps",
		"~":              "/u/op",
		"/srv/apps/":     "/srv/apps",
		"  /srv/apps  ":  "/srv/apps",
		"/srv/../x/apps": "/x/apps",
	} {
		s, err := Parse(map[string]any{"install_root": in}, "/u/op")
		if err != nil || s.InstallRoot != want {
			t.Fatalf("Parse(%q) = %q, %v; want %q", in, s.InstallRoot, err, want)
		}
	}
}

func TestAdoptRoot(t *testing.T) {
	s, err := Parse(map[string]any{"adopt_root": "~/state/"}, "/u/op")
	if err != nil || s.AdoptRoot != "/u/op/state" {
		t.Fatalf("adopt_root = %q, %v", s.AdoptRoot, err)
	}
}

func TestRefusalsNameTheKey(t *testing.T) {
	for _, c := range []struct {
		raw  any
		home string
		want string
	}{
		{"not a map", "/h", "cerberus: the config block must be a map"},
		{map[string]any{"install_rot": "/x", "zz": 1}, "/h", "unknown config key(s): install_rot, zz"},
		{map[string]any{"install_root": 3.0}, "/h", "cerberus.install_root must be a string"},
		{map[string]any{"install_root": " "}, "/h", "cerberus.install_root must not be empty"},
		{map[string]any{"install_root": "apps"}, "/h", "cerberus.install_root"},
		{map[string]any{"install_root": "~/apps"}, "", "home directory is unknown"},
		{map[string]any{"adopt_root": 1.0}, "/h", "cerberus.adopt_root must be a string"},
		{map[string]any{"adopt_root": "rel"}, "/h", "cerberus.adopt_root"},
	} {
		_, err := Parse(c.raw, c.home)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("Parse(%v) err = %v, want %q", c.raw, err, c.want)
		}
	}
}

func TestModeAndSelf(t *testing.T) {
	s, err := Parse(nil, "/u/op")
	if err != nil || s.Mode != ModeObserve || s.SelfResourceID != "" {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	s, err = Parse(map[string]any{"mode": "supervise", "self_resource_id": " cerberus-substrate "}, "/u/op")
	if err != nil || s.Mode != ModeSupervise || s.SelfResourceID != "cerberus-substrate" {
		t.Fatalf("set: %+v %v", s, err)
	}
	for raw, want := range map[string]string{"chaos": "cerberus.mode must be observe or supervise", "": ""} {
		_, err := Parse(map[string]any{"mode": raw}, "/u/op")
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Fatalf("mode %q: %v", raw, err)
		}
	}
	if _, err := Parse(map[string]any{"self_resource_id": 3.0}, "/u/op"); err == nil || !strings.Contains(err.Error(), "cerberus.self_resource_id must be a string") {
		t.Fatalf("non-string self: %v", err)
	}
}

func TestBackgroundBlocks(t *testing.T) {
	s, err := Parse(map[string]any{}, "/u/op")
	if err != nil {
		t.Fatal(err)
	}
	if s.HostMonitor != nil || s.Scheduler.Enabled || s.EventExport.Enabled {
		t.Fatalf("defaults: %+v %+v %+v", s.HostMonitor, s.Scheduler, s.EventExport)
	}
	s, err = Parse(map[string]any{
		"host_monitor": map[string]any{"enabled": true, "outpost_interval": "30s", "thresholds": map[string]any{"offline_after": 2}},
		"scheduler":    map[string]any{"enabled": true, "allow_existing": true},
		"event_export": map[string]any{"enabled": true, "grace_seconds": 5, "interval_seconds": 10, "hub": true},
	}, "/u/op")
	if err != nil {
		t.Fatal(err)
	}
	if s.HostMonitor == nil || !s.HostMonitor.Enabled || s.HostMonitor.OutpostInterval != "30s" ||
		s.HostMonitor.Thresholds == nil || s.HostMonitor.Thresholds.OfflineAfter != 2 {
		t.Fatalf("host_monitor: %+v", s.HostMonitor)
	}
	if s.Scheduler != (Scheduler{Enabled: true, AllowExisting: true}) {
		t.Fatalf("scheduler: %+v", s.Scheduler)
	}
	if s.EventExport != (EventExport{Enabled: true, GraceSeconds: 5, IntervalSeconds: 10, Hub: true}) {
		t.Fatalf("event_export: %+v", s.EventExport)
	}
	for name, block := range map[string]map[string]any{
		"host_monitor": {"enabeld": true},
		"scheduler":    {"enabled": "yes please"},
		"event_export": {"interval_seconds": -1},
	} {
		if _, err := Parse(map[string]any{name: block}, "/u/op"); err == nil || !strings.Contains(err.Error(), "cerberus."+name) {
			t.Errorf("%s: want an error naming cerberus.%s, got %v", name, name, err)
		}
	}
}

// connectors is the old connector-config.yaml's shape: plugin id to fields, mcp.expose and
// limits; an unknown key is refused naming the block, and the block is fingerprinted.
func TestConnectors(t *testing.T) {
	s, err := Parse(nil, "/u/op")
	if err != nil || s.Connectors.Entries == nil || s.Connectors.SHA256 != "" {
		t.Fatalf("unset connectors = %+v, %v", s.Connectors, err)
	}
	s, err = Parse(map[string]any{"connectors": map[string]any{"cloudflare": map[string]any{
		"fields": map[string]any{"account": "a1"}, "mcp": map[string]any{"expose": []any{"list_zones"}}}}}, "/u/op")
	if err != nil {
		t.Fatal(err)
	}
	if e := s.Connectors.Entries["cloudflare"]; e.Fields["account"] != "a1" || len(e.MCP.Expose) != 1 || s.Connectors.SHA256 == "" {
		t.Fatalf("connectors = %+v", s.Connectors)
	}
	for _, raw := range []any{
		map[string]any{"connectors": map[string]any{"cloudflare": map[string]any{"feilds": map[string]any{}}}},
		map[string]any{"connectors": []any{"cloudflare"}},
	} {
		if _, err := Parse(raw, "/u/op"); err == nil || !strings.Contains(err.Error(), "cerberus.connectors") {
			t.Fatalf("Parse(%v) err = %v, want a refusal naming cerberus.connectors", raw, err)
		}
	}
}

// build.install_after_build is the global install-after-build default: true (old's default)
// when unset, the key's value when set; a wrong type or an unknown key names the block.
func TestBuildInstallAfterBuild(t *testing.T) {
	for _, c := range []struct {
		raw  any
		want bool
	}{
		{nil, true},
		{map[string]any{"build": nil}, true},
		{map[string]any{"build": map[string]any{}}, true},
		{map[string]any{"build": map[string]any{"install_after_build": nil}}, true},
		{map[string]any{"build": map[string]any{"install_after_build": true}}, true},
		{map[string]any{"build": map[string]any{"install_after_build": false}}, false},
	} {
		s, err := Parse(c.raw, "/u/op")
		if err != nil || s.InstallAfterBuildDefault() != c.want {
			t.Fatalf("Parse(%v): default %v, %v; want %v", c.raw, s.InstallAfterBuildDefault(), err, c.want)
		}
	}
	for _, block := range []map[string]any{{"install_after_build": "sometimes"}, {"install_after_build": 1.0}, {"install_after_biuld": false}} {
		if _, err := Parse(map[string]any{"build": block}, "/u/op"); err == nil || !strings.Contains(err.Error(), "cerberus.build") {
			t.Errorf("build %v: want an error naming cerberus.build, got %v", block, err)
		}
	}
}
