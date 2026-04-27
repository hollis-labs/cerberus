package local

import "github.com/chrispian/cerberus/internal/domain"

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
	switch state {
	case domain.StateRunning, domain.StateStarting, domain.StateHealthy, domain.StateUnhealthy, domain.StateFailed:
		return "apply", "installed artifact is stale while the service is active"
	default:
		return "sync", "installed artifact is stale"
	}
}
