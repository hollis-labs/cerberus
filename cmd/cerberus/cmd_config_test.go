package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/configops"
)

func TestConfigMigrationIsRetired(t *testing.T) {
	previous := cfgPath
	cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	t.Cleanup(func() { cfgPath = previous })
	if err := os.WriteFile(cfgPath, []byte("version: 2\nprojects:\n  - id: owned\n    name: Owned\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := configMigrateCmd.RunE(configMigrateCmd, nil); !errors.Is(err, configops.ErrCentralizedMigrationRetired) {
		t.Fatalf("migration = %v", err)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("original config moved or removed: %v", err)
	}
	for _, path := range []string{cfgPath + ".bak", filepath.Join(filepath.Dir(cfgPath), "projects"), filepath.Join(filepath.Dir(cfgPath), "registry.yaml")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("retired migration wrote %s: %v", path, err)
		}
	}
}

func TestValidateRefusesCentralLocationAndBundleReference(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	central := filepath.Join(homeDir, ".cerberus", "projects")
	if err := os.MkdirAll(central, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(central, "owned.cerberus.yaml")
	if err := os.WriteFile(path, []byte("kind: cerberus-project/v1\nproject:\n  id: owned\n  name: Owned\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "bundle.cerberus.yaml")
	if err := os.WriteFile(bundle, []byte("kind: cerberus-bundle/v1\nprojects: ["+path+"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, bundle} {
		if err := validateOneFile(candidate); err == nil || !strings.Contains(err.Error(), "owning repo") {
			t.Fatalf("validate(%s) = %v", candidate, err)
		}
	}
}
