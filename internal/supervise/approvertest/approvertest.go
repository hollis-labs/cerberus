// Package approvertest is the table of what the old build routes away from a plain
// acknowledged run: to an approval, a typed confirmation, or a brake. Slice 5's interim approver
// refused each such case; slice 6 runs the same table against the real, gate-backed lane, whose
// answers are the old build's codes.
//
// The cases come from the old build's routing (origin/main, internal/cerbapi):
//   - a body naming approval_id runs under that approval (MutationOpts.ApprovalID, P3-2);
//   - a body asking for plan returns the plan instead of running (MutationOpts.Plan, P3-2);
//   - a confirmed plan hash runs a plan a person confirmed (confirm route, P3-3);
//   - a lockdown refuses every write until lifted or broken (brakes, 423);
//   - a freeze refuses the writes on the targets its match selects (brakes, 423);
//   - everything else, acknowledged, runs under the posture policy gives the caller.
package approvertest

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/supervise"
	"github.com/hollis-labs/cerberus/internal/target"
)

// The kinds of case, one per routing decision.
const (
	Plain           = "plain"
	ApprovalID      = "approval-id"
	PlanOnly        = "plan"
	ConfirmedPlan   = "confirmed-plan"
	Lockdown        = "lockdown"
	FreezeCovers    = "freeze-covers"
	FreezeElsewhere = "freeze-elsewhere"
)

// Case is one routing decision of the old build.
type Case struct {
	Name string
	Kind string
	Req  supervise.Request
	// Brakes is the brake state the case runs under.
	Brakes brake.State
}

// Resource is the resource every case targets: the operator's own, in production, so the
// built-in baseline enforces an agent's change to it (policy.BaselineEnforced) and asks for an
// approval rather than refusing it.
func Resource() config.ResourceDef {
	return config.ResourceDef{ID: "web", Type: "process", Project: "demo", Connector: "local",
		Env: target.EnvProd, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf},
		Config: map[string]any{"mode": "dev_session", "command": []any{"/bin/true"}}}
}

// Cases is the table, for each of the six verbs.
func Cases() []Case {
	lockdown := brake.State{Lockdown: &brake.Lockdown{ID: "lk-1", EngagedAt: time.Unix(0, 0)}}
	freezeWeb := brake.State{Freezes: []brake.Freeze{{ID: "fz-web", Match: policy.TargetMatch{ID: "web"}, EngagedAt: time.Unix(0, 0)}}}
	freezeOther := brake.State{Freezes: []brake.Freeze{{ID: "fz-api", Match: policy.TargetMatch{ID: "api"}, EngagedAt: time.Unix(0, 0)}}}
	var out []Case
	for _, verb := range supervise.Verbs {
		req := supervise.Request{Verb: verb, ID: "web", Acknowledged: true}
		withApproval, withPlan, confirmed := req, req, req
		withApproval.ApprovalID = "ap-1"
		withPlan.Plan = true
		confirmed.ConfirmedPlanHash = "sha256:abc"
		for _, c := range []Case{
			{Kind: Plain, Req: req},
			{Kind: ApprovalID, Req: withApproval},
			{Kind: PlanOnly, Req: withPlan},
			{Kind: ConfirmedPlan, Req: confirmed},
			{Kind: Lockdown, Req: req, Brakes: lockdown},
			{Kind: FreezeCovers, Req: req, Brakes: freezeWeb},
			{Kind: FreezeElsewhere, Req: req, Brakes: freezeOther},
		} {
			c.Name = verb + "/" + c.Kind
			out = append(out, c)
		}
	}
	return out
}

// Caller is who a case runs as, as the API's auth guard established them.
type Caller struct {
	Name      string
	Principal libauth.Principal
	// Agent is true when the gate classes the caller an agent, so the baseline enforces its
	// change to production.
	Agent bool
}

// Callers are the two the table runs as: the owner signed in to the console, and an API key.
func Callers() []Caller {
	return []Caller{
		{Name: "owner-session", Principal: libauth.Principal{Verified: true, ID: "user:u1", Kind: "user", SessionID: "s1", Scopes: []string{"*"}}},
		{Name: "api-key", Principal: libauth.Principal{Verified: true, ID: "key:k1", Kind: libauth.KindService, Scopes: []string{"cerberus.read", "cerberus.operate", "cerberus.admin"}}, Agent: true},
	}
}

// Context is ctx carrying the caller as the API enters a request into the gate.
func (c Caller) Context(ctx context.Context) context.Context {
	return api.GateContext(libauth.WithPrincipal(ctx, c.Principal))
}

// Want is the old build's answer to a case: it ran (passed the gate), it answered with the plan,
// or it was refused with Code at Status.
type Want struct {
	Ran, Plan bool
	Status    int
	Code      gate.ErrorCode
}

// GateWant is the old build's answer to c from who, under the built-in baseline with the
// applied snapshot's enforcement (no policy applied). A person's call is shadow: it runs, an
// approval id it names is not needed and is not spent, and a confirmed plan must still be the
// plan. An agent's change to production is enforced: it needs an out-of-band approval, which a
// confirmation on the call does not meet. The brakes come before policy for both, and a plan
// request runs nothing, so neither the brakes nor policy stop it.
func GateWant(c Case, who Caller) Want {
	switch c.Kind {
	case PlanOnly:
		return Want{Plan: true, Status: http.StatusOK}
	case Lockdown:
		return Want{Status: http.StatusLocked, Code: gate.CodeLockdown}
	case FreezeCovers:
		return Want{Status: http.StatusLocked, Code: gate.CodeFrozen}
	}
	if !who.Agent {
		if c.Kind == ConfirmedPlan {
			return Want{Status: http.StatusConflict, Code: gate.CodePlanStale}
		}
		return Want{Ran: true, Status: http.StatusOK}
	}
	if c.Kind == ApprovalID {
		return Want{Status: http.StatusPreconditionRequired, Code: gate.CodeApprovalRequired}
	}
	return Want{Status: http.StatusConflict, Code: gate.CodeApprovalPending}
}

// Answer is what the lane answered a case.
type Answer struct {
	Out *supervise.OpResult
	Err error
}

// RunGate runs the table as each caller through run, which builds a gate-backed lane under the
// case's brakes and calls it with ctx, and checks every answer against GateWant.
func RunGate(t *testing.T, run func(t *testing.T, ctx context.Context, c Case) Answer) {
	t.Helper()
	for _, who := range Callers() {
		for _, c := range Cases() {
			t.Run(who.Name+"/"+c.Name, func(t *testing.T) {
				want := GateWant(c, who)
				got := run(t, who.Context(context.Background()), c)
				if got.Err == nil {
					switch {
					case got.Out == nil:
						t.Fatal("no result and no error")
					case want.Plan && (got.Out.Plan == nil || got.Out.Plan.PlanHash == ""):
						t.Fatalf("want the plan, got %+v", got.Out)
					case want.Ran && got.Out.Plan != nil:
						t.Fatalf("want a run, got a plan: %+v", got.Out)
					case !want.Ran && !want.Plan:
						t.Fatalf("passed the gate (%+v), want %d %s", got.Out, want.Status, want.Code)
					}
					return
				}
				status, code, _, details, ok := gate.HTTPError(got.Err)
				if !ok || status != want.Status || code != want.Code {
					t.Fatalf("err = %v (%d %s), want %d %s", got.Err, status, code, want.Status, want.Code)
				}
				if code == gate.CodeApprovalPending && (details.Approval == nil || details.Approval.ID == "") {
					t.Fatalf("approval_pending without the approval it is pending on: %+v", details)
				}
				var refusal *supervise.Refusal
				if errors.As(got.Err, &refusal) {
					t.Fatalf("a lane refusal, not the gate's: %v", refusal)
				}
			})
		}
	}
}
