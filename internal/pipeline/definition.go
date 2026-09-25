package pipeline

import contract "github.com/hollis-labs/cerberus/pkg/connector"

// Pipeline operations.
const (
	OpRun  = "run"
	OpList = "list"
)

// Definition is the pipeline runner's contract. A run is exec (Decision 11):
// a stage can be a shell action (`sh -c`), and a static contract cannot see
// which stages a given pipeline declares, so every run is classed by what
// the most reaching stage could do.
func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            "pipeline",
		Version:       "builtin",
		ResourceTypes: []string{"pipeline"},
		Operations: []contract.Operation{{
			Name:        OpRun,
			Description: "Run a pipeline's stages: build, deploy, start, stop, health_wait and shell actions.",
			Effect:      contract.EffectExec,
			Target:      contract.TargetDescriptor{Kind: "pipeline", From: []string{"id"}},
			Preview:     contract.PreviewNone,
			Output:      contract.OutputFreeText,
			Cost:        contract.CostNone,
			LocalFS:     contract.LocalFSWrites,
			Inputs:      []contract.Input{contract.RequiredField("id", contract.StringSchema("ID of a configured pipeline (see `cerberus pipeline list`)."))},
		}, {
			Name:        OpList,
			Description: "List configured pipelines.",
			Effect:      contract.EffectRead,
			Target:      contract.TargetDescriptor{Kind: "pipeline.registry"},
			Preview:     contract.PreviewNone,
			Output:      contract.OutputStructured,
			Cost:        contract.CostNone,
			LocalFS:     contract.LocalFSNone,
		}},
	})
}
