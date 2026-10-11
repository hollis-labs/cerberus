package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/hosts"
)

const capacityHostsYAML = `version: 1
hosts:
  - {name: zulu, role: worker, labels: {agents: "true"}}
  - {name: alpha, role: worker, labels: {agents: "true", zone: a}}
  - {name: bravo, role: worker, labels: {agents: "true"}}
  - {name: charlie, role: service}
  - {name: delta, role: worker}
  - {name: echo, role: worker}
  - {name: foxtrot, role: worker}
`

const gib = int64(1 << 30)

type capacityFixture struct {
	svc   *HostService
	clock time.Time
}

// newCapacityFixture seeds recorded facts directly, so no probe runs:
// alpha is fresh with plenty free; bravo fresh and small; charlie never
// probed; delta stale; echo offline (its last facts fresh); foxtrot fresh but
// reported no memory; zulu fresh and large, declared first.
func newCapacityFixture(t *testing.T) *capacityFixture {
	t.Helper()
	dir := t.TempDir()
	hostsPath := filepath.Join(dir, "hosts.yaml")
	if err := os.WriteFile(hostsPath, []byte(capacityHostsYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	svc := NewHostService(audit.NewMemory(), HostServiceOptions{HostsPath: hostsPath, StatePath: filepath.Join(dir, "state.json"), Now: func() time.Time { return now }})
	probed := func(cpu int, mem, disk int64, tools map[string]bool) *hosts.Probed {
		return &hosts.Probed{Source: "posix", CPU: cpu, MemoryAvailable: mem, MemoryTotal: 2 * mem, DiskTotal: 2 * disk, DiskFree: disk, Tools: tools}
	}
	put := func(name, state string, age time.Duration, p *hosts.Probed) {
		at := now.Add(-age)
		st := hosts.State{State: state, CheckedAt: &at, Probed: p}
		if p != nil {
			st.ProbedAt = &at
		}
		if err := svc.store.Put(name, st); err != nil {
			t.Fatal(err)
		}
	}
	put("alpha", hosts.StateOnline, time.Minute, probed(8, 8*gib, 100*gib, map[string]bool{"docker": true, "tmux": false}))
	put("bravo", hosts.StateOnline, time.Minute, probed(2, gib, 10*gib, map[string]bool{"docker": true}))
	put("delta", hosts.StateOnline, time.Hour, probed(16, 32*gib, 500*gib, map[string]bool{"docker": true}))
	put("echo", hosts.StateOffline, time.Minute, probed(16, 32*gib, 500*gib, map[string]bool{"docker": true}))
	put("foxtrot", hosts.StateOnline, time.Minute, &hosts.Probed{Source: "posix", CPU: 4})
	put("zulu", hosts.StateOnline, time.Minute, probed(32, 64*gib, 900*gib, map[string]bool{"docker": true, "tmux": true}))
	return &capacityFixture{svc: svc, clock: now}
}

func names(vs []HostView) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, v.Host.Name)
	}
	return out
}

func verdictNames(vs []HostVerdict) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, v.Name)
	}
	return out
}

func same(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func TestCapacityMatchesAndKeepsNameOrder(t *testing.T) {
	f := newCapacityFixture(t)
	res, err := f.svc.Capacity(context.Background(), CapacityQuery{MinMemoryAvailable: 2 * hosts.Size(gib), MinCPU: 4})
	if err != nil {
		t.Fatal(err)
	}
	// zulu is declared first and sorts last; no host is preferred over another.
	if got := names(res.Matched); !same(got, []string{"alpha", "zulu"}) {
		t.Fatalf("matched = %v", got)
	}
	if res.Matched[0].Probed == nil || res.Matched[0].ProbedAt == nil || res.Matched[0].Stale {
		t.Fatalf("a match carries its facts and age: %+v", res.Matched[0])
	}
	// Excluded hosts are not listed unless the query asks to explain.
	if len(res.Excluded) != 0 {
		t.Fatalf("excluded listed without explain: %+v", res.Excluded)
	}
}

func TestCapacityNeverGuessesAtHostsItCannotJudge(t *testing.T) {
	f := newCapacityFixture(t)
	res, err := f.svc.Capacity(context.Background(), CapacityQuery{MinMemoryAvailable: hosts.Size(gib)})
	if err != nil {
		t.Fatal(err)
	}
	// charlie: never probed. delta: an hour old. foxtrot: no memory fact.
	if got := verdictNames(res.Unknown); !same(got, []string{"charlie", "delta", "foxtrot"}) {
		t.Fatalf("unknown = %v", got)
	}
	reasons := map[string]string{}
	for _, u := range res.Unknown {
		reasons[u.Name] = strings.Join(u.Reasons, ";")
		if !strings.Contains(u.NextStep, "host probe "+u.Name) {
			t.Errorf("%s: next step = %q", u.Name, u.NextStep)
		}
	}
	for name, want := range map[string]string{"charlie": "no probe data", "delta": "beyond the 5m0s limit", "foxtrot": "did not report memory_available"} {
		if !strings.Contains(reasons[name], want) {
			t.Errorf("%s: reasons %q lack %q", name, reasons[name], want)
		}
	}
	for _, m := range res.Matched {
		if m.Host.Name == "charlie" || m.Host.Name == "delta" || m.Host.Name == "foxtrot" {
			t.Fatalf("%s matched without fresh facts", m.Host.Name)
		}
	}

	// A longer age limit admits delta's hour-old facts, and its view says stale.
	res, err = f.svc.Capacity(context.Background(), CapacityQuery{MinMemoryAvailable: hosts.Size(gib), MaxStaleAge: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	var delta *HostView
	for i := range res.Matched {
		if res.Matched[i].Host.Name == "delta" {
			delta = &res.Matched[i]
		}
	}
	if delta == nil || !delta.Stale || delta.ProbedAt == nil {
		t.Fatalf("delta under a 2h limit = %+v", delta)
	}
}

func TestCapacityExplainListsExclusionsWithReasons(t *testing.T) {
	f := newCapacityFixture(t)
	res, err := f.svc.Capacity(context.Background(), CapacityQuery{MinMemoryAvailable: 4 * hosts.Size(gib), Role: "worker", Explain: true})
	if err != nil {
		t.Fatal(err)
	}
	why := map[string]string{}
	for _, x := range res.Excluded {
		why[x.Name] = strings.Join(x.Reasons, ";")
	}
	if !strings.Contains(why["bravo"], "memory_available is 1073741824 bytes, below the 4294967296 bytes required") ||
		!strings.Contains(why["charlie"], "role is service, not worker") ||
		!strings.Contains(why["echo"], "the host is offline") {
		t.Fatalf("exclusions = %v", why)
	}
	if got := names(res.Matched); !same(got, []string{"alpha", "zulu"}) {
		t.Fatalf("matched = %v", got)
	}
	// An offline host is judged on its last facts only when asked.
	res, _ = f.svc.Capacity(context.Background(), CapacityQuery{MinMemoryAvailable: 4 * hosts.Size(gib), IncludeOffline: true})
	if got := names(res.Matched); !same(got, []string{"alpha", "echo", "zulu"}) {
		t.Fatalf("include-offline matched = %v", got)
	}
}

func TestCapacityToolRequirementLabelsAndStaticFilters(t *testing.T) {
	f := newCapacityFixture(t)
	res, err := f.svc.Capacity(context.Background(), CapacityQuery{RequireTools: []string{"tmux"}, Explain: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(res.Matched); !same(got, []string{"zulu"}) {
		t.Fatalf("tmux matched = %v", got)
	}
	why := map[string]string{}
	for _, x := range res.Excluded {
		why[x.Name] = strings.Join(x.Reasons, ";")
	}
	if why["alpha"] != "tmux is not installed" {
		t.Fatalf("alpha = %q", why["alpha"])
	}
	// bravo's probe never said whether tmux is installed: unknown, not absent.
	if got := verdictNames(res.Unknown); !same(got, []string{"bravo", "charlie", "delta", "foxtrot"}) {
		t.Fatalf("unknown = %v", got)
	}

	// Role, provider and labels need no probe data at all.
	res, err = f.svc.Capacity(context.Background(), CapacityQuery{Labels: map[string]string{"agents": "true", "zone": "a"}})
	if err != nil || !same(names(res.Matched), []string{"alpha"}) || len(res.Unknown) != 0 {
		t.Fatalf("label query = %+v, %v", res, err)
	}
	res, _ = f.svc.Capacity(context.Background(), CapacityQuery{Role: "service"})
	if !same(names(res.Matched), []string{"charlie"}) {
		t.Fatalf("role query matched %v", names(res.Matched))
	}
}

// The result names facts and verdicts. It has no field a consumer could read
// as a ranking, a score or a pick, and it is the same order every time.
func TestCapacityResultHasNoRankingField(t *testing.T) {
	f := newCapacityFixture(t)
	res, err := f.svc.Capacity(context.Background(), CapacityQuery{Explain: true})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(res)
	var tree any
	_ = json.Unmarshal(data, &tree)
	forbidden := []string{"rank", "score", "best", "prefer", "recommend", "reserve", "weight", "priority", "pick", "choice", "fit"}
	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				for _, bad := range forbidden {
					if strings.Contains(strings.ToLower(k), bad) {
						t.Errorf("field %s.%s reads as a ranking", path, k)
					}
				}
				walk(child, path+"."+k)
			}
		case []any:
			for _, child := range x {
				walk(child, path+"[]")
			}
		}
	}
	walk(tree, "")
	again, _ := f.svc.Capacity(context.Background(), CapacityQuery{Explain: true})
	if !same(names(res.Matched), names(again.Matched)) || !same(names(res.Matched), []string{"alpha", "bravo", "charlie", "delta", "foxtrot", "zulu"}) {
		t.Fatalf("order = %v then %v", names(res.Matched), names(again.Matched))
	}
}

func TestCapacityRefusesMalformedQueriesWithGuidance(t *testing.T) {
	f := newCapacityFixture(t)
	for name, q := range map[string]CapacityQuery{
		"unknown tool": {RequireTools: []string{"kubectl"}},
		"negative":     {MinCPU: -1},
	} {
		_, err := f.svc.Capacity(context.Background(), q)
		var invalid *HostInvalidError
		if !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	_, err := f.svc.Capacity(context.Background(), CapacityQuery{RequireTools: []string{"kubectl"}})
	if err == nil || !strings.Contains(err.Error(), "docker, tmux, tailscale, caddy") {
		t.Fatalf("guidance = %v", err)
	}
}

func TestCapacityQueryWireForm(t *testing.T) {
	var q CapacityQuery
	if err := json.Unmarshal([]byte(`{"min_memory_available":"4GiB","min_disk_free":1000,"min_cpu":2,"max_stale_age":"10m","require_tools":["docker"],"explain":true}`), &q); err != nil {
		t.Fatal(err)
	}
	if q.MinMemoryAvailable != 4*hosts.Size(gib) || q.MinDiskFree != 1000 || q.MaxStaleAge != 10*time.Minute || !q.Explain || q.RequireTools[0] != "docker" {
		t.Fatalf("decoded = %+v", q)
	}
	back, _ := json.Marshal(q)
	var again CapacityQuery
	if err := json.Unmarshal(back, &again); err != nil || again.MaxStaleAge != q.MaxStaleAge || again.MinMemoryAvailable != q.MinMemoryAvailable {
		t.Fatalf("round trip = %+v, %v", again, err)
	}
	// A constraint this build does not know is refused, never dropped: a
	// dropped constraint would return hosts that do not satisfy the query.
	var invalid *HostInvalidError
	if err := json.Unmarshal([]byte(`{"min_gpu":1}`), &q); !errors.As(err, &invalid) {
		t.Fatalf("unknown field = %v", err)
	}
}
