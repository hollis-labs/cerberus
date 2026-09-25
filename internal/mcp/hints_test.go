package mcp

import "testing"

// TestToolHintsMatchWhatTheToolsDo pins the hints fixed in P0-2: a client
// decides whether to ask a human from these, so a mutating tool marked
// non-destructive, or a tool that overwrites local files marked read-only,
// skips the question.
func TestToolHintsMatchWhatTheToolsDo(t *testing.T) {
	for _, tc := range []struct {
		tool        Tool
		readOnly    bool
		destructive bool
	}{
		{NewCerberusResourceStopTool(nil), false, true},
		{NewCerberusResourceDeployTool(nil), false, true},
		{NewCerberusResourceEnsureFreshTool(nil), false, true},
		{NewCerberusResourceApplyTool(nil), false, true},
		{NewCerberusResourceSyncTool(nil), false, true},
		{NewCerberusResourceReloadTool(nil), false, true},
		{NewCerberusResourceRemoveTool(nil), false, true},
		{NewCerberusPipelineRunTool(nil), false, true},
		{NewCerberusDropletCreateTool(nil), false, true},
		{NewCerberusDropletStopTool(nil), false, true},
		{NewCerberusDropletDestroyTool(nil), false, true},
		{NewCerberusDropletStartTool(nil), false, false},
		{NewCerberusSSHGetTool(nil, nil), false, true},
		{NewCerberusSSHGetDirTool(nil, nil), false, true},
		{NewCerberusSSHPutTool(nil, nil), false, true},
		// docker_down is compose stop / docker stop: nothing is removed.
		{NewCerberusDockerDownTool(nil), false, false},
	} {
		if tc.tool.ReadOnlyHint != tc.readOnly || tc.tool.DestructiveHint != tc.destructive {
			t.Errorf("%s: ReadOnlyHint=%v DestructiveHint=%v, want %v/%v",
				tc.tool.Name, tc.tool.ReadOnlyHint, tc.tool.DestructiveHint, tc.readOnly, tc.destructive)
		}
	}
}
