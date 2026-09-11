package local

import (
	"fmt"
	"strings"
)

// FormatApplyResultMessage renders an operator-facing message for a resource apply result.
func FormatApplyResultMessage(id string, spec ProcessSpec, res ApplyResult) string {
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
