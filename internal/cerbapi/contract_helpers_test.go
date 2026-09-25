package cerbapi

import (
	"context"
	"github.com/hollis-labs/cerberus/internal/audit"

	"github.com/hollis-labs/cerberus/internal/connector"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// keysInScope is every input of def's operations in the given scope: what a
// caller on that side of the local/remote line may send.
func keysInScope(def contract.Definition, scope contract.InputScope) map[string]bool {
	out := map[string]bool{}
	for _, op := range def.Operations {
		for _, in := range op.Inputs {
			if in.Scope == scope || (scope == contract.InputCaller && in.Scope == "") {
				out[in.Name] = true
			}
		}
	}
	return out
}

// sshOperationFields and dockerCallerFields are the keys a socket, web or MCP
// caller may send to ssh and docker, read from the connectors' key tables.
var sshOperationFields = keysInScope(sshconn.Definition(), contract.InputCaller)

func dockerCallerFields() map[string]bool {
	return keysInScope(dockerconn.Definition(), contract.InputCaller)
}

// checkFromSurface runs the contract gate as a caller on surface would meet
// it, with nothing resolved: only the connector's definition is registered.
func checkFromSurface(surface CallerSurface, args ExternalConnectorOperationArgs) error {
	registry := connector.NewRegistry()
	for _, def := range []contract.Definition{sshconn.Definition(), dockerconn.Definition()} {
		registry.RegisterDefinition(def)
	}
	_, err := NewExternalConnectorService(audit.NewMemory(), registry).declaredOperation(WithCallerSurface(context.Background(), surface), args)
	return err
}

// RefuseAdHocDockerTarget is the socket's view of a docker call: what the
// key table refuses from a caller that is not the operator's own shell.
func RefuseAdHocDockerTarget(args ExternalConnectorOperationArgs) error {
	return checkFromSurface(SurfaceSocket, args)
}
