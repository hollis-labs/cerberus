package namecheap

import contract "github.com/hollis-labs/cerberus/pkg/connector"

// DisabledOperations are the contracts of the two per-record writes Cerberus
// refuses (ErrUnsafePerRecordWrite): what they would do if they ran. They are
// not in Definition(), so nothing dispatches them; they exist so the MCP
// tools that explain the refusal carry annotations derived like every other
// tool's, and so conformance checks them.
func DisabledOperations() []contract.Operation {
	domain := contract.RequiredField("domain", contract.StringSchema("Domain name, such as example.com."))
	target := contract.TargetDescriptor{Kind: "namecheap.domain", From: []string{"domain"}}
	ops := []contract.Operation{
		{
			Name: "create_dns_record", Description: "Disabled: create one DNS record.",
			Effect: contract.EffectWrite, Reversible: false, Target: target,
			Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone,
			Inputs: []contract.Input{domain},
		},
		{
			Name: "delete_dns_record", Description: "Disabled: delete one DNS record.",
			Effect: contract.EffectDestructive, Target: target,
			Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone,
			Inputs: []contract.Input{domain},
		},
	}
	for i := range ops {
		ops[i] = ops[i].Finalize()
	}
	return ops
}
