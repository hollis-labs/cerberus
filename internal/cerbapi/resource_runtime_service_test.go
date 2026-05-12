package cerbapi

import (
	"context"
	"runtime"
	"testing"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/pausectl"
)

func TestResourceRuntimeStopPausesDevSessionUntilApply(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.ConfigV2{
		Resources: []config.ResourceDef{
			{
				ID:        "dev-api",
				Name:      "Dev API",
				Type:      string(domain.ResourceProcess),
				Connector: "local",
				Config: map[string]any{
					"mode":    "dev_session",
					"command": []any{"/bin/sh", "-c", "true"},
				},
			},
		},
	}
	svc := NewResourceRuntimeService(WithResourceRuntimeConfigV2(cfg))

	stopRes, err := svc.StopResource(context.Background(), "dev-api")
	if err != nil {
		t.Fatalf("StopResource returned error: %v", err)
	}
	if !stopRes.Success {
		t.Fatalf("StopResource success=false: %s", stopRes.Error)
	}
	if !pausectl.IsServicePaused("dev-api") {
		t.Fatal("expected dev_session stop to set operator pause flag")
	}
	status, err := svc.GetResourceRuntime(context.Background(), "dev-api")
	if err != nil {
		t.Fatalf("GetResourceRuntime returned error: %v", err)
	}
	if !status.OperatorStopped {
		t.Fatalf("expected operator stopped status, got %+v", status)
	}
	doctor, err := svc.GetResourceDoctor(context.Background(), "dev-api")
	if err != nil {
		t.Fatalf("GetResourceDoctor returned error: %v", err)
	}
	if !doctor.OperatorStopped {
		t.Fatalf("expected operator stopped doctor result, got %+v", doctor)
	}

	applyRes, err := svc.ApplyResource(context.Background(), "dev-api")
	if err != nil {
		t.Fatalf("ApplyResource returned error: %v", err)
	}
	if !applyRes.Success {
		t.Fatalf("ApplyResource success=false: %s", applyRes.Error)
	}
	if pausectl.IsServicePaused("dev-api") {
		t.Fatal("expected explicit apply to clear operator pause flag")
	}
}

func TestResourceRuntimeStopDoesNotPauseOSService(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd stop semantics are macOS-specific")
	}
	t.Setenv("HOME", t.TempDir())
	cfg := &config.ConfigV2{
		Resources: []config.ResourceDef{
			{
				ID:        "service-api",
				Name:      "Service API",
				Type:      string(domain.ResourceProcess),
				Connector: "local",
				Config: map[string]any{
					"mode":         "os_service",
					"supervisor":   "launchd",
					"service_name": "com.example.service-api",
				},
			},
		},
	}
	svc := NewResourceRuntimeService(WithResourceRuntimeConfigV2(cfg))

	stopRes, err := svc.StopResource(context.Background(), "service-api")
	if err != nil {
		t.Fatalf("StopResource returned error: %v", err)
	}
	if !stopRes.Success {
		t.Fatalf("StopResource success=false: %s", stopRes.Error)
	}
	if pausectl.IsServicePaused("service-api") {
		t.Fatal("did not expect os_service stop to set dev-session pause flag")
	}
}
