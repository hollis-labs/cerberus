package cerbapi

import (
	"context"
	"errors"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// OpHostInventory collects a host's physical facts and records them.
const OpHostInventory = "host_inventory"

// InventoryArgs asks for one host to be inventoried. Revision is optional: the
// collection gathers its own data, so with none the current revision is used
// and the write is retried if another writer got in first.
type InventoryArgs struct {
	WriteArgs
	Name string `json:"name"`
	// DryRun collects and shows what would change, and writes nothing.
	DryRun bool `json:"dry_run,omitempty"`
}

// InventoryResult is the write result and what the collection found.
type InventoryResult struct {
	WriteResult
	DryRun bool `json:"dry_run,omitempty"`
}

// inventoryExecOperation is what the audit log records the remote collection
// under: a read of privileged facts over ssh.
func inventoryExecOperation() contract.Operation {
	return contract.Operation{
		Name: "inventory", Effect: contract.EffectReadSensitive,
		Target:  contract.TargetDescriptor{Kind: "host", From: []string{"name"}},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone,
	}.Finalize()
}

// Inventory runs the read-only collection script on the host over ssh
// (`sudo -n` for the root-only facts, where the host allows it) and records
// the result through the same write path as every other change: validated as
// a whole file, backed up, audited and put in the history. Facts an operator
// set (notes, storage_slots, vendor-sourced ceilings, components) are kept.
func (s *HostService) Inventory(ctx context.Context, a InventoryArgs) (InventoryResult, error) {
	if s == nil {
		return InventoryResult{}, errors.New("host service is not configured")
	}
	declared, err := s.get(a.Name)
	if err != nil {
		return InventoryResult{}, err
	}
	var collected hosts.Collected
	var warnings []string
	res, err := s.write(ctx, writePlan{op: OpHostInventory, kind: "host", name: a.Name, args: a.WriteArgs,
		config:       map[string]any{"name": a.Name, "dry_run": a.DryRun},
		autoRevision: true, dryRun: a.DryRun,
		prepare: func(ctx context.Context, call *auditCall) error {
			c, err := s.collect(ctx, call, declared.Host)
			collected = c
			return err
		},
		mutate: func(f *hosts.File) error {
			_, w, err := hosts.MergeInventory(f, a.Name, collected, s.now())
			warnings = w
			return err
		},
		entity: hostEntity(a.Name), hosts: hostOnly(a.Name)})
	res.Warnings = append(warnings, res.Warnings...)
	return InventoryResult{WriteResult: res, DryRun: a.DryRun}, err
}

// collect runs the inventory script on the host over its ssh reach.
func (s *HostService) collect(ctx context.Context, call *auditCall, h hosts.Host) (hosts.Collected, error) {
	if h.Reach.SSH == "" {
		return hosts.Collected{}, redact.Guidance("host %q declares no reach.ssh, so it cannot be inventoried; the inventory needs `sudo -n` over ssh (there is no privileged Outpost operation for it yet). Set reach.ssh to a server/ssh resource id in hosts.yaml", h.Name)
	}
	def, err := s.reach(h)
	if err != nil {
		return hosts.Collected{}, err
	}
	if s.ssh == nil {
		return hosts.Collected{}, redact.Guidance("host %q cannot be inventoried: the ssh connector is not available", h.Name)
	}
	ctx, scope := redact.EnsureScope(ctx)
	pctx, cancel := context.WithTimeout(call.withTelemetry(ctx), 90*time.Second)
	defer cancel()
	spec := auditSpec{connector: "ssh", operation: "exec", op: inventoryExecOperation(), known: true, config: map[string]any{"id": def.ID}, resources: s.resources}
	out, err := s.execFixed(withCredentialScope(pctx, spec), def, hosts.InventoryCommand)
	if err != nil {
		return hosts.Collected{}, hostReasonError(scope, "the host did not answer the inventory over ssh", err)
	}
	return hosts.ParseInventory(out)
}

func hostReasonError(scope *redact.Scope, lead string, cause error) error {
	return redact.Guidance("%s", hostReason(scope, lead, cause))
}
