package local

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/service"
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
	if art.ActivationPending {
		// A timed-out wait does not stop launchd's retries. With unchanged
		// installed bytes, a missing activation marker cannot distinguish an
		// old process from one that recovered later. Keep that uncertainty
		// visible without recommending another automatic restart.
		if !art.Stale && state != domain.StateStopped {
			return "inspect", "installed binary activation is unconfirmed; the supervised service may have recovered after the startup wait"
		}
		return "apply", "installed binary has not been confirmed active; activate it with apply, or deploy after source changes"
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

func RecommendedNextStep(action, reason string) string {
	switch action {
	case "inspect":
		return "Run `cerberus resource inspect <resource-id>` and verify the running executable against the installed artifact before retrying activation. A running PID alone does not confirm the installed image; use apply only when another activation is needed."
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

// RecommendedDevSessionAction surfaces staleness for dev_session resources,
// which have no installed artifact and so are invisible to
// RecommendedStatusAction. It targets the most common dev drift — "I rebuilt
// but the dev session is still running the old binary" — using a cheap,
// false-positive-resistant signal: the run binary on disk is newer than the
// running process's start time (recorded in the PID meta file at launch).
//
// It deliberately stays silent for dev sessions WITHOUT a build_strategy
// (e.g. `npm run dev`, `go run`), which hot-reload source and have no built
// binary to compare — flagging those would be noise.
func RecommendedDevSessionAction(id string, spec ProcessSpec, state domain.State) (string, string) {
	if spec.Mode != "" && spec.Mode != ProcessModeDevSession {
		return "", ""
	}
	if !HasBuildStrategy(spec) || !isActiveState(state) {
		return "", ""
	}
	bin, ok := devSessionRunBinary(spec)
	if !ok {
		return "", ""
	}
	fi, err := os.Stat(bin)
	if err != nil {
		return "", ""
	}
	meta, err := service.ReadMetaFile(id)
	if err != nil || meta.StartedAt.IsZero() {
		return "", ""
	}
	if fi.ModTime().After(meta.StartedAt) {
		return "deploy", "a newer build exists on disk than the running dev session; rebuild and restart"
	}
	return "", ""
}

// devSessionRunBinary resolves the built binary a dev session runs, preferring
// the build_strategy declared output, then command[0] when it is a filesystem
// path. It returns ok=false when the command is a launcher (npm, go, bash,
// ...) rather than a built binary, so no staleness signal is produced.
func devSessionRunBinary(spec ProcessSpec) (string, bool) {
	if p, ok := buildStrategyOutputPath(spec); ok {
		return p, true
	}
	if len(spec.Command) == 0 {
		return "", false
	}
	c0 := spec.Command[0]
	switch {
	case filepath.IsAbs(c0):
		return c0, true
	case strings.ContainsRune(c0, filepath.Separator) && spec.Dir != "":
		return filepath.Join(spec.Dir, c0), true
	default:
		return "", false
	}
}

func isActiveState(s domain.State) bool {
	switch s {
	case domain.StateRunning, domain.StateStarting, domain.StateHealthy, domain.StateUnhealthy:
		return true
	default:
		return false
	}
}
