package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/pausectl"
	"github.com/hollis-labs/cerberus/internal/service"
)

func TestResourceStartupOrderRespectsDependenciesAndReportsCycles(t *testing.T) {
	resources := []config.ResourceDef{{ID: "web", DependsOn: []string{"api"}}, {ID: "api", DependsOn: []string{"db"}}, {ID: "db"}}
	ordered, warnings := resourceStartupOrder(resources, nil)
	position := map[string]int{}
	for i, res := range ordered {
		position[res.ID] = i
	}
	if position["db"] >= position["api"] || position["api"] >= position["web"] || len(warnings) > 0 {
		t.Fatalf("bad dependency order: %+v %v", ordered, warnings)
	}
	resources[2].DependsOn = []string{"api"}
	_, warnings = resourceStartupOrder(resources, []string{"web"})
	if !strings.Contains(strings.Join(warnings, " "), "api -> db -> api") {
		t.Fatalf("cycle not explained: %v", warnings)
	}
}

func TestExplicitStartWarnsAboutUnavailableDependencies(t *testing.T) {
	dir := t.TempDir()
	id := fmt.Sprintf("test-dependencies-%d-%s", os.Getpid(), filepath.Base(dir))
	depID := id + "-db"
	cfg := &config.ConfigV2{Resources: []config.ResourceDef{
		{ID: id, Type: "process", Connector: "local", DependsOn: []string{depID, "missing-dependency"}, Config: map[string]any{"command": []string{"/bin/sleep", "60"}, "log_file": filepath.Join(dir, "runtime.log")}},
		{ID: depID, Type: "process", Connector: "local", Config: map[string]any{"command": []string{"/bin/sleep", "60"}}},
	}}
	svc := NewResourceRuntimeService(WithResourceRuntimeConfigV2(cfg))
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = svc.StopResource(ctx, id, WithAcknowledged(true))
		_ = pausectl.ResumeService(id)
		_ = service.RemovePIDFile(id)
	})
	result, err := svc.ApplyResource(ctx, id, WithAcknowledged(true))
	if err != nil || !result.Success {
		t.Fatalf("explicit start was blocked: %+v %v", result, err)
	}
	warnings := strings.Join(result.Warnings, " ")
	if !strings.Contains(warnings, depID) || !strings.Contains(warnings, "currently stopped") || !strings.Contains(warnings, "missing-dependency") {
		t.Fatalf("missing dependency warnings: %v", warnings)
	}
	if _, err = service.ReadPIDFile(depID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dependency was silently started: %v", err)
	}
	status, err := svc.GetResourceRuntime(ctx, id)
	if err != nil || !strings.Contains(strings.Join(status.DependencyWarnings, " "), depID) {
		t.Fatalf("status hides dependency: %+v %v", status, err)
	}
	doctor, err := svc.GetResourceDoctor(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range doctor.Checks {
		if check.Name == "dependency" && check.Status == "warn" {
			return
		}
	}
	t.Fatal("doctor omitted dependency warnings")
}
