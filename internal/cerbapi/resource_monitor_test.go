package cerbapi

import (
	"context"
	"github.com/hollis-labs/cerberus/internal/audit"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/config"
)

func TestResourceMonitorRemovedResourceGetsFreshRetryBudget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.ConfigV2{}
	m := NewResourceMonitor(NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(cfg)), DefaultResourceMonitorConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.failureCount["returning"] = 3
	m.lastRestart["returning"] = time.Now()
	m.lastError["returning"] = "old failure"
	m.checkAllResources(context.Background())
	marker := filepath.Join(t.TempDir(), "started")
	cfg.Resources = []config.ResourceDef{{ID: "returning", Type: "process", Connector: "local", Config: map[string]any{"auto_restart": true, "command": []string{"/usr/bin/touch", marker}}}}
	m.checkAllResources(context.Background())
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("old retry state prevented re-added resource starting: %v", m.lastError)
		}
		time.Sleep(time.Millisecond)
	}
	if m.lastError["returning"] != "" {
		t.Fatalf("old error survived successful restart: %v", m.lastError)
	}
}

func TestResourceMonitorChecksAtBootBeforeFirstTick(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	marker := filepath.Join(t.TempDir(), "started")
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{{ID: "boot", Type: "process", Connector: "local", Config: map[string]any{"auto_restart": true, "command": []string{"/usr/bin/touch", marker}}}}}
	m := NewResourceMonitor(NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(cfg)), ResourceMonitorConfig{CheckInterval: time.Hour, DefaultMaxRestartAttempts: 3}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = m.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("eligible resource waited for the first monitor tick")
		}
		time.Sleep(time.Millisecond)
	}
}

// The monitor restarts only the definition a workload was last applied
// with (M10): with the definition edited since, the workload stays down
// and the monitor says to apply it; applied again, it restarts.
func TestTheMonitorRestartsOnlyTheAppliedDefinition(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	applied, edited := filepath.Join(dir, "applied"), filepath.Join(dir, "edited")
	def := func(marker string) config.ResourceDef {
		return config.ResourceDef{ID: "svc", Type: "process", Connector: "local", Config: map[string]any{"auto_restart": true, "command": []string{"/usr/bin/touch", marker}}}
	}
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{def(applied)}}
	runtime := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(cfg))
	checked := def(applied)
	if err := runtime.applied.set("svc", runtime.definitionDigest(&checked)); err != nil {
		t.Fatal(err)
	}
	cfg.Resources = []config.ResourceDef{def(edited)}
	m := NewResourceMonitor(runtime, DefaultResourceMonitorConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.checkAllResources(context.Background())
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(edited); err == nil {
		t.Fatal("the monitor ran a definition nobody applied")
	}
	if !strings.Contains(m.lastError["svc"], "cerberus resource apply svc") {
		t.Fatalf("last error %q", m.lastError["svc"])
	}
	now := def(edited)
	if err := runtime.applied.set("svc", runtime.definitionDigest(&now)); err != nil {
		t.Fatal(err)
	}
	m.checkAllResources(context.Background())
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(edited); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the applied definition did not restart: %v", m.lastError)
		}
		time.Sleep(time.Millisecond)
	}
}

// A gated start records the definition it ran, and the record survives
// the process when it has a path.
func TestAGatedStartRecordsItsDefinition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "applied.json")
	a := &appliedDefinitions{path: path}
	if err := a.set("svc", "hmac:one"); err != nil {
		t.Fatal(err)
	}
	again := &appliedDefinitions{path: path}
	if d, ok := again.get("svc"); !ok || d != "hmac:one" {
		t.Fatalf("after a restart: %q %v", d, ok)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stored %v %v", info, err)
	}
}

// A gated apply records the definition it started, which is then the one
// the monitor restarts.
func TestAGatedApplyRecordsTheDefinitionItRan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	res := config.ResourceDef{ID: "svc", Type: "process", Connector: "local", Config: map[string]any{"command": []string{"/bin/sleep", "5"}}}
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{res}}
	runtime := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(cfg))
	out, err := runtime.ApplyResource(BeginRequest(context.Background(), SurfaceInProcess), "svc", WithAcknowledged(true))
	if err != nil || out == nil || !out.Success {
		t.Fatalf("apply: %+v %v", out, err)
	}
	t.Cleanup(func() {
		_, _ = runtime.StopResource(BeginRequest(context.Background(), SurfaceInProcess), "svc", WithAcknowledged(true))
	})
	if d, ok := runtime.applied.get("svc"); !ok || d != runtime.definitionDigest(&res) {
		t.Fatalf("recorded %q %v", d, ok)
	}
}
