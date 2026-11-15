package admin_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/admin/admintest"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/supervise/approvertest"
)

// Every admin write passes the gate as the old build routed it (approvertest): a plain call,
// one naming an approval, a plan request, a confirmed plan, a lockdown, a freeze on its target
// and one elsewhere, as the owner's console session and as an API key.
func TestAdminWritesAreGated(t *testing.T) {
	approvertest.RunAdminGate(t, func(t *testing.T, ctx context.Context, c approvertest.AdminCase) approvertest.AdminAnswer {
		root, _ := install(t, admintest.Plugin{Loaded: c.Write.Name != approvertest.PluginLoad})
		world := gatetest.New(t, gatetest.Options{})
		l := newLane(t, root, world)
		if err := l.svc.Plugins().RestorePlugins(context.Background()); err != nil {
			t.Fatal(err)
		}
		world.Engage(t, c.Brakes)
		opts := admin.GateOpts{ApprovalID: c.ApprovalID, Plan: c.Plan, ConfirmedPlanHash: c.ConfirmedPlanHash}
		switch c.Write.Name {
		case approvertest.ConnectorExecuteSSH, approvertest.PluginIssuingOp:
			args := admin.OperationArgs{Connector: "ssh", Operation: "exec", Acknowledged: true, Config: map[string]any{"id": "box", "command": "uptime"},
				ApprovalID: c.ApprovalID, Plan: c.Plan, ConfirmedPlanHash: c.ConfirmedPlanHash}
			if c.Write.Name == approvertest.PluginIssuingOp {
				args.Connector, args.Operation, args.Config = "echo", "provision", map[string]any{"issuance_id": strings.Repeat("c", 32), "id": "db"}
			}
			out, err := l.svc.Execute(ctx, args)
			if err != nil {
				return approvertest.AdminAnswer{Err: err}
			}
			if c.Plan {
				shown, ok := out.Data.(gate.ConnectorPlan)
				if !ok {
					t.Fatalf("a plan request answered %#v", out.Data)
				}
				return approvertest.AdminAnswer{PlanHash: shown.PlanHash}
			}
			if c.Write.Name == approvertest.PluginIssuingOp && (out.CredentialIssuance == nil || out.CredentialIssuance.State != "completed") {
				t.Fatalf("the issuing operation ran without its issuance: %+v", out)
			}
			return approvertest.AdminAnswer{Ran: true}
		case approvertest.PluginLoad, approvertest.PluginUnload:
			do := l.svc.Plugins().Load
			if c.Write.Name == approvertest.PluginUnload {
				do = l.svc.Plugins().Unload
			}
			st, shown, err := do(ctx, "echo", opts)
			if err != nil {
				return approvertest.AdminAnswer{Err: err}
			}
			if shown != nil {
				return approvertest.AdminAnswer{PlanHash: shown.PlanHash}
			}
			if st.Loaded != (c.Write.Name == approvertest.PluginLoad) {
				t.Fatalf("the %s ran and left loaded=%v", c.Write.Name, st.Loaded)
			}
			return approvertest.AdminAnswer{Ran: true}
		case approvertest.ProviderSave:
			out, err := l.svc.SaveCredentials(ctx, "echo", admin.CredentialSaveRequest{Secrets: map[string]string{"token": "t-1"}}, opts)
			if err != nil {
				return approvertest.AdminAnswer{Err: err}
			}
			if out.Plan != nil {
				return approvertest.AdminAnswer{PlanHash: out.Plan.PlanHash}
			}
			return approvertest.AdminAnswer{Ran: out.Success}
		}
		t.Fatalf("no case for %s", c.Write.Name)
		return approvertest.AdminAnswer{}
	})
}
