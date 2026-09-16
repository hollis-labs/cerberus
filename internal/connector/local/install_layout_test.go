package local

import (
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/domain"
)

func TestDefaultInstallLayout(t *testing.T) {
	layout, err := DefaultInstallLayout("/Users/chrispian", &domain.Resource{
		ID:        "volon-api",
		ProjectID: "volon",
	}, ProcessSpec{})
	if err != nil {
		t.Fatalf("DefaultInstallLayout failed: %v", err)
	}

	wantRoot := filepath.Join("/Users/chrispian", ".cerberus", "apps", "volon", "volon-api")
	if layout.RootDir != wantRoot {
		t.Fatalf("RootDir = %q, want %q", layout.RootDir, wantRoot)
	}
	if layout.ArtifactPath != filepath.Join(wantRoot, "bin", "volon-api") {
		t.Fatalf("ArtifactPath = %q", layout.ArtifactPath)
	}
	if layout.PlistPath != filepath.Join("/Users/chrispian", "Library", "LaunchAgents", "com.fragments-engine.cerberus.volon.volon-api.plist") {
		t.Fatalf("PlistPath = %q", layout.PlistPath)
	}
}

func TestDefaultInstallLayoutHonorsOverrides(t *testing.T) {
	layout, err := DefaultInstallLayout("/Users/chrispian", &domain.Resource{
		ID:        "Volon API",
		ProjectID: "Volon UI",
	}, ProcessSpec{
		ServiceName:    "com.example.volon.api",
		ArtifactPath:   "/tmp/artifact/volon-api",
		InstallRoot:    "/tmp/cerberus/volon-api",
		InstallWorkDir: "/tmp/cerberus/volon-api/current",
		Dir:            "/workspace/volon",
	})
	if err != nil {
		t.Fatalf("DefaultInstallLayout failed: %v", err)
	}

	if layout.ServiceName != "com.example.volon.api" {
		t.Fatalf("ServiceName = %q", layout.ServiceName)
	}
	if layout.RootDir != "/tmp/cerberus/volon-api" {
		t.Fatalf("RootDir = %q", layout.RootDir)
	}
	if layout.WorkingDir != "/workspace/volon" {
		t.Fatalf("WorkingDir = %q", layout.WorkingDir)
	}
	if layout.ArtifactPath != "/tmp/artifact/volon-api" {
		t.Fatalf("ArtifactPath = %q", layout.ArtifactPath)
	}
}

func TestSanitizeSlug(t *testing.T) {
	if got := sanitizeSlug("Volon UI/API"); got != "volon-ui-api" {
		t.Fatalf("sanitizeSlug = %q", got)
	}
}
