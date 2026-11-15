package gate

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// The audit lane: `cerberus audit prune` and `cerberus audit reanchor`, the two writes to the
// audit chain that are not an operation's record. The old build ran both in the CLI's own
// process, on an interactive terminal after a typed phrase, through a second FileSink on the
// same directory. Here the serving Cerberus is the chain's one writer (its FileSink holds the
// directory's lock and its keychain anchor), so both run on the gate's own sink, with old's
// semantics and records: audit.Prune's intent before anything is removed and its outcome after,
// audit.Reanchor's chain_reanchored record, which moves the anchor.
//
// Both are the owner's (decision 0.1, as the brake lift and secrets set): the typed phrase is
// the acknowledgment, then the owner signed in on the console, or an out-of-band approval the
// owner decided on the console, bound to exactly what is removed or acknowledged. An MCP client
// is refused and asks for nothing. Neither passes the brakes or policy, as on main: they are the
// chain's own maintenance, and a lockdown must not stop the owner from reanchoring the chain a
// lift waits on.

// Audit lane names, as the records and the approvals carry them.
const (
	AuditConnector   = audit.PruneConnector
	AuditPrune       = audit.PruneOperation
	AuditReanchor    = "reanchor"
	auditChainTarget = "audit.chain"
)

// AuditPruneArgs is a prune: the date, as old's --before, and the form's fields.
type AuditPruneArgs struct {
	Before       time.Time `json:"before"`
	Acknowledged bool      `json:"acknowledged"`
	ApprovalID   string    `json:"approval_id,omitempty"`
}

// AuditPrunePlan is what a prune would remove: old's "This removes N audit month file(s) from".
type AuditPrunePlan struct {
	Dir    string   `json:"dir"`
	Before string   `json:"before"`
	Files  []string `json:"files"`
}

// AuditPruneResult is what a prune removed, and what authorized it ("console" or
// "approval:<id>"). Nothing pruned is old's "nothing to prune", not an error.
type AuditPruneResult struct {
	AuditPrunePlan
	Removed []string `json:"removed"`
	Proof   string   `json:"proof,omitempty"`
}

// AuditReanchorArgs is a reanchor's form fields.
type AuditReanchorArgs struct {
	Acknowledged bool   `json:"acknowledged"`
	ApprovalID   string `json:"approval_id,omitempty"`
}

// AuditReanchorPlan is what a reanchor acknowledges: the chain's problems. TailTrusted is old's
// "nothing to reanchor".
type AuditReanchorPlan struct {
	Dir         string   `json:"dir"`
	TailTrusted bool     `json:"tail_trusted"`
	Problems    []string `json:"problems"`
}

// AuditReanchorResult is the chain_reanchored record's seq, and what authorized it.
type AuditReanchorResult struct {
	AuditReanchorPlan
	Seq   uint64 `json:"seq,omitempty"`
	Proof string `json:"proof,omitempty"`
}

// auditDir is the directory the gate's own sink writes: the chain the lane maintains. A sink
// with none (observe mode's, a test's memory sink) has no chain to prune or reanchor.
func (g *Gate) auditDir(operation string) (string, error) {
	d, ok := g.sink.(interface{ Dir() string })
	if !ok || d.Dir() == "" {
		return "", codedError(opArgs{Connector: AuditConnector, Operation: operation}, CodeAuditUnavailable,
			redact.Guidance("this Cerberus writes no audit chain of its own, so it cannot %s one; the serving Cerberus in supervise mode does", operation))
	}
	return d.Dir(), nil
}

// AuditPrunePlan is the prune's plan: the month files that lie wholly before before.
func (g *Gate) AuditPrunePlan(before time.Time) (AuditPrunePlan, error) {
	dir, err := g.auditDir(AuditPrune)
	if err != nil {
		return AuditPrunePlan{}, err
	}
	if before.IsZero() {
		return AuditPrunePlan{}, codedError(opArgs{Connector: AuditConnector, Operation: AuditPrune}, CodeInvalidArgs,
			redact.Guidance("--before is required, as a date such as 2026-06-01"))
	}
	files, err := audit.PrunePlan(dir, before)
	if err != nil {
		return AuditPrunePlan{}, err
	}
	out := AuditPrunePlan{Dir: dir, Before: before.UTC().Format("2006-01-02"), Files: []string{}}
	for _, f := range files {
		out.Files = append(out.Files, filepath.Base(f))
	}
	return out, nil
}

// PruneAudit removes the month files the plan lists, on the gate's own sink, once acknowledged
// and authorized by the owner (auditOwnerProof).
func (g *Gate) PruneAudit(ctx context.Context, args AuditPruneArgs) (AuditPruneResult, error) {
	plan, err := g.AuditPrunePlan(args.Before)
	if err != nil {
		return AuditPruneResult{}, err
	}
	out := AuditPruneResult{AuditPrunePlan: plan, Removed: []string{}}
	if len(plan.Files) == 0 {
		return out, nil
	}
	fields := map[string]string{"before": plan.Before, "removed": joinNames(plan.Files)}
	t := audit.Target{Kind: audit.PruneTarget, Fields: fields}
	proof, err := g.auditOwnerProof(ctx, AuditPrune, t, fields, args.Acknowledged, args.ApprovalID,
		"pruning "+joinNames(plan.Files)+" from the audit log", "removing them")
	if err != nil {
		return out, err
	}
	out.Proof = proof
	removed, err := audit.Prune(g.sink, plan.Dir, args.Before, AuditPrincipal(ctx))
	out.Removed = append(out.Removed, removed...)
	if errors.Is(err, audit.ErrUnavailable) {
		return out, codedError(opArgs{Connector: AuditConnector, Operation: AuditPrune}, CodeAuditUnavailable, err)
	}
	return out, err
}

// AuditReanchorPlan is the chain's problems as a reanchor would acknowledge them.
func (g *Gate) AuditReanchorPlan() (AuditReanchorPlan, error) {
	dir, err := g.auditDir(AuditReanchor)
	if err != nil {
		return AuditReanchorPlan{}, err
	}
	checked, err := audit.Check(dir)
	if err != nil {
		return AuditReanchorPlan{}, codedError(opArgs{Connector: AuditConnector, Operation: AuditReanchor}, CodeAuditUnavailable, err)
	}
	return AuditReanchorPlan{Dir: dir, TailTrusted: checked.TailTrusted(), Problems: append([]string{}, checked.Problems...)}, nil
}

// ReanchorAudit writes the chain_reanchored record on the gate's own sink, once acknowledged and
// authorized by the owner (auditOwnerProof). A chain that vouches for its tail has nothing to
// reanchor, which is old's answer, not an error.
func (g *Gate) ReanchorAudit(ctx context.Context, args AuditReanchorArgs) (AuditReanchorResult, error) {
	plan, err := g.AuditReanchorPlan()
	if err != nil || plan.TailTrusted {
		return AuditReanchorResult{AuditReanchorPlan: plan}, err
	}
	// The approval binds to how many problems it acknowledges. Not to their text: the records the
	// approval itself writes after a cut change how the cut reads ("past the end" becomes "not
	// where it was written"), and it is still the same cut.
	fields := map[string]string{"problems": strconv.Itoa(len(plan.Problems))}
	t := audit.Target{Kind: auditChainTarget}
	proof, err := g.auditOwnerProof(ctx, AuditReanchor, t, fields, args.Acknowledged, args.ApprovalID,
		"reanchoring the audit chain", "acknowledging them")
	if err != nil {
		return AuditReanchorResult{AuditReanchorPlan: plan}, err
	}
	rec, err := audit.Reanchor(g.sink, plan.Dir, AuditPrincipal(ctx))
	if errors.Is(err, audit.ErrNothingToReanchor) {
		plan.TailTrusted = true
		return AuditReanchorResult{AuditReanchorPlan: plan, Proof: proof}, nil
	}
	if err != nil {
		return AuditReanchorResult{AuditReanchorPlan: plan}, codedError(opArgs{Connector: AuditConnector, Operation: AuditReanchor}, CodeAuditUnavailable, err)
	}
	return AuditReanchorResult{AuditReanchorPlan: plan, Seq: rec.Seq, Proof: proof}, nil
}

// auditOwnerProof is what authorizes a write to the chain: the acknowledgment (old's typed
// phrase), then the owner. The owner signed in on the console writes directly; any other caller
// but an MCP client names the owner's out-of-band approval, asked for when none is named, bound
// to the target and the digest of what is written. An MCP client is refused and asks for nothing.
func (g *Gate) auditOwnerProof(ctx context.Context, operation string, t audit.Target, args map[string]string, acknowledged bool, approvalID, what, ackWhat string) (string, error) {
	if !acknowledged {
		return "", codedError(opArgs{Connector: AuditConnector, Operation: operation}, CodeAckRequired,
			redact.Guidance("%s changes the audit chain, so it needs an acknowledgment (\"acknowledged\": true: the typed phrase at the CLI, before %s); nothing was written", what, ackWhat))
	}
	if ConsolePerson(ctx) && approvalID == "" {
		return "console", nil
	}
	return g.OwnerApproval(ctx, OwnerApprovalRequest{Connector: AuditConnector, Operation: operation, Target: t,
		ArgsDigest: g.sink.Digest(args), ApprovalID: approvalID, What: what,
		Rule: "audit." + operation, Reason: "the audit chain is pruned and reanchored by the owner on the console"})
}

func joinNames(names []string) string { return strings.Join(names, ",") }
