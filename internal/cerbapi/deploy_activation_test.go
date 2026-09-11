package cerbapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/pausectl"
	"github.com/chrispian/cerberus/internal/service"
)

func TestDeployBuildsActivatesAndPreservesRunningProcessOnBuildFailure(t *testing.T) {
	dir := t.TempDir()
	id := fmt.Sprintf("test-deploy-%d-%s", os.Getpid(), filepath.Base(dir))
	if err := os.WriteFile(filepath.Join(dir, "source-app"), []byte("#!/bin/sh\nexec /bin/sleep \"$@\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	makefile := filepath.Join(dir, "Makefile")
	if err := os.WriteFile(makefile, []byte("build:\n\t@test \"$$CERBERUS_PIN_TEST\" = 22\n\t@cp source-app app\n\t@chmod 700 app\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{{ID: id, Project: "test", Type: "process", Connector: "local", Config: map[string]any{
		"dir": dir, "command": []string{"./app", "60"}, "log_file": filepath.Join(dir, "runtime.log"), "install_root": filepath.Join(dir, "install"),
		"install_after_build": false, "build_strategy": map[string]any{"kind": "make_standard", "env_prefix": []string{"/usr/bin/env", "CERBERUS_PIN_TEST=22"}, "rules": map[string]any{"output": "./app"}},
	}}}}
	svc := NewResourceRuntimeService(WithResourceRuntimeConfigV2(cfg))
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = svc.StopResource(ctx, id)
		_ = pausectl.ResumeService(id)
		_ = service.RemovePIDFile(id)
	})
	first, err := svc.DeployResource(ctx, id)
	if err != nil || !first.Success || !first.BuildPerformed || first.Activation == nil || first.Activation.SHA256 == "" {
		t.Fatalf("deploy lacked build/activation evidence: %+v %v", first, err)
	}
	before, err := service.ReadPIDFile(id)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.DeployResource(ctx, id)
	if err != nil || !second.Success {
		t.Fatalf("second deploy: %+v %v", second, err)
	}
	after, err := service.ReadPIDFile(id)
	if err != nil || before == after {
		t.Fatalf("second deploy retained old process: %d %d %v", before, after, err)
	}
	if err = os.WriteFile(makefile, []byte("build:\n\t@exit 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	failed, err := svc.DeployResource(ctx, id)
	if err != nil || failed.Success || !strings.Contains(failed.Error, "build failed") {
		t.Fatalf("build failure not reported: %+v %v", failed, err)
	}
	still, err := service.ReadPIDFile(id)
	if err != nil || still != after {
		t.Fatal("failed build disrupted running process")
	}
	if _, err = svc.StopResource(ctx, id); err != nil {
		t.Fatal(err)
	}
	applied, err := svc.ApplyResource(ctx, id)
	if err != nil || !applied.Success || applied.BuildPerformed || applied.Activation == nil || !strings.Contains(applied.Message, "no build ran") {
		t.Fatalf("apply confused with deployment: %+v %v", applied, err)
	}
}
