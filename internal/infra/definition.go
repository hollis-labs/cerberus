package infra

import contract "github.com/hollis-labs/cerberus/pkg/connector"

// OpRunProfile runs a deployment profile.
const OpRunProfile = "run_profile"

// Definition is the deployment runner's contract. Running a profile executes
// its preflight, build and deploy commands in a shell, so it is exec; the
// console confirms against the plan PlanDeployment computes, which is why
// its preview is host-computed.
func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            "infra",
		Version:       "builtin",
		ResourceTypes: []string{"deploy_profile"},
		Operations: []contract.Operation{{
			Name:        OpRunProfile,
			Description: "Run a deployment profile's preflight, build, link and deploy commands.",
			Effect:      contract.EffectExec,
			Target:      contract.TargetDescriptor{Kind: "infra.deploy_profile", From: []string{"id"}},
			Preview:     contract.PreviewHost,
			Output:      contract.OutputFreeText,
			Cost:        contract.CostNone,
			LocalFS:     contract.LocalFSWrites,
			Inputs:      []contract.Input{contract.RequiredField("id", contract.StringSchema("ID of a saved deployment profile."))},
		}},
	})
}
