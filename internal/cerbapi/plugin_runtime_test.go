package cerbapi

import (
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/pluginhost"
)

func newTestPluginManager(t *testing.T, installer pluginhost.Installer, launcher pluginhost.Launcher, version string, opts ...pluginhost.ManagerOption) *pluginhost.Manager {
	t.Helper()
	root := t.TempDir()
	opts = append([]pluginhost.ManagerOption{pluginhost.WithRuntimeRoots(filepath.Join(root, "data"), filepath.Join(root, "cache"))}, opts...)
	return pluginhost.NewManager(installer, launcher, version, opts...)
}
