package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// State readable by other accounts is made private at start, and said so.
func TestEnsurePrivateFixesModesLoudly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".cerberus")
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // the state as an older Cerberus left it
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("version: 2\n"), 0o644); err != nil { //nolint:gosec // the state as an older Cerberus left it
		t.Fatal(err)
	}
	fixed, err := EnsurePrivate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixed) != 2 || !strings.Contains(fixed[0], "0755") || !strings.Contains(fixed[1], "0644") || !strings.Contains(fixed[0], "readable by other accounts") {
		t.Fatalf("fixed = %q", fixed)
	}
	for path, want := range map[string]os.FileMode{dir: 0o700, cfg: 0o600} {
		if info, _ := os.Stat(path); info.Mode().Perm() != want {
			t.Errorf("%s is %04o, want %04o", path, info.Mode().Perm(), want)
		}
	}
	// Already private: nothing to say.
	if fixed, err := EnsurePrivate(cfg); err != nil || len(fixed) != 0 {
		t.Fatalf("second run: %q %v", fixed, err)
	}
}

// A fresh machine gets a private directory, whatever the umask, and a new
// default config is private.
func TestEnsurePrivateCreatesPrivately(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if fixed, err := EnsurePrivate(""); err != nil || len(fixed) != 0 {
		t.Fatalf("%q %v", fixed, err)
	}
	if info, _ := os.Stat(filepath.Join(home, ".cerberus")); info.Mode().Perm() != 0o700 {
		t.Fatalf("created %04o", info.Mode().Perm())
	}
	if err := EnsureDefault(); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(DefaultPath()); info.Mode().Perm() != 0o600 {
		t.Fatalf("default config %04o", info.Mode().Perm())
	}
}
