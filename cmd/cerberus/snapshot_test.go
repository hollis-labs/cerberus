package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/supervise"
)

// The snapshot counts the registry's descriptors and the lane's observed workloads as main's
// in-process snapshot did: running, stopped, and everything else needing attention.
func TestSnapshotCounts(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".cerberus")
	desc := filepath.Join(home, "apps", "demo.cerberus.yaml")
	for _, d := range []string{root, filepath.Dir(desc)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	svc := func(id string) string {
		return "  - id: " + id + "\n    type: process\n    project: demo\n    connector: local\n    env: dev\n    owner: self\n    admin: self\n" +
			"    config:\n      mode: os_service\n      supervisor: systemd_user\n      command: [\"/bin/true\"]\n"
	}
	descriptor := "kind: cerberus-project/v1\nowner: demo\nnamespace: local\nproject:\n  id: demo\n  name: Demo\nresources:\n" +
		svc("up") + svc("down") + svc("crashing") +
		"  - id: box-ssh\n    type: server\n    project: demo\n    connector: ssh\n    env: dev\n    owner: self\n    admin: self\n"
	registryYAML := "version: 1\nentries:\n  - owner: demo\n    namespace: local\n    path: " + desc + "\n    kind: cerberus-project/v1\n"
	for p, c := range map[string]string{desc: descriptor, filepath.Join(root, "registry.yaml"): registryYAML} {
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg := registry.ForRoot(root)
	list := func() ([]config.ResourceDef, error) {
		r, err := reg.Resolve()
		if err != nil {
			return nil, err
		}
		return r.Config.Resources, nil
	}
	units := fakeUnits{
		"com.hollis-labs.cerberus.demo.up.service":       "LoadState=loaded\nActiveState=active\nSubState=running\n",
		"com.hollis-labs.cerberus.demo.down.service":     "LoadState=loaded\nActiveState=inactive\nSubState=dead\n",
		"com.hollis-labs.cerberus.demo.crashing.service": "LoadState=loaded\nActiveState=failed\nSubState=failed\nResult=exit-code\n",
	}
	lane, err := supervise.New(supervise.Options{Mode: settings.ModeObserve, Resources: list, Home: home, StateDir: home, Runner: units})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := snapshotSource{lane: lane, reg: reg}.take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Resources != 4 || snap.Projects != 1 || snap.Running != 1 || snap.Stopped != 1 || snap.Attention != 1 || snap.RegistryEntries != 1 {
		t.Fatalf("snapshot %+v", snap)
	}
}
