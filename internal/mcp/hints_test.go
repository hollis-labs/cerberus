package mcp

import (
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Every served tool's annotations are the ones its operation's contract
// derives, and they agree with the gate: a client decides whether to ask a
// human from DestructiveHint, and every operation that needs acknowledgment
// is marked destructive. This replaces P0-2's hand-pinned list, which the
// derivation now makes true by construction for every tool.
func TestToolHintsAreDerivedFromTheContract(t *testing.T) {
	for _, tool := range AllTools(nil) {
		op, ok := ToolOperation(tool.Name)
		if !ok {
			t.Errorf("%s has no contract binding", tool.Name)
			continue
		}
		want := contract.HintsFor(op)
		got := contract.ToolHints{ReadOnly: tool.ReadOnlyHint, Destructive: tool.DestructiveHint, Idempotent: tool.IdempotentHint, OpenWorld: tool.OpenWorldHint}
		if got != want {
			t.Errorf("%s: hints %+v, want %+v derived from %s", tool.Name, got, want, op.Name)
		}
		if tool.DestructiveHint != op.RequiresAck || tool.ReadOnlyHint == op.RequiresAck {
			t.Errorf("%s: ReadOnly=%v Destructive=%v disagree with requires_ack=%v", tool.Name, tool.ReadOnlyHint, tool.DestructiveHint, op.RequiresAck)
		}
	}
}

// The cases P0-2 fixed by hand, still true: a tool that overwrites local
// files is not read-only, and mutations a client must ask about are marked.
func TestToolHintsKeepTheP0Fixes(t *testing.T) {
	for _, tc := range []struct {
		tool                  Tool
		readOnly, destructive bool
	}{
		{NewCerberusSSHGetTool(nil), false, true},
		{NewCerberusSSHGetDirTool(nil), false, true},
		{NewCerberusResourceStopTool(nil), false, true},
		{NewCerberusPipelineRunTool(nil), false, true},
		{NewCerberusDockerDownTool(nil), false, true},
		{NewCerberusDockerDestroyTool(nil), false, true},
		{NewCerberusResourceStatusTool(nil), true, false},
		{NewCerberusDockerLogsTool(nil), true, false},
	} {
		if tc.tool.ReadOnlyHint != tc.readOnly || tc.tool.DestructiveHint != tc.destructive {
			t.Errorf("%s: ReadOnlyHint=%v DestructiveHint=%v, want %v/%v", tc.tool.Name, tc.tool.ReadOnlyHint, tc.tool.DestructiveHint, tc.readOnly, tc.destructive)
		}
	}
}
