package cerbapi

import (
	"context"
	"github.com/hollis-labs/cerberus/internal/audit"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/config"
)

func TestRuntimeObserversDoNotWaitBehindBuild(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{{ID: "building", Type: "process", Connector: "local", Config: map[string]any{
		"dir": dir, "command": []string{"/bin/true"}, "install_after_build": false,
		"build_strategy": map[string]any{"kind": "legacy_command", "rules": map[string]any{"command": []string{"/bin/sh", "-c", "touch started; while [ ! -f release ]; do sleep 0.01; done; exit 1"}}},
	}}}}
	svc := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(cfg))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.DeployResource(context.Background(), "building", WithAcknowledged(true))
	}()
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(dir, "release"), nil, 0600); <-done })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("build did not start")
		}
		time.Sleep(time.Millisecond)
	}
	observed := make(chan error, 1)
	go func() {
		_, err := svc.GetResourceRuntime(context.Background(), "building")
		if err == nil {
			_, err = svc.GetResourceInspect(context.Background(), "building")
		}
		if err == nil {
			_, err = svc.ResourceLogs(context.Background(), "building", 10, "stdout")
		}
		observed <- err
	}()
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime reads waited behind the unfinished build")
	}
}
