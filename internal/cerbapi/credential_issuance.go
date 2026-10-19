package cerbapi

import (
	"context"

	"github.com/hollis-labs/cerberus/internal/credentialbroker"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// Called only from execute, after the existing entry point's policy, approval
// and write-ahead audit gate. Preview and plan calls never reserve an ID or
// generate a secret. Values never enter operation arguments, previews or audit.
func (s *ManagedPluginConnectorService) prepareCredentialIssuance(ctx context.Context, id string, args PluginConnectorExecArgs) (*credentialbroker.Dispatch, error) {
	if args.DryRun {
		return nil, nil
	}
	for _, def := range s.Definitions() {
		if def.ID != id {
			continue
		}
		op, ok := def.Operation(args.Operation)
		if !ok || len(op.IssuedSecrets) == 0 {
			return nil, nil
		}
		if err := s.manager.CheckOperation(pluginhost.OperationArgs{Connector: id, Operation: args.Operation, Config: args.Config, Acknowledged: args.Acknowledged}); err != nil {
			return nil, err
		}
		if err := op.CheckInputs(args.Config, true); err != nil {
			return nil, redact.GuidanceWrap(err, "credential issuance inputs refused")
		}
		issuanceID, _ := args.Config["issuance_id"].(string)
		configDigest, entrypointDigest := s.fingerprints(id)
		binding := credentialbroker.Binding{ID: issuanceID, Connector: id, Operation: args.Operation, Digest: s.audit.Digest(map[string]any{"connector": id, "operation": args.Operation, "arguments": args.Config, "config": configDigest, "entrypoint": entrypointDigest})}
		d, err := s.issuance.Prepare(ctx, binding, op.IssuedSecrets)
		if err != nil {
			return nil, redact.GuidanceWrap(err, "credential issuance refused before dispatch")
		}
		for name, value := range d.Values() {
			redact.ScopeFrom(ctx).Add(name, value)
		}
		return d, nil
	}
	return nil, nil
}
