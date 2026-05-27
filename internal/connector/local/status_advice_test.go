package local

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/service"
)

func TestRecommendedDevSessionAction(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	appDir := t.TempDir()
	bin := filepath.Join(appDir, "app")
	if err := os.WriteFile(bin, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := ProcessSpec{
		Dir:           appDir,
		Command:       []string{"./app", "serve"},
		Mode:          ProcessModeDevSession,
		BuildStrategy: &BuildStrategyConfig{Kind: "make_standard", Rules: map[string]any{}},
	}
	start := time.Now().Add(-time.Hour)

	// Binary rebuilt after the process started -> stale -> deploy.
	if err := service.WriteMetaFile("devsvc", service.PIDMeta{PID: 1, StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(bin, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if action, _ := RecommendedDevSessionAction("devsvc", spec, domain.StateRunning); action != "deploy" {
		t.Fatalf("stale dev session: action = %q, want deploy", action)
	}

	// Binary older than process start -> current -> no action.
	if err := service.WriteMetaFile("devsvc", service.PIDMeta{PID: 1, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(bin, start, start); err != nil {
		t.Fatal(err)
	}
	if action, _ := RecommendedDevSessionAction("devsvc", spec, domain.StateRunning); action != "" {
		t.Fatalf("current dev session: action = %q, want none", action)
	}

	// No build_strategy (hot-reloader) -> always silent.
	noBuild := spec
	noBuild.BuildStrategy = nil
	if action, _ := RecommendedDevSessionAction("devsvc", noBuild, domain.StateRunning); action != "" {
		t.Fatalf("hot-reloader: action = %q, want none", action)
	}

	// Stopped -> silent even if stale.
	if action, _ := RecommendedDevSessionAction("devsvc", spec, domain.StateStopped); action != "" {
		t.Fatalf("stopped dev session: action = %q, want none", action)
	}
}

func TestRecommendedStatusAction(t *testing.T) {
	spec := ProcessSpec{
		Mode:    ProcessModeOSService,
		RunFrom: ProcessRunFromArtifact,
	}
	tests := []struct {
		name   string
		state  domain.State
		art    ArtifactStatus
		action string
		reason string
	}{
		{
			name:   "missing artifact recommends apply",
			state:  domain.StateStopped,
			art:    ArtifactStatus{},
			action: "apply",
			reason: "installed artifact is missing",
		},
		{
			name:   "stale running recommends apply",
			state:  domain.StateRunning,
			art:    ArtifactStatus{Installed: true, Stale: true},
			action: "apply",
			reason: "installed artifact is stale while the service is active",
		},
		{
			name:   "stale stopped recommends sync",
			state:  domain.StateStopped,
			art:    ArtifactStatus{Installed: true, Stale: true},
			action: "sync",
			reason: "installed artifact is stale",
		},
		{
			name:   "source_changed running recommends apply",
			state:  domain.StateRunning,
			art:    ArtifactStatus{Installed: true, Stale: true, StaleReason: "source_changed"},
			action: "apply",
			reason: "installed artifact is stale while the service is active",
		},
		{
			name:   "current artifact no recommendation",
			state:  domain.StateRunning,
			art:    ArtifactStatus{Installed: true, Stale: false},
			action: "",
			reason: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			action, reason := RecommendedStatusAction(spec, tc.state, tc.art)
			if action != tc.action || reason != tc.reason {
				t.Fatalf("got (%q, %q), want (%q, %q)", action, reason, tc.action, tc.reason)
			}
		})
	}
}

func TestRecommendedNextStepDeploy(t *testing.T) {
	got := RecommendedNextStep("deploy", "repo state changed since the installed artifact was last synced")
	want := "Run `cerberus resource deploy <resource-id>` to rebuild from the current repo state, sync the artifact, and activate it."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
