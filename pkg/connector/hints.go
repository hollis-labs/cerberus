package connector

import "strings"

// ToolHints is the MCP tool-annotation set, derived from an operation's
// contract. Hand-written hints are how an operation's annotation came to
// disagree with its gate (Fix first item 8), so every Cerberus tool —
// built-in or generated from a plugin manifest — takes its hints from here.
type ToolHints struct {
	ReadOnly    bool
	Destructive bool
	Idempotent  bool
	OpenWorld   bool
}

// closedWorldTargets are target kinds inside Cerberus itself: its own
// supervised workloads, pipelines and control plane. Everything else — a
// provider API, a remote host, a Docker daemon — is an external system.
var closedWorldTargets = []string{"local.", "pipeline", "cerberus."}

// HintsFor derives an operation's MCP annotations from its contract:
//
//   - ReadOnly: the operation needs no acknowledgment — its effect only
//     reads, and it writes nothing to the local filesystem. ssh get reads a
//     remote file but overwrites a local path, so it is not read-only.
//   - Destructive: not read-only. A client decides whether to ask a human
//     from this hint, and every operation that is not read-only needs
//     acknowledgment (Decision 14), so the hint says the same as the gate:
//     DestructiveHint == RequiresAck. It is also MCP's own default for a
//     tool that is not read-only. Reversibility is not a reason to skip the
//     question — a "reversible" droplet stop still takes down what it
//     serves.
//   - Idempotent: only a read claims it. The contract does not record
//     idempotence, and claiming it falsely invites a client to retry a
//     mutation, so the derivation never does.
//   - OpenWorld: the target is outside Cerberus — a provider, a remote host,
//     a Docker daemon — rather than its own workloads and pipelines.
//
// A plugin operation's effective contract (Manifest operation's Operation())
// goes through the same function, so a generated tool gets the same hints a
// built-in would.
func HintsFor(op Operation) ToolHints {
	op = op.Finalize()
	readOnly := !op.RequiresAck
	return ToolHints{
		ReadOnly:    readOnly,
		Destructive: !readOnly,
		Idempotent:  readOnly,
		OpenWorld:   openWorld(op.Target.Kind),
	}
}

func openWorld(kind string) bool {
	for _, prefix := range closedWorldTargets {
		if strings.HasPrefix(kind, prefix) {
			return false
		}
	}
	return true
}
