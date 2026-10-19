package cerbapi

import contract "github.com/hollis-labs/cerberus/pkg/connector"

// Control-plane read operations: what Cerberus reports about itself.
const (
	OpHealth            = "health"
	OpPortfolioHealth   = "portfolio_health"
	OpProjectList       = "project_list"
	OpConnectorList     = "connector_list"
	OpConnectorDescribe = "connector_describe"
	// OpApprovalWait waits, for a bounded time, for an approval to change
	// state (P3-6): what an agent calls after approval_pending.
	OpApprovalWait = "approval_wait"
	// OpLockdown engages the lockdown (§12): self-restriction, so it needs
	// no acknowledgment; nothing lifts it but a person on a terminal.
	OpLockdown = "lockdown"
)

// ControlPlaneDefinition is the contract of the control plane's own reads —
// daemon health, the project registry and connector discovery — so the
// tools that serve them derive their annotations the way every other tool
// does. All are read; none is gated.
func ControlPlaneDefinition() contract.Definition {
	read := func(name, description, kind string, inputs ...contract.Input) contract.Operation {
		return contract.Operation{
			Name: name, Description: description, Effect: contract.EffectRead,
			Target:  contract.TargetDescriptor{Kind: kind},
			Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone,
			Inputs: inputs,
		}
	}
	return contract.Finalize(contract.Definition{
		ID:            "cerberus",
		Version:       "builtin",
		ResourceTypes: []string{"control_plane"},
		Operations: []contract.Operation{
			read(OpHealth, "Report daemon and resource health.", "cerberus.daemon", contract.Field("resource_id", contract.StringSchema("Limit to one resource."))),
			read(OpPortfolioHealth, "Report combined portfolio health projection across hosts, projects, deployments, shared containers, and connectors.", "cerberus.portfolio",
				contract.Field("mode", contract.StringSchema("Observation mode: cached or live_bounded.")),
				contract.Field("project", contract.StringSchema("Optional project ID filter.")),
				contract.Field("host", contract.StringSchema("Optional host name filter.")),
				contract.Field("kind", contract.StringSchema("Optional deployment kind filter.")),
				contract.Field("rollup_status", contract.StringSchema("Optional rollup status filter.")),
				contract.Field("include_unsupervised", map[string]any{"type": "boolean", "description": "Include unsupervised connector handles (default true)."}),
			),
			read(OpProjectList, "List registered projects.", "cerberus.registry"),
			read(OpConnectorList, "List connectors.", "cerberus.connectors"),
			read(OpConnectorDescribe, "Describe one connector's contract.", "cerberus.connectors", contract.RequiredField("id", contract.StringSchema("Connector ID."))),
			read(OpApprovalWait, "Wait for an approval to be decided, up to a bounded time.", "cerberus.approvals",
				contract.RequiredField("id", contract.StringSchema("Approval ID, from approval_pending.")),
				contract.Field("timeout_seconds", contract.IntegerSchema("How long to wait, 1 to 60 seconds; 30 when omitted."))),
			{
				Name: OpLockdown, Description: "Engage the lockdown: every operation but a plain read is refused until a person lifts it.",
				Effect: contract.EffectWrite, Target: contract.TargetDescriptor{Kind: "cerberus.brakes"},
				Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSWrites,
				Inputs: []contract.Input{contract.RequiredField("reason", contract.StringSchema("Why, for the operator and the record."))},
			},
		},
	})
}
