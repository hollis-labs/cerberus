package cerbapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/opsref"
	"github.com/hollis-labs/cerberus/internal/opssink"
)

// DeployRecord is a deploy done outside `resource deploy` (Cerberus's own
// release scripts for the Tether hub, outposts and Surface), reported after the
// fact.
type DeployRecord struct {
	Resource string
	Revision string
	// Outcome is deployed, deploy_failed or rolled_back.
	Outcome string
	Health  string
	// Refs are what the caller resolved (flag, env); the mapping completes them.
	Refs opsref.Refs
}

// RecordDeploy writes the audit record of an out-of-band deploy. It is the
// record of a fact, not an operation: nothing runs, so there is no gate and no
// intent, only the outcome. The same record drives the deploy.state_changed
// event (the exporter reads the chain) and the Ops post (the audit sink's
// observer), exactly as for `resource deploy`.
func (s *ResourceRuntimeService) RecordDeploy(ctx context.Context, r DeployRecord) (audit.Record, error) {
	r.Resource = strings.TrimSpace(r.Resource)
	if r.Resource == "" {
		return audit.Record{}, fmt.Errorf("deploy record: a resource id is required")
	}
	if !opssink.ValidOutcome(r.Outcome) {
		return audit.Record{}, fmt.Errorf("deploy record: outcome %q is not one of deployed, deploy_failed, rolled_back", r.Outcome)
	}
	snap := s.snapshotConfig()
	rec := audit.Record{
		Kind:        audit.KindOutcome,
		OperationID: audit.NewID(),
		Principal:   principalFor(ctx, auditSpec{}),
		Connector:   "local",
		Operation:   opssink.OpDeployRecord,
		Target:      audit.Target{Kind: "resource", Fields: map[string]string{"id": r.Resource}, Resource: r.Resource},
		Decision:    audit.DecisionAllowed,
		OutcomeCode: audit.OutcomeOK,
		Posture:     PolicyDecisionPoint().GlobalPosture(),

		Revision:      strings.TrimSpace(r.Revision),
		Health:        strings.TrimSpace(r.Health),
		DeployOutcome: r.Outcome,
	}
	refs := s.resolveOpsRefs(MutationOpts{OpsID: r.Refs.OpsID, WorkstreamID: r.Refs.WorkstreamID, Task: r.Refs.Task}, r.Resource, snap)
	rec.OpsID, rec.WorkstreamID, rec.Task = refs.OpsID, refs.WorkstreamID, refs.Task
	out, err := s.audit.Write(rec)
	if err != nil {
		return audit.Record{}, fmt.Errorf("the audit log could not be written, so the deploy was not recorded: %w", err)
	}
	return out, nil
}
