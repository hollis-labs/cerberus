package cerbapi

import (
	"context"
	"github.com/hollis-labs/cerberus/internal/audit"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
