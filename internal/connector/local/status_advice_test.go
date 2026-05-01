package local

import (
	"testing"

	"github.com/chrispian/cerberus/internal/domain"
)

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
			name:   "repo drift recommends deploy",
			state:  domain.StateRunning,
			art:    ArtifactStatus{Installed: true, Stale: true, StaleReason: "repo_worktree_changed"},
			action: "deploy",
			reason: "repo state changed since the installed artifact was last synced",
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
