package cerbapi

import contract "github.com/hollis-labs/cerberus/pkg/connector"

// Control-plane read operations: what Cerberus reports about itself.
const (
	OpHealth            = "health"
	OpProjectList       = "project_list"
	OpConnectorList     = "connector_list"
	OpConnectorDescribe = "connector_describe"
	// OpApprovalWait waits, for a bounded time, for an approval to change
	// state (P3-6): what an agent calls after approval_pending.
	OpApprovalWait = "approval_wait"
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
			read(OpProjectList, "List registered projects.", "cerberus.registry"),
			read(OpConnectorList, "List connectors.", "cerberus.connectors"),
			read(OpConnectorDescribe, "Describe one connector's contract.", "cerberus.connectors", contract.RequiredField("id", contract.StringSchema("Connector ID."))),
			read(OpApprovalWait, "Wait for an approval to be decided, up to a bounded time.", "cerberus.approvals",
				contract.RequiredField("id", contract.StringSchema("Approval ID, from approval_pending.")),
				contract.Field("timeout_seconds", contract.IntegerSchema("How long to wait, 1 to 60 seconds; 30 when omitted."))),
		},
	})
}
