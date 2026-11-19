package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/hostmon"
)

// The Outpost host actions run as main's did, against a fake Outpost that checks the bearer from
// the token file (newHostWorld): a plan runs nothing and answers the plan an approval binds to;
// units runs; a restart needs acknowledgment, then runs, and is recorded as host.restart; a
// lockdown refuses it without reaching the Outpost; a host without an Outpost reach and the probe
// verb are refused; observe mode runs nothing.
func TestHostActions(t *testing.T) {
	w := newHostWorld(t)
	g := gatetest.New(t, gatetest.Options{})
	a := w.api(t, g)
	a.o.HostActions = &hostmon.Actions{Prober: w.reads.Prober, Gate: g.Gate}
	calls := func() int { return *w.probes }

	code, body := send(t, a, apiKey, "POST", "/api/hosts/box/actions/restart/plan", `{"request":{"unit":"web.service"}}`)
	if code != 200 || !strings.Contains(body, `"plan_hash":"sha256:`) || !strings.Contains(body, `"operation":"restart"`) || calls() != 0 {
		t.Fatalf("plan: %d %s (outpost calls %d)", code, body, calls())
	}
	if code, body = send(t, a, apiKey, "POST", "/api/hosts/box/actions/units", `{"request":{}}`); code != 200 || !strings.Contains(body, `"exit_code":0`) || calls() != 1 {
		t.Fatalf("units: %d %s", code, body)
	}
	if code, body = send(t, a, apiKey, "POST", "/api/hosts/box/actions/restart", `{"request":{"unit":"web.service"}}`); code != http.StatusConflict ||
		!strings.Contains(body, `"code":"acknowledgment_required"`) || calls() != 1 {
		t.Fatalf("restart without ack: %d %s", code, body)
	}
	if code, body = send(t, a, apiKey, "POST", "/api/hosts/box/actions/restart", `{"request":{"unit":"web.service"},"acknowledged":true}`); code != 200 || calls() != 2 {
		t.Fatalf("restart: %d %s", code, body)
	}
	recorded := ""
	for _, r := range g.Records(audit.KindOutcome) {
		recorded += " " + r.Connector + "." + r.Operation
	}
	if !strings.Contains(recorded, "host.restart") || !strings.Contains(recorded, "host.units") {
		t.Fatalf("outcomes recorded: %s", recorded)
	}
	g.Engage(t, brakeLockdown())
	if code, body = send(t, a, apiKey, "POST", "/api/hosts/box/actions/restart", `{"request":{"unit":"web.service"},"acknowledged":true}`); code != http.StatusLocked || calls() != 2 {
		t.Fatalf("restart under lockdown: %d %s (outpost calls %d)", code, body, calls())
	}
	for _, c := range []struct{ path, want string }{
		{"/api/hosts/alpha/actions/units", "needs reach.transport=outpost"},
		{"/api/hosts/nope/actions/units", `"code":"not_found"`},
	} {
		if code, body = send(t, a, apiKey, "POST", c.path, `{"request":{}}`); code == 200 || !strings.Contains(body, c.want) {
			t.Fatalf("%s: %d %s", c.path, code, body)
		}
	}
	observe := w.api(t, nil)
	if code, body = send(t, observe, apiKey, "POST", "/api/hosts/box/actions/units", `{"request":{}}`); code != http.StatusConflict || !strings.Contains(body, `"code":"observe_mode"`) {
		t.Fatalf("observe mode: %d %s", code, body)
	}
}
