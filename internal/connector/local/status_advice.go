package local

import (
	"fmt"

	"github.com/chrispian/cerberus/internal/domain"
)

// RecommendedStatusAction returns a concise operator action for the current
// local process state when Cerberus has enough information to make one.
func RecommendedStatusAction(spec ProcessSpec, state domain.State, art ArtifactStatus) (string, string) {
	if spec.Mode != ProcessModeOSService || spec.RunFrom != ProcessRunFromArtifact {
		return "", ""
	}
	if !art.Installed {
		return "apply", "installed artifact is missing"
	}
	if !art.Stale {
		return "", ""
	}
	if artifactNeedsDeploy(art.StaleReason) {
		return "deploy", "repo state changed since the installed artifact was last synced"
	}
	switch state {
	case domain.StateRunning, domain.StateStarting, domain.StateHealthy, domain.StateUnhealthy, domain.StateFailed:
		return "apply", "installed artifact is stale while the service is active"
	default:
		return "sync", "installed artifact is stale"
	}
}

func RecommendedNextStep(action, reason string) string {
	switch action {
	case "apply":
		if reason == "installed artifact is missing" {
			return "Run `cerberus resource apply <resource-id>` to install the artifact and load the service."
		}
		if reason != "" {
			return "Run `cerberus resource apply <resource-id>` to sync the current artifact and reload the service."
		}
		return "Run `cerberus resource apply <resource-id>`."
	case "sync":
		return "Run `cerberus resource sync <resource-id>` to update the installed artifact without touching the running service, then apply when you are ready to reload it."
	case "deploy":
		return "Run `cerberus resource deploy <resource-id>` to rebuild from the current repo state, sync the artifact, and activate it."
	case "":
		return ""
	default:
		return fmt.Sprintf("Run `cerberus resource %s <resource-id>`.", action)
	}
}

func artifactNeedsDeploy(reason string) bool {
	switch reason {
	case "repo_root_changed", "repo_head_changed", "repo_worktree_changed":
		return true
	default:
		return false
	}
}
