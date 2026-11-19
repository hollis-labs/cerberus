package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
)

func askCapacity(t *testing.T, a *API, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/hosts/capacity", strings.NewReader(body))
	req = req.WithContext(libauth.WithPrincipal(req.Context(), apiKey))
	rec := httptest.NewRecorder()
	a.Mux().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// POST /api/hosts/capacity is main's HostCapacity: a freshly probed host that fits is matched, a
// stale or never-probed one is unknown (not guessed), and with explain a host that does not fit is
// excluded with its reasons. The read is recorded as main's host.capacity; a bad query is 400.
func TestHostCapacity(t *testing.T) {
	w := newHostWorld(t)
	g := gatetest.New(t, gatetest.Options{})
	a := w.api(t, g)
	if code, body := get(t, a, "POST", "/api/hosts/box/probe"); code != 200 {
		t.Fatalf("probe: %d %s", code, body)
	}
	names := func(vs []HostVerdict) map[string]string {
		out := map[string]string{}
		for _, v := range vs {
			out[v.Name] = strings.Join(v.Reasons, "; ")
		}
		return out
	}

	code, body := askCapacity(t, a, `{"min_cpu":2,"explain":true}`)
	var res CapacityResult
	if code != 200 || json.Unmarshal([]byte(body), &res) != nil {
		t.Fatalf("capacity: %d %s", code, body)
	}
	if len(res.Matched) != 1 || res.Matched[0].Host.Name != "box" || res.Total != 4 {
		t.Fatalf("matched: %s", body)
	}
	unknown := names(res.Unknown)
	if _, ok := unknown["zeta"]; !ok {
		t.Errorf("a stale host is not unknown: %s", body)
	}
	if _, ok := unknown["mid"]; !ok {
		t.Errorf("a never-probed host is not unknown: %s", body)
	}

	code, body = askCapacity(t, a, `{"min_cpu":16,"explain":true}`)
	if err := json.Unmarshal([]byte(body), &res); code != 200 || err != nil {
		t.Fatalf("capacity: %d %s", code, body)
	}
	if why, ok := names(res.Excluded)["box"]; len(res.Matched) != 0 || !ok || why == "" {
		t.Fatalf("a host short of cpu is not excluded with a reason: %s", body)
	}

	for _, bad := range []string{`{"min_cpu":"x"}`, `{"max_stale_age":"soon"}`, `{"nope":1}`} {
		if code, body := askCapacity(t, a, bad); code != 400 || !strings.Contains(body, `"code":"invalid_args"`) {
			t.Errorf("%s: %d %s", bad, code, body)
		}
	}
	var recorded int
	for _, r := range g.Records(audit.KindIntent) {
		if r.Connector+"."+r.Operation == "host.capacity" {
			recorded++
		}
	}
	if recorded < 2 {
		t.Fatalf("capacity reads recorded %d times", recorded)
	}
}
