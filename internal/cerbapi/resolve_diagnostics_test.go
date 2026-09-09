package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/registry"
)

// writeRegistryFixture lays out a config.yaml plus a sibling registry
// index pointing at each of the given owner→body configs, and returns
// the config.yaml path. The sibling layout is what ResolveConfig
// derives, so a test that writes it exercises the real lookup.
func writeRegistryFixture(t *testing.T, configs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("version: 2\n"), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	index := "version: 1\nentries:\n"
	for owner, body := range configs {
		path := filepath.Join(dir, owner+".cerberus.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s config: %v", owner, err)
		}
		index += "    - owner: " + owner + "\n" +
			"      namespace: local\n" +
			"      path: " + path + "\n" +
			"      kind: cerberus-project/v1\n" +
			"      registered_at: \"2026-09-09T00:00:00Z\"\n"
	}

	indexPath, err := registry.IndexPathFor(cfgPath)
	if err != nil {
		t.Fatalf("IndexPathFor: %v", err)
	}
	if err := os.WriteFile(indexPath, []byte(index), 0o600); err != nil {
		t.Fatalf("write index: %v", err)
	}
	return cfgPath
}

const cleanProjectConfig = `kind: cerberus-project/v1
owner: cleanapp
project:
  id: cleanapp
  name: Clean App
resources:
  - id: cleanapp-api
    name: Clean API
    type: process
    connector: local
    project: cleanapp
`

// futureProjectConfig carries a field this binary does not know — valid
// at runtime since CW-20260909-0007, but worth saying out loud.
const futureProjectConfig = `kind: cerberus-project/v1
owner: futureapp
future_top_level_field: something-this-binary-has-never-heard-of
project:
  id: futureapp
  name: Future App
resources:
  - id: futureapp-api
    name: Future API
    type: process
    connector: local
    project: futureapp
`

// brokenProjectConfig fails validation, so resolve drops it entirely.
// This is the case that produced a bare "No resources found".
const brokenProjectConfig = `kind: cerberus-project/v1
owner: brokenapp
project:
  id: brokenapp
  name: Broken App
resources:
  - id: brokenapp-api
    name: Broken API
    type: process
    connector: local
    project: brokenapp
    config:
      port: 0
`

func TestResolveDiagnosticsCountsSkippedAndWarned(t *testing.T) {
	cfgPath := writeRegistryFixture(t, map[string]string{
		"cleanapp":  cleanProjectConfig,
		"futureapp": futureProjectConfig,
		"brokenapp": brokenProjectConfig,
	})
	svc := NewResourceRuntimeService(WithResourceRuntimeConfigPath(cfgPath))

	diag, err := svc.ResolveDiagnostics(context.Background())
	if err != nil {
		t.Fatalf("ResolveDiagnostics: %v", err)
	}
	if diag.Skipped != 1 || diag.SkippedOwners[0] != "brokenapp" {
		t.Errorf("Skipped = %d %v, want 1 [brokenapp]", diag.Skipped, diag.SkippedOwners)
	}
	if diag.Warned != 1 || diag.WarnedOwners[0] != "futureapp" {
		t.Errorf("Warned = %d %v, want 1 [futureapp]", diag.Warned, diag.WarnedOwners)
	}
	if diag.Clean() {
		t.Error("Clean() = true with a skipped and a warned config")
	}

	// The dropped config must be absent from the list the same call
	// serves — that pairing is the whole reason the notice exists.
	list, err := svc.ListResources(context.Background(), ResourceListArgs{})
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	for _, r := range list {
		if r.Project == "brokenapp" {
			t.Fatalf("brokenapp resource %q present in list but reported skipped", r.ID)
		}
	}
}

func TestResolveDiagnosticsCleanTreeReportsNothing(t *testing.T) {
	cfgPath := writeRegistryFixture(t, map[string]string{"cleanapp": cleanProjectConfig})
	svc := NewResourceRuntimeService(WithResourceRuntimeConfigPath(cfgPath))

	diag, err := svc.ResolveDiagnostics(context.Background())
	if err != nil {
		t.Fatalf("ResolveDiagnostics: %v", err)
	}
	if !diag.Clean() {
		t.Errorf("Clean() = false on a healthy tree: %+v", diag)
	}
}

// A service built from an in-memory config has no registry to resolve.
// It must report clean rather than erroring, so the notice is simply
// absent on that path instead of breaking the list.
func TestResolveDiagnosticsInMemoryConfigReportsClean(t *testing.T) {
	svc := NewResourceRuntimeService(WithResourceRuntimeConfigV2(&config.ConfigV2{Version: 2}))

	diag, err := svc.ResolveDiagnostics(context.Background())
	if err != nil {
		t.Fatalf("ResolveDiagnostics: %v", err)
	}
	if !diag.Clean() {
		t.Errorf("Clean() = false for an in-memory config: %+v", diag)
	}
}

// TestResolveDiagnosticsOverSocket walks the whole path the CLI takes in
// daemon mode: socket client → route → in-process client → runtime
// service. The counts crossing the wire is the part unit tests on either
// side cannot show.
func TestResolveDiagnosticsOverSocket(t *testing.T) {
	cfgPath := writeRegistryFixture(t, map[string]string{
		"cleanapp":  cleanProjectConfig,
		"futureapp": futureProjectConfig,
		"brokenapp": brokenProjectConfig,
	})

	// os.TempDir rather than t.TempDir: a unix socket path is capped at
	// ~104 bytes and the per-test temp dir alone can exceed it.
	sockPath := filepath.Join(os.TempDir(),
		fmt.Sprintf("cerb-diag-%d-%d.sock", os.Getpid(), time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	srv := NewSocketServer(NewInProcessClient(WithConfigPath(cfgPath)), sockPath)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("socket server: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}

	diag, err := NewSocketClient(sockPath).ResolveDiagnostics(context.Background())
	if err != nil {
		t.Fatalf("ResolveDiagnostics over socket: %v", err)
	}
	if diag.Skipped != 1 || diag.Warned != 1 {
		t.Errorf("diag = %+v, want skipped=1 warned=1", diag)
	}
	if len(diag.SkippedOwners) != 1 || diag.SkippedOwners[0] != "brokenapp" {
		t.Errorf("SkippedOwners = %v, want [brokenapp]", diag.SkippedOwners)
	}
	if len(diag.WarnedOwners) != 1 || diag.WarnedOwners[0] != "futureapp" {
		t.Errorf("WarnedOwners = %v, want [futureapp]", diag.WarnedOwners)
	}
}
