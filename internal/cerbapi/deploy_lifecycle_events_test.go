package cerbapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/eventexport"
	"github.com/hollis-labs/cerberus/internal/pausectl"
	"github.com/hollis-labs/cerberus/internal/service"
)

// A failed build leaves the previous process running (see
// TestDeployBuildsActivatesAndPreservesRunningProcessOnBuildFailure), so the
// exported lifecycle must say the deploy failed and nothing about the resource.
func TestFailedBuildDeployExportsDeployFailureNotResourceFailure(t *testing.T) {
	dir := t.TempDir()
	id := fmt.Sprintf("test-lifecycle-%d-%s", os.Getpid(), filepath.Base(dir))
	if err := os.WriteFile(filepath.Join(dir, "source-app"), []byte("#!/bin/sh\nexec /bin/sleep \"$@\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	makefile := filepath.Join(dir, "Makefile")
	if err := os.WriteFile(makefile, []byte("build:\n\t@cp source-app app\n\t@chmod 700 app\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{{ID: id, Project: "test", Type: "process", Connector: "local", Config: map[string]any{
		"dir": dir, "command": []string{"./app", "60"}, "log_file": filepath.Join(dir, "runtime.log"), "install_root": filepath.Join(dir, "install"),
		"install_after_build": false, "build_strategy": map[string]any{"kind": "make_standard", "env_prefix": []string{"/usr/bin/env"}, "rules": map[string]any{"output": "./app"}},
	}}}}
	log := audit.NewMemory()
	svc := NewResourceRuntimeService(log, WithResourceRuntimeConfigV2(cfg))
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = svc.StopResource(ctx, id, WithAcknowledged(true))
		_ = pausectl.ResumeService(id)
		_ = service.RemovePIDFile(id)
	})
	if r, err := svc.DeployResource(ctx, id, WithAcknowledged(true)); err != nil || !r.Success {
		t.Fatalf("first deploy: %+v %v", r, err)
	}
	running, err := service.ReadPIDFile(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(makefile, []byte("build:\n\t@exit 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if r, err := svc.DeployResource(ctx, id, WithAcknowledged(true)); err != nil || r.Success {
		t.Fatalf("build failure not reported: %+v %v", r, err)
	}
	if still, err := service.ReadPIDFile(id); err != nil || still != running {
		t.Fatal("failed build disrupted running process")
	}

	recs := log.Records()
	var last audit.Record
	for _, rec := range recs {
		if rec.Kind == audit.KindOutcome && rec.Operation == "deploy" {
			last = rec
		}
	}
	if last.OutcomeCode == audit.OutcomeOK || last.OperationID == "" {
		t.Fatalf("no failed deploy outcome in the audit log: %+v", last)
	}
	got := eventexport.MapLifecycle(last)
	if len(got) != 2 {
		t.Fatalf("failed deploy exported %d events, want deploy started and failed only", len(got))
	}
	for _, m := range got {
		if m.Envelope.Kind != "deploy.state_changed" {
			t.Fatalf("failed deploy exported %s; the running process was preserved", m.Envelope.Kind)
		}
	}
}
