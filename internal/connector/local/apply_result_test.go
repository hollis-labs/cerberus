package local

import "testing"

func TestFormatApplyResultMessage(t *testing.T) {
	spec := ProcessSpec{
		Mode:       ProcessModeOSService,
		Supervisor: ProcessSupervisorLaunchd,
	}
	tests := []struct {
		name string
		res  ApplyResult
		want string
	}{
		{
			name: "started",
			res:  ApplyResult{Action: ApplyActionStarted, ArtifactChanged: true},
			want: `resource "demo" applied successfully (artifact synced, launchd loaded)`,
		},
		{
			name: "reloaded",
			res:  ApplyResult{Action: ApplyActionReloaded, ArtifactChanged: true, PlistChanged: true},
			want: `resource "demo" applied successfully (artifact synced, plist updated, launchd reloaded)`,
		},
		{
			name: "restarted",
			res:  ApplyResult{Action: ApplyActionRestarted},
			want: `resource "demo" applied successfully (artifact already current, launchd restarted)`,
		},
		{
			name: "noop",
			res:  ApplyResult{Action: ApplyActionNoop},
			want: `resource "demo" already current (launchd unchanged)`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatApplyResultMessage("demo", spec, tc.res); got != tc.want {
				t.Fatalf("message = %q, want %q", got, tc.want)
			}
		})
	}
}
