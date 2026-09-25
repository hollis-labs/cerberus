package cerbapi

import (
	"context"
	"github.com/hollis-labs/cerberus/internal/audit"
	"runtime"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/pausectl"
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
	svc := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(cfg))

	stopRes, err := svc.StopResource(context.Background(), "dev-api", WithAcknowledged(true))
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

	applyRes, err := svc.ApplyResource(context.Background(), "dev-api", WithAcknowledged(true))
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
	svc := NewResourceRuntimeService(audit.NewMemory(), WithResourceRuntimeConfigV2(cfg))

	stopRes, err := svc.StopResource(context.Background(), "service-api", WithAcknowledged(true))
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

func TestResolveInstallAfterBuild(t *testing.T) {
	t.Parallel()

	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name          string
		rawCfg        map[string]any
		resourceVal   bool
		globalDefault bool
		override      *bool
		want          bool
	}{
		{
			name:          "empty resource config + default-true global = true",
			rawCfg:        map[string]any{},
			resourceVal:   false,
			globalDefault: true,
			override:      nil,
			want:          true,
		},
		{
			name:          "empty resource config + default-false global = false",
			rawCfg:        map[string]any{},
			resourceVal:   false,
			globalDefault: false,
			override:      nil,
			want:          false,
		},
		{
			name:          "resource explicit false overrides default-true global",
			rawCfg:        map[string]any{"install_after_build": false},
			resourceVal:   false,
			globalDefault: true,
			override:      nil,
			want:          false,
		},
		{
			name:          "resource explicit true overrides default-false global",
			rawCfg:        map[string]any{"install_after_build": true},
			resourceVal:   true,
			globalDefault: false,
			override:      nil,
			want:          true,
		},
		{
			name:          "CLI override true beats resource false",
			rawCfg:        map[string]any{"install_after_build": false},
			resourceVal:   false,
			globalDefault: true,
			override:      boolPtr(true),
			want:          true,
		},
		{
			name:          "CLI override false beats resource true",
			rawCfg:        map[string]any{"install_after_build": true},
			resourceVal:   true,
			globalDefault: true,
			override:      boolPtr(false),
			want:          false,
		},
		{
			name:          "CLI override false beats default-true global when resource absent",
			rawCfg:        map[string]any{},
			resourceVal:   false,
			globalDefault: true,
			override:      boolPtr(false),
			want:          false,
		},
		{
			name:          "nil rawCfg falls through to global default",
			rawCfg:        nil,
			resourceVal:   false,
			globalDefault: true,
			override:      nil,
			want:          true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveInstallAfterBuild(tc.rawCfg, tc.resourceVal, tc.globalDefault, tc.override)
			if got != tc.want {
				t.Fatalf("ResolveInstallAfterBuild() = %v, want %v", got, tc.want)
			}
		})
	}
}
