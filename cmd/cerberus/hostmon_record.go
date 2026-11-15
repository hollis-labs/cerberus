package main

import (
	"context"
	"log/slog"

	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/hostmon"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// opMonitorEvent is the old build's host monitor_event operation (main's cerbapi
// HostDefinition): a read, recorded, never refused.
var opMonitorEvent = contract.Operation{
	Name:        "monitor_event",
	Description: "A record of the host monitor acting on its own: a status change, a failed-probe streak, an hourly summary.",
	Effect:      contract.EffectRead,
	Target:      contract.TargetDescriptor{Kind: "host"},
	Preview:     contract.PreviewNone,
	Output:      contract.OutputStructured,
	Cost:        contract.CostNone,
	LocalFS:     contract.LocalFSNone,
}

// hostMonitorRecorder writes the host monitor's events (status changes, the first failure of
// a streak, the hourly summary, a janitor compaction and the rows it deleted) to the audit
// chain as the old build did: an automation record through the gate, which records it and
// never refuses it. nil, and records nothing, without a gate (observe mode has none).
func hostMonitorRecorder(g *gate.Gate, logger *slog.Logger) hostmon.Recorder {
	if g == nil {
		return nil
	}
	return func(ctx context.Context, what, note string) {
		call, err := g.Begin(ctx, gate.Spec{Connector: "host", Operation: opMonitorEvent.Name, Op: opMonitorEvent, Known: true,
			Automation: true, Config: map[string]any{"event": what}})
		if err != nil {
			logger.Warn("daemon.host_monitor.audit_failed", "event", what, "error", redact.Text(err.Error()))
			return
		}
		call.Note(what + ": " + note)
		call.Finish(nil)
	}
}
