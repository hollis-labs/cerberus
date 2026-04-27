package local

import (
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/domain"
)

func TestDefaultInstallLayout(t *testing.T) {
	layout, err := DefaultInstallLayout("/Users/me", &domain.Resource{
		ID:        "app-h-api",
		ProjectID: "app-h",
	}, ProcessSpec{})
	if err != nil {
		t.Fatalf("DefaultInstallLayout failed: %v", err)
	}

	wantRoot := filepath.Join("/Users/me", ".cerberus", "apps", "app-h", "app-h-api")
	if layout.RootDir != wantRoot {
		t.Fatalf("RootDir = %q, want %q", layout.RootDir, wantRoot)
	}
	if layout.ArtifactPath != filepath.Join(wantRoot, "bin", "app-h-api") {
		t.Fatalf("ArtifactPath = %q", layout.ArtifactPath)
	}
	if layout.PlistPath != filepath.Join("/Users/me", "Library", "LaunchAgents", "com.fragments-engine.cerberus.app-h.app-h-api.plist") {
		t.Fatalf("PlistPath = %q", layout.PlistPath)
	}
}

func TestDefaultInstallLayoutHonorsOverrides(t *testing.T) {
	layout, err := DefaultInstallLayout("/Users/me", &domain.Resource{
		ID:        "app-h API",
		ProjectID: "app-h UI",
	}, ProcessSpec{
		ServiceName:    "com.example.app-h.api",
		ArtifactPath:   "/tmp/artifact/app-h-api",
		InstallRoot:    "/tmp/cerberus/app-h-api",
		InstallWorkDir: "/tmp/cerberus/app-h-api/current",
		Dir:            "/workspace/app-h",
	})
	if err != nil {
		t.Fatalf("DefaultInstallLayout failed: %v", err)
	}

	if layout.ServiceName != "com.example.app-h.api" {
		t.Fatalf("ServiceName = %q", layout.ServiceName)
	}
	if layout.RootDir != "/tmp/cerberus/app-h-api" {
		t.Fatalf("RootDir = %q", layout.RootDir)
	}
	if layout.WorkingDir != "/workspace/app-h" {
		t.Fatalf("WorkingDir = %q", layout.WorkingDir)
	}
	if layout.ArtifactPath != "/tmp/artifact/app-h-api" {
		t.Fatalf("ArtifactPath = %q", layout.ArtifactPath)
	}
}

func TestSanitizeSlug(t *testing.T) {
	if got := sanitizeSlug("app-h UI/API"); got != "app-h-ui-api" {
		t.Fatalf("sanitizeSlug = %q", got)
	}
}
