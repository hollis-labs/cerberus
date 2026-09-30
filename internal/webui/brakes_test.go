package webui

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// brakesDaemon is the console's client with the daemon's brakes.
type brakesDaemon struct {
	*fakeClient
	state   brake.State
	engaged []cerbapi.BrakeEngageArgs
	lifted  []string
	liftErr error
}

func (d *brakesDaemon) Brakes(context.Context) (cerbapi.BrakesView, error) {
	return cerbapi.BrakesView{State: d.state}, nil
}

func (d *brakesDaemon) EngageBrake(_ context.Context, args cerbapi.BrakeEngageArgs) (cerbapi.BrakesView, error) {
	d.engaged = append(d.engaged, args)
	d.state.Lockdown = &brake.Lockdown{ID: "ldn_1", EngagedAt: time.Now(), By: audit.Principal{Kind: "human", Via: "web"}, Reason: args.Reason}
	return cerbapi.BrakesView{State: d.state}, nil
}

func (d *brakesDaemon) LiftBrake(_ context.Context, id string, args cerbapi.BrakeLiftArgs) (cerbapi.BrakesView, error) {
	d.lifted = append(d.lifted, id+"|"+args.ApprovalID)
	return cerbapi.BrakesView{}, d.liftErr
}

func (d *brakesDaemon) ResetSuspension(_ context.Context, id string, args cerbapi.BrakeResetArgs) (cerbapi.BrakesView, error) {
	d.lifted = append(d.lifted, "reset:"+id+"|"+args.Typed)
	d.state.Suspensions = nil
	return cerbapi.BrakesView{State: d.state}, nil
}

func brakesConsole(t *testing.T) (*brakesDaemon, http.Handler, string) {
	t.Helper()
	d := &brakesDaemon{fakeClient: &fakeClient{}}
	srv := mustNew(t, d)
	h := srv.Handler(testGuard())
	cookie := signIn(t, srv, h)
	_, token := sessionOf(t, h, cookie)
	return d, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(cookie)
		h.ServeHTTP(w, r)
	}), token
}

// Engaging from the console is one request with no typed phrase, and the
// session then carries the banner.
func TestConsoleEngagesLockdown(t *testing.T) {
	d, h, token := brakesConsole(t)
	if rec := serve(h, newTestRequest(http.MethodGet, "/api/session", nil)); !strings.Contains(rec.Body.String(), `"brakes":null`) {
		t.Fatalf("session with no brake: %s", rec.Body.String())
	}
	// A freeze match is not engaged from here: the console's button is the
	// lockdown.
	rec := consolePost(h, token, "/api/brakes/lockdown", `{"reason":"loop","match":{"Env":"prod"}}`)
	if rec.Code != http.StatusOK || len(d.engaged) != 1 || d.engaged[0].Reason != "loop" || d.engaged[0].Match != nil {
		t.Fatalf("engage: %d %s %+v", rec.Code, rec.Body.String(), d.engaged)
	}
	if rec = serve(h, newTestRequest(http.MethodGet, "/api/session", nil)); !strings.Contains(rec.Body.String(), `"id":"ldn_1"`) {
		t.Fatalf("session with a lockdown: %s", rec.Body.String())
	}
	// Without the action token it is refused, like every state change.
	if rec = consolePost(h, "", "/api/brakes/lockdown", `{}`); rec.Code != http.StatusForbidden || len(d.engaged) != 1 {
		t.Fatalf("engage without the token: %d", rec.Code)
	}
}

// Lifting reaches the daemon, and its approval_pending comes back with the
// approval for the page to open.
func TestConsoleLiftsThroughTheDaemon(t *testing.T) {
	d, h, token := brakesConsole(t)
	d.state.Freezes = []brake.Freeze{{ID: "frz_1", Match: policy.TargetMatch{Env: "prod"}}}
	if rec := serve(h, newTestRequest(http.MethodGet, "/api/brakes", nil)); !strings.Contains(rec.Body.String(), `"scope":"env=prod"`) {
		t.Fatalf("brakes: %s", rec.Body.String())
	}
	d.liftErr = &cerbapi.ExternalConnectorError{Code: cerbapi.ExternalConnectorApprovalPending, Connector: "brake", Operation: "lift_freeze",
		Err: errors.New("approve it with your passkey"), Approval: &cerbapi.ApprovalRef{ID: "apr_7"}}
	rec := consolePost(h, token, "/api/brakes/freeze/frz_1/lift", `{}`)
	if rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"apr_7"`) || d.lifted[0] != "frz_1|" {
		t.Fatalf("pending lift: %d %s", rec.Code, rec.Body.String())
	}
	d.liftErr = nil
	if rec = consolePost(h, token, "/api/brakes/lockdown/lift", `{"approval_id":"apr_8"}`); rec.Code != http.StatusOK || d.lifted[1] != "|apr_8" {
		t.Fatalf("lift: %d %s %v", rec.Code, rec.Body.String(), d.lifted)
	}
}

// Suspensions reach the session's banner, and a reset carries the typed
// phrase to the daemon.
func TestConsoleShowsAndResetsSuspensions(t *testing.T) {
	d, h, token := brakesConsole(t)
	d.state.Suspensions = []brake.Suspension{{ID: "sus_1", Key: "agent|mcp_stdio|session:s1", Principal: audit.Principal{Kind: "agent", Via: "mcp_stdio"}, Denials: 3, Window: "10m0s"}}
	if rec := serve(h, newTestRequest(http.MethodGet, "/api/session", nil)); !strings.Contains(rec.Body.String(), `"id":"sus_1"`) {
		t.Fatalf("session with a suspension: %s", rec.Body.String())
	}
	if rec := consolePost(h, token, "/api/brakes/suspensions/sus_1/reset", `{"typed":"reset sus_1"}`); rec.Code != http.StatusOK || d.lifted[0] != "reset:sus_1|reset sus_1" {
		t.Fatalf("reset: %d %s %v", rec.Code, rec.Body.String(), d.lifted)
	}
	if rec := consolePost(h, "", "/api/brakes/suspensions/sus_1/reset", `{"typed":"reset sus_1"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("reset without the token: %d", rec.Code)
	}
}
