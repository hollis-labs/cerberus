package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/plan"
)

// confirmDaemon asks for a confirmation until it gets one, and remembers
// what the console sent it and as whom.
type confirmDaemon struct {
	*fakeClient
	opts      cerbapi.MutationOpts
	args      cerbapi.ExternalConnectorOperationArgs
	principal cerbapi.Principal
}

var shownPlan = &cerbapi.ConnectorPlan{PlanHash: "sha256:shown", ComputedBy: cerbapi.SurfaceSocket,
	Plan: plan.Plan{Connector: "local", Operation: "stop", Effect: "lifecycle", Target: audit.Target{Kind: "local.resource", Resource: "web", Env: "dev"}}}

func (d *confirmDaemon) StopResource(ctx context.Context, id string, options ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	d.opts = cerbapi.ApplyMutationOptions(options)
	d.principal, _ = cerbapi.PrincipalFrom(ctx)
	switch {
	case d.opts.Plan:
		return &cerbapi.OpResult{Success: true, ServiceID: id, Plan: shownPlan}, nil
	case d.opts.ConfirmedPlanHash != "":
		d.mutations++
		return &cerbapi.OpResult{Success: true, ServiceID: id}, nil
	}
	return nil, &cerbapi.ExternalConnectorError{Code: cerbapi.ExternalConnectorApprovalPending, Connector: "local", Operation: "stop",
		Approval: &cerbapi.ApprovalRef{ID: "apr_1", Channel: approval.ChannelTTYConfirm, ApproveWith: "cerberus approvals approve apr_1"}}
}

func (d *confirmDaemon) ExecuteConnectorOperation(ctx context.Context, args cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	d.args = args
	d.principal, _ = cerbapi.PrincipalFrom(ctx)
	if args.Plan {
		// Over the socket a plan arrives as decoded JSON.
		raw, _ := json.Marshal(shownPlan)
		var data map[string]any
		_ = json.Unmarshal(raw, &data)
		return cerbapi.ExternalConnectorOperationResult{Connector: args.Connector, Operation: args.Operation, Data: data}, nil
	}
	return cerbapi.ExternalConnectorOperationResult{Connector: args.Connector, Operation: args.Operation, Data: map[string]any{"ok": true}}, nil
}

func confirmConsole(t *testing.T) (*confirmDaemon, http.Handler, string) {
	t.Helper()
	d := &confirmDaemon{fakeClient: &fakeClient{}}
	srv := mustNew(t, d)
	h := srv.Handler(testGuard())
	cookie := signIn(t, srv, h)
	_, token := sessionOf(t, h, cookie)
	return d, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(cookie)
		h.ServeHTTP(w, r)
	}), token
}

// A refusal the console can confirm carries its approval, with the channel,
// to the browser; the dialog then asks for the plan and sends the
// confirmation, which reaches the daemon with the hash, the approval id and
// the signed-in session on the principal.
func TestConsoleConfirmsAResourceAction(t *testing.T) {
	d, h, token := confirmConsole(t)
	rec := consolePost(h, token, "/api/resources/web/stop", `{"acknowledged":true}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"channel":"tty_confirm"`) || !strings.Contains(rec.Body.String(), `"id":"apr_1"`) {
		t.Fatalf("refusal: %d %s", rec.Code, rec.Body.String())
	}
	rec = consolePost(h, token, "/api/resources/web/stop/plan", `{"acknowledged":true}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"plan_hash":"sha256:shown"`) || !d.opts.Plan || d.mutations != 0 {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body.String())
	}
	rec = consolePost(h, token, "/api/resources/web/stop/confirm", `{"acknowledged":true,"approval_id":"apr_1","confirmed_plan_hash":"sha256:shown"}`)
	if rec.Code != http.StatusOK || d.mutations != 1 || d.opts.ConfirmedPlanHash != "sha256:shown" || d.opts.ApprovalID != "apr_1" || !d.opts.Acknowledged {
		t.Fatalf("confirm: %d %s opts %+v", rec.Code, rec.Body.String(), d.opts)
	}
	if d.principal.Kind != cerbapi.PrincipalHuman || d.principal.Via != cerbapi.ViaWeb || d.principal.Session == "" {
		t.Fatalf("the confirmation did not carry the signed-in session: %+v", d.principal)
	}
}

// A confirm names the plan it confirms, and the operation's own route never
// reads a confirmed hash from the body.
func TestConsoleConfirmNeedsItsRouteAndItsHash(t *testing.T) {
	d, h, token := confirmConsole(t)
	if rec := consolePost(h, token, "/api/resources/web/stop/confirm", `{"acknowledged":true,"approval_id":"apr_1"}`); rec.Code != http.StatusBadRequest || d.mutations != 0 {
		t.Fatalf("confirm without a hash: %d %s", rec.Code, rec.Body.String())
	}
	consolePost(h, token, "/api/resources/web/stop", `{"acknowledged":true,"confirmed_plan_hash":"sha256:shown"}`)
	if d.opts.ConfirmedPlanHash != "" || d.mutations != 0 {
		t.Fatalf("the action's own route read a confirmed hash: %+v", d.opts)
	}
	consolePost(h, token, "/api/connectors/docker/operations/stop", `{"config":{},"confirmed_plan_hash":"sha256:shown"}`)
	if d.args.ConfirmedPlanHash != "" {
		t.Fatalf("the operation's own route read a confirmed hash: %+v", d.args)
	}
	if rec := consolePost(h, token, "/api/connectors/docker/operations/stop/confirm", `{"config":{}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("connector confirm without a hash: %d", rec.Code)
	}
}

// A connector operation's plan comes back as the plan, and its confirm
// carries the hash through.
func TestConsoleConfirmsAConnectorOperation(t *testing.T) {
	d, h, token := confirmConsole(t)
	rec := consolePost(h, token, "/api/connectors/docker/operations/stop/plan", `{"config":{"container":"web"},"acknowledged":true}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"plan_hash":"sha256:shown"`) || !d.args.Plan {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body.String())
	}
	rec = consolePost(h, token, "/api/connectors/docker/operations/stop/confirm", `{"config":{"container":"web"},"acknowledged":true,"approval_id":"apr_1","confirmed_plan_hash":"sha256:shown"}`)
	if rec.Code != http.StatusOK || d.args.ConfirmedPlanHash != "sha256:shown" || d.args.ApprovalID != "apr_1" || d.args.Plan {
		t.Fatalf("confirm: %d %s args %+v", rec.Code, rec.Body.String(), d.args)
	}
}
