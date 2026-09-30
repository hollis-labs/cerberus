package local

import (
	"fmt"
	"strings"
)

// FormatApplyResultMessage renders an operator-facing message for a resource apply result.
func FormatApplyResultMessage(id string, spec ProcessSpec, res ApplyResult) string {
	if spec.Mode == ProcessModeOSService {
		if supervisor, err := effectiveSupervisor(spec); err == nil && supervisor == ProcessSupervisorSystemdUser {
			return formatSystemdApplyResult(id, res)
		}
	}
	if spec.Mode == ProcessModeOSService && spec.Supervisor == ProcessSupervisorLaunchd {
		switch res.Action {
		case ApplyActionStarted:
			if res.ArtifactChanged {
				return fmt.Sprintf("resource %q applied successfully (artifact synced, launchd loaded)", id)
			}
			return fmt.Sprintf("resource %q applied successfully (launchd loaded)", id)
		case ApplyActionReloaded:
			parts := make([]string, 0, 2)
			if res.ArtifactChanged {
				parts = append(parts, "artifact synced")
			}
			if res.PlistChanged {
				parts = append(parts, "plist updated")
			}
			if len(parts) == 0 {
				parts = append(parts, "activation pending")
			}
			return fmt.Sprintf("resource %q applied successfully (%s, launchd reloaded)", id, strings.Join(parts, ", "))
		case ApplyActionRestarted:
			return fmt.Sprintf("resource %q applied successfully (artifact already current, launchd restarted)", id)
		case ApplyActionNoop:
			return fmt.Sprintf("resource %q already current (launchd unchanged)", id)
		}
	}
	switch res.Action {
	case ApplyActionNoop:
		return fmt.Sprintf("resource %q already current", id)
	case ApplyActionRestarted:
		return fmt.Sprintf("resource %q applied successfully (restarted)", id)
	default:
		return fmt.Sprintf("resource %q applied successfully", id)
	}
}

func formatSystemdApplyResult(id string, res ApplyResult) string {
	switch res.Action {
	case ApplyActionStarted:
		if res.ArtifactChanged {
			return fmt.Sprintf("resource %q applied successfully (artifact synced, systemd unit started)", id)
		}
		return fmt.Sprintf("resource %q applied successfully (systemd unit started)", id)
	case ApplyActionReloaded:
		parts := make([]string, 0, 2)
		if res.ArtifactChanged {
			parts = append(parts, "artifact synced")
		}
		if res.PlistChanged {
			parts = append(parts, "unit updated")
		}
		if len(parts) == 0 {
			parts = append(parts, "activation pending")
		}
		return fmt.Sprintf("resource %q applied successfully (%s, systemd unit restarted)", id, strings.Join(parts, ", "))
	case ApplyActionRestarted:
		return fmt.Sprintf("resource %q applied successfully (artifact already current, systemd unit restarted)", id)
	case ApplyActionNoop:
		return fmt.Sprintf("resource %q already current (systemd unit unchanged)", id)
	}
	return fmt.Sprintf("resource %q applied successfully", id)
}
