package app

import (
	"sync/atomic"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/standby"
)

// daemonProcess is set by the daemon body. Only the daemon writes the main
// chain in standby: its fence diverts AuditSwitch to the standby chain. Every
// other process (the CLI's in-process paths, `cerberus web`, `mcp-http`) has no
// fence, so its sink refuses while the flag is present.
var daemonProcess atomic.Bool

// MarkDaemonProcess records that this process is the daemon. Call it before
// the daemon builds any service.
func MarkDaemonProcess() { daemonProcess.Store(true) }

// errStandbyOutsideDaemon is why a process other than the daemon writes no
// audit record in standby. Gated operations are then refused and reads are
// logged rather than recorded (Decision 8).
var errStandbyOutsideDaemon = redact.Guidance("this Cerberus is in standby, so this command wrote no audit record and a gated operation was refused: " +
	"the new Cerberus (the Substrate build) owns every write now. Run the command with the new `cerberus` CLI instead. " +
	"To leave standby, remove ~/.cerberus/standby")

// standbyGuardSink refuses every write while the standby flag is present and
// this process is not the daemon. It reads the flag on each write, so a
// long-running process sees the flag appear and disappear.
type standbyGuardSink struct {
	next  audit.Sink
	paths standby.Paths
}

// Write implements audit.Sink.
func (g standbyGuardSink) Write(r audit.Record) (audit.Record, error) {
	if !daemonProcess.Load() && g.paths.Read().Active {
		return audit.Unavailable{Err: errStandbyOutsideDaemon}.Write(r)
	}
	return g.next.Write(r)
}

// Digest implements audit.Sink.
func (g standbyGuardSink) Digest(v any) string { return g.next.Digest(v) }

// guardStandby wraps the main chain's sink with the standby guard. When the
// flag's path cannot be resolved the main sink cannot be either (both live
// under the home directory), so the sink is returned as is.
func guardStandby(next audit.Sink) audit.Sink {
	p, err := standby.DefaultPaths()
	if err != nil {
		return next
	}
	return standbyGuardSink{next: next, paths: p}
}
