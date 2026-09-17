package config_test

import (
	"path/filepath"
	"testing"

	"github.com/hollis-labs/go-apppaths/paths"

	"github.com/hollis-labs/cerberus/internal/config"
)

// hermeticPaths pins the four XDG roots into per-test temp dirs so
// config.ResolveLayout resolves (and materializes) Cerberus's layout under
// t.TempDir rather than the developer's real home. $HOME is left real on
// purpose — Cerberus tests shell out to launchctl/git/keychain and $HOME
// pinning breaks them (sharp edge #9 of the cutover runbook).
func hermeticPaths(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	return base
}

func TestResolveLayoutDefault(t *testing.T) {
	base := hermeticPaths(t)

	layout, err := config.ResolveLayout()
	if err != nil {
		t.Fatalf("ResolveLayout: %v", err)
	}

	wantDB := filepath.Join(base, "data", "cerberus", "workspaces", "default", "main.db")
	if got := layout.MainDB(); got != wantDB {
		t.Errorf("MainDB() = %q, want %q", got, wantDB)
	}
	if got, want := layout.DataDir(), filepath.Join(base, "data", "cerberus"); got != want {
		t.Errorf("DataDir() = %q, want %q", got, want)
	}
	if got, want := layout.StateDir(), filepath.Join(base, "state", "cerberus"); got != want {
		t.Errorf("StateDir() = %q, want %q", got, want)
	}
	if got, want := layout.ConfigDir(), filepath.Join(base, "config", "cerberus"); got != want {
		t.Errorf("ConfigDir() = %q, want %q", got, want)
	}
}

// TestResolveLayoutDBOverride checks that paths.WithDBOverride (the mechanism
// behind the --db flag) reroutes the main database path.
func TestResolveLayoutDBOverride(t *testing.T) {
	base := hermeticPaths(t)

	override := filepath.Join(base, "custom", "cerberus.db")
	layout, err := config.ResolveLayout(paths.WithDBOverride(override), paths.WithoutMaterialize())
	if err != nil {
		t.Fatalf("ResolveLayout: %v", err)
	}
	if got := layout.MainDB(); got != override {
		t.Errorf("MainDB() = %q, want override %q", got, override)
	}
}

// TestResolveLayoutDBPathEnv checks that CERBERUS_DB_PATH is honored natively
// by go-apppaths (env prefix derived from the "cerberus" app name).
func TestResolveLayoutDBPathEnv(t *testing.T) {
	base := hermeticPaths(t)

	envDB := filepath.Join(base, "env", "cerberus.db")
	t.Setenv("CERBERUS_DB_PATH", envDB)

	layout, err := config.ResolveLayout(paths.WithoutMaterialize())
	if err != nil {
		t.Fatalf("ResolveLayout: %v", err)
	}
	if got := layout.MainDB(); got != envDB {
		t.Errorf("MainDB() = %q, want CERBERUS_DB_PATH %q", got, envDB)
	}
}
