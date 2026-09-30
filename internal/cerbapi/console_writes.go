package cerbapi

import (
	"context"
	"log/slog"

	"github.com/hollis-labs/cerberus/internal/audit"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// ConsoleWrite is a change the web console makes to Cerberus's own state:
// a deploy profile saved or deleted, a project config registered or
// deregistered, a config backup restored, a provider's settings saved.
// Profiles carry shell commands and target labels, and labels decide which
// approval channel a call needs, so each of these is an admin operation:
// gated like any call and recorded (M9).
type ConsoleWrite struct {
	// Operation names the change, for example "profile_save".
	Operation string
	// Target names what it changes: names and ids, never values.
	Target map[string]any
	// Config is the change as the request described it, recorded as a
	// keyed digest, never in the clear.
	Config map[string]any
}

// consoleWriteOperation is a console write's contract: admin, local files.
func consoleWriteOperation(name string) contract.Operation {
	return contract.Operation{
		Name: name, Effect: contract.EffectAdmin,
		// The labels and paths are names, recorded in the clear: a relabelled
		// target is visible in the record, not only as a digest.
		Target:  contract.TargetDescriptor{Kind: "console", From: []string{"id", "env", "owner", "admin", "config_path", "backup_path"}},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSWrites,
	}.Finalize()
}

// RunConsoleWrite runs do as the console write w: through the gate (a
// verified caller's scopes, the brakes, policy) with its intent recorded
// first, and its outcome after. An unwritable log refuses the write, as for
// any non-read. The caller's principal is the console session on ctx.
func RunConsoleWrite(ctx context.Context, sink audit.Sink, w ConsoleWrite, do func(context.Context) error) error {
	config := map[string]any{}
	for k, v := range w.Target {
		config[k] = v
	}
	for k, v := range w.Config {
		if _, taken := config[k]; !taken {
			config[k] = v
		}
	}
	spec := auditSpec{connector: "console", operation: w.Operation, op: consoleWriteOperation(w.Operation), known: true, acknowledged: true, config: config}
	call, err := beginGated(ctx, sink, slog.Default(), spec)
	if err != nil {
		return err
	}
	err = do(ctx)
	call.finish(err)
	return err
}
