package api

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/policy"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// PolicyOptions is what the policy and posture routes read and write: the operator's policy
// files (the config dir's policy/, with the audit dir a snapshot that fails its hash check is
// enforced from) and every operation Cerberus can be asked to run, for the sampled decisions.
type PolicyOptions struct {
	Store policy.Store
	// Definitions are the connectors' definitions: the admin lane's built-ins and plugins, the
	// local runtime and pipelines, as old's policyDefinitions listed them.
	Definitions func(context.Context) []contract.Definition
}
