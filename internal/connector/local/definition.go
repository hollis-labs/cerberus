package local

import contract "github.com/hollis-labs/cerberus/pkg/connector"

// Operation names of the supervision lane's mutations. The resource runtime
// service gates each call on the contract declared for it here.
const (
	OpDeploy = "deploy"
	OpApply  = "apply"
	OpReload = "reload"
	OpStop   = "stop"
	OpSync   = "sync"
	OpRemove = "remove"

	// OpEnsureFresh chooses deploy, apply or sync from the resource's
	// runtime advice and runs it. It is gated by the operation it chooses;
	// its own contract is that of the most reaching choice, deploy.
	OpEnsureFresh = "ensure_fresh"

	// Reads of the supervision lane. Not gated: they change nothing.
	OpList    = "list"
	OpStatus  = "status"
	OpInspect = "inspect"
	OpDoctor  = "doctor"
	OpLogs    = "logs"
)

// Resource mutation input keys.
const (
	InputID                        = "id"
	InputInstallAfterBuildOverride = "install_after_build_override"
)

// Definition is the local connector's contract: the six mutations the
// resource runtime service runs on a supervised local workload (Decision 11).
// Every one of them needs acknowledgment (Decision 14). Supervision itself —
// auto_restart, the resource monitor's health-driven restarts — is not an
// operation request and does not pass through it.
func Definition() contract.Definition {
	target := contract.TargetDescriptor{Kind: "local.resource", From: []string{InputID}}
	id := contract.RequiredField(InputID, contract.StringSchema("ID of a configured local resource (see `cerberus resource list`)."))
	op := func(name, description string, effect contract.Effect, reversible bool, localFS contract.LocalFS, extra ...contract.Input) contract.Operation {
		return contract.Operation{
			Name:        name,
			Description: description,
			Effect:      effect,
			Reversible:  reversible,
			Target:      target,
			Preview:     contract.PreviewNone,
			Output:      contract.OutputStructured,
			Cost:        contract.CostNone,
			LocalFS:     localFS,
			Inputs:      append([]contract.Input{id}, extra...),
		}
	}
	return contract.Finalize(contract.Definition{
		ID:            "local",
		Version:       "builtin",
		ResourceTypes: []string{"process"},
		Operations: []contract.Operation{
			// deploy builds and installs a new artifact over the running one,
			// then applies it. The previous artifact is not kept, so it is
			// not reversible from here.
			op(OpDeploy, "Build the resource from source, install the artifact and apply it.", contract.EffectLifecycle, false, contract.LocalFSWrites,
				contract.Field(InputInstallAfterBuildOverride, map[string]any{"type": "boolean", "description": "Override install_after_build for this deploy."})),
			op(OpApply, "Apply the installed resource through its runtime backend: write the launchd job or start the dev session.", contract.EffectLifecycle, true, contract.LocalFSWrites),
			op(OpReload, "Restart the installed resource without rebuilding or syncing.", contract.EffectLifecycle, true, contract.LocalFSNone),
			op(OpStop, "Stop the resource, keeping its install state.", contract.EffectLifecycle, true, contract.LocalFSNone),
			// sync copies the built artifact into the install directory and
			// touches nothing that runs: it changes installed state without a
			// lifecycle event, which is a write.
			op(OpSync, "Copy the built artifact into the install directory without touching the runtime backend.", contract.EffectWrite, false, contract.LocalFSWrites),
			op(OpRemove, "Uninstall the resource's runtime state.", contract.EffectDestructive, false, contract.LocalFSWrites),
			op(OpEnsureFresh, "Run deploy, apply or sync, whichever the resource's runtime advice recommends.", contract.EffectLifecycle, false, contract.LocalFSWrites,
				contract.Field("force", map[string]any{"type": "boolean", "description": "Always deploy (rebuild)."})),
			op(OpStatus, "Read the resource's runtime status and recommended next action.", contract.EffectRead, false, contract.LocalFSNone),
			op(OpInspect, "Read the resource's runtime details, install paths and log paths.", contract.EffectRead, false, contract.LocalFSNone),
			op(OpDoctor, "Run the resource's runtime and install checks.", contract.EffectRead, false, contract.LocalFSNone),
			{
				Name: OpList, Description: "List configured resources.",
				Effect: contract.EffectRead, Target: contract.TargetDescriptor{Kind: "local.registry"},
				Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone,
				Inputs: []contract.Input{contract.Field("project", contract.StringSchema("Filter by project ID."))},
			},
			// Logs are text the workload wrote, which can carry anything.
			func() contract.Operation {
				logs := op(OpLogs, "Read recent lines of the resource's log.", contract.EffectReadSensitive, false, contract.LocalFSNone,
					contract.Field("lines", contract.IntegerSchema("Number of lines.")), contract.Field("stream", contract.StringSchema("stdout or stderr.")))
				logs.Output = contract.OutputFreeText
				return logs
			}(),
		},
	})
}
