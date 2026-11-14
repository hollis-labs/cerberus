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
		if s.InstallRoot != "/u/op/.cerberus/apps" {
			t.Fatalf("install_root = %q", s.InstallRoot)
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
	} {
		_, err := Parse(c.raw, c.home)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("Parse(%v) err = %v, want %q", c.raw, err, c.want)
		}
	}
}
