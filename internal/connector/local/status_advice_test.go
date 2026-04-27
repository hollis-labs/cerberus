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
