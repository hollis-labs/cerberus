package cerbapi

import (
	"context"
	"sync/atomic"

	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// inProcessStateCheck says whether this process reads Cerberus's real
// state: the brakes, policy, approvals and audit log live under the
// account's home, and the CLI resolves them from $HOME. A CLI run with
// HOME pointed elsewhere, which --config sends in-process, would run a
// mutation under a scratch lockdown, a scratch policy and a scratch audit
// log (M11). The CLI installs the check; nothing else runs mutations
// in-process on the operator's behalf.
var inProcessStateCheck atomic.Pointer[func() error]

// SetInProcessStateCheck installs the check, or with nil removes it.
func SetInProcessStateCheck(check func() error) {
	if check == nil {
		inProcessStateCheck.Store(nil)
		return
	}
	inProcessStateCheck.Store(&check)
}

// inProcessStateRefusal refuses an in-process mutation when the check says
// this process's state is not the real one. Reads, dry runs, plans and
// automation pass; so does everything through the daemon, which reads its
// own state.
func inProcessStateRefusal(ctx context.Context, spec auditSpec) error {
	check := inProcessStateCheck.Load()
	if check == nil || CallerSurfaceFrom(ctx) != SurfaceInProcess || spec.automation || spec.dryRun || spec.planOnly {
		return nil
	}
	if spec.known && (spec.op.Effect == contract.EffectRead || spec.op.Effect == contract.EffectReadSensitive) {
		return nil
	}
	err := (*check)()
	if err == nil {
		return nil
	}
	return externalConnectorError(ExternalConnectorOperationArgs{Connector: spec.connector, Operation: spec.operation}, ExternalConnectorAuditUnavailable,
		redact.GuidanceWrap(err, "%s %s did not run: run in this process, it would be braked, authorized and recorded by state other than Cerberus's real state", spec.connector, spec.operation))
}
