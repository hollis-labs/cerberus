// Package approvertest is the table of what the old build routes away from a plain
// acknowledged run: to an approval, a typed confirmation, or a brake. Slice 5's interim
// approver refuses each such case; slice 6 runs the same table against the real approver,
// whose answer for each case is an approval flow instead of a refusal.
//
// The cases come from the old build's routing (origin/main, internal/cerbapi):
//   - a body naming approval_id runs under that approval (MutationOpts.ApprovalID, P3-2);
//   - a body asking for plan returns the plan instead of running (MutationOpts.Plan, P3-2);
//   - a confirmed plan hash runs a plan a person confirmed (confirm route, P3-3);
//   - a lockdown refuses every write until lifted or broken (brakes, 423);
//   - a freeze refuses the writes on the targets its match selects (brakes, 423);
//   - everything else, acknowledged, runs under the baseline posture.
package approvertest

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/supervise"
)

// Case is one routing decision of the old build.
type Case struct {
	Name string
	Req  supervise.Request
	// Brakes is the brake state the case runs under.
	Brakes brake.State
	// Routed is true when the old build does not run the request as asked: it needs an
	// approval, a confirmation, or is braked. Status is the old status for a refusal.
	Routed bool
	Status int
}

// Resource is the resource every case targets.
func Resource() config.ResourceDef {
	return config.ResourceDef{ID: "web", Type: "process", Project: "demo", Connector: "local", Env: "prod",
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
		out = append(out,
			Case{Name: verb + "/plain", Req: req},
			Case{Name: verb + "/approval-id", Req: withApproval, Routed: true, Status: http.StatusPreconditionRequired},
			Case{Name: verb + "/plan", Req: withPlan, Routed: true, Status: http.StatusPreconditionRequired},
			Case{Name: verb + "/confirmed-plan", Req: confirmed, Routed: true, Status: http.StatusPreconditionRequired},
			Case{Name: verb + "/lockdown", Req: req, Brakes: lockdown, Routed: true, Status: http.StatusLocked},
			Case{Name: verb + "/freeze-covers", Req: req, Brakes: freezeWeb, Routed: true, Status: http.StatusLocked},
			Case{Name: verb + "/freeze-elsewhere", Req: req, Brakes: freezeOther},
		)
	}
	return out
}

// RunInterim checks an approver built per case against the table: a routed case is refused
// with approvals_unavailable at the old status, and a plain one passes.
func RunInterim(t *testing.T, build func(brake.State) supervise.Approver) {
	t.Helper()
	res := Resource()
	for _, c := range Cases() {
		t.Run(c.Name, func(t *testing.T) {
			err := build(c.Brakes).Approve(context.Background(), c.Req, &res)
			if !c.Routed {
				if err != nil {
					t.Fatalf("a plain acknowledged %s was refused: %v", c.Req.Verb, err)
				}
				return
			}
			var r *supervise.Refusal
			if !errors.As(err, &r) || r.Code != supervise.ErrApprovalsUnavailable || r.Status != c.Status {
				t.Fatalf("err = %v, want %d %s", err, c.Status, supervise.ErrApprovalsUnavailable)
			}
		})
	}
}
