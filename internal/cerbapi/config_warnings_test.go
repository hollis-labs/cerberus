package cerbapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/pausectl"
	"github.com/hollis-labs/cerberus/internal/service"
)

func TestResourceConfigWarningVisibleInLifecycleStatusAndDoctor(t *testing.T) {
	dir := t.TempDir()
	id := fmt.Sprintf("test-warning-%d-%s", os.Getpid(), filepath.Base(dir))
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{{ID: id, Type: "process", Connector: "local", Config: map[string]any{"command": []string{"/bin/sleep", "60"}, "log_file": filepath.Join(dir, "out.log"), "ENV": map[string]any{"TOKEN": "secret-sentinel"}}}}}
	svc := NewResourceRuntimeService(WithResourceRuntimeConfigV2(cfg))
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = svc.StopResource(ctx, id, WithAcknowledged(true))
		_ = pausectl.ResumeService(id)
		_ = service.RemovePIDFile(id)
	})
	op, err := svc.ApplyResource(ctx, id, WithAcknowledged(true))
	if err != nil || !op.Success || !strings.Contains(strings.Join(op.Warnings, " "), "config.ENV") {
		t.Fatalf("apply hid typo: %+v %v", op, err)
	}
	status, err := svc.GetResourceRuntime(ctx, id)
	if err != nil || !strings.Contains(strings.Join(status.ConfigWarnings, " "), "config.ENV") {
		t.Fatalf("status hid typo: %+v %v", status, err)
	}
	doctor, err := svc.GetResourceDoctor(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range doctor.Checks {
		if check.Name == "config_key" && check.Status == "warn" && strings.Contains(check.Message, "config.ENV") {
			return
		}
	}
	t.Fatalf("doctor hid typo: %+v", doctor)
}
