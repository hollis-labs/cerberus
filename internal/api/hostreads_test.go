package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
	libauth "github.com/hollis-labs/libs/security/auth"
	"gopkg.in/yaml.v3"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/hostmon"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/storage"
	"github.com/hollis-labs/cerberus/pkg/outpost"
)

const probeOutput = "os=Linux\narch=x86_64\nkernel=6.1.0-test\ncpu=4\nmem_total_kb=8000000\nmem_available_kb=4000000\n" +
	"load=0.10 0.20 0.30\nuptime_seconds=60\ndisk_kb=1000000 500000\ntool_docker=1\ntool_tmux=0\ntool_tailscale=1\ntool_caddy=0\n"

// hostWorld is the L9 golden fixture (testdata/golden-l9) plus an outpost host served by a
// fake executor, with the host reads over a migrated SQLite Telemetry store.
type hostWorld struct {
	root    string
	golden  func(string) string
	store   *hosts.SampleStore
	reads   *hostmon.Reads
	probes  *int
	project []config.ProjectDef
}

func newHostWorld(t *testing.T) *hostWorld {
	t.Helper()
	home := t.TempDir()
	w := &hostWorld{root: filepath.Join(home, ".cerberus"), probes: new(int)}
	if err := os.MkdirAll(filepath.Join(w.root, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	w.golden = func(name string) string {
		b, err := os.ReadFile(filepath.Join("testdata", "golden-l9", name)) //nolint:gosec // fixed test data names
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(b), "@HOME@", home)
	}
	exec := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		*w.probes++
		if r.Header.Get("Authorization") != "Bearer box-token" {
			t.Error("the token file was not sent")
		}
		_ = json.NewEncoder(rw).Encode(outpost.Result{Output: probeOutput})
	}))
	t.Cleanup(exec.Close)
	token := filepath.Join(w.root, "outpost-tokens", "box")
	if err := os.MkdirAll(filepath.Dir(token), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("box-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	var f hosts.File
	if err := yaml.Unmarshal([]byte(w.golden("hosts.yaml")), &f); err != nil {
		t.Fatal(err)
	}
	f.Hosts = append(f.Hosts, hosts.Host{Name: "box", Role: "worker", Provider: "homelab",
		Reach: hosts.Reach{Transport: "outpost", Outpost: &hosts.OutpostReach{URL: exec.URL, TokenFile: token}}})
	raw, _ := yaml.Marshal(f)
	for dst, body := range map[string]string{filepath.Join(w.root, "hosts.yaml"): string(raw), filepath.Join(w.root, "state", "hosts.json"): w.golden("state-hosts.json")} {
		if err := os.WriteFile(dst, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	cfg := store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: filepath.Join(t.TempDir(), "cerberus.db"), CreateParentDir: true}}
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fsys, root := storage.Migrations()
	if err = migrate.Ensure(ctx, cfg, st, migrate.Source{FS: fsys, Root: root}); err != nil {
		t.Fatal(err)
	}
	w.store = hosts.NewSampleStore(st)
	prober, err := hostmon.NewProber(hostmon.ProbeOptions{HostsPath: filepath.Join(w.root, "hosts.yaml"), StatePath: filepath.Join(w.root, "state", "hosts.json")})
	if err != nil {
		t.Fatal(err)
	}
	ms := hosts.DefaultMonitorSettings()
	ms.Enabled = true
	m := hostmon.New(hostmon.Options{Prober: prober, Store: w.store, Settings: ms})
	w.reads = &hostmon.Reads{Prober: prober, Store: w.store, Monitor: m, Settings: ms}
	w.project = []config.ProjectDef{{ID: "shop", Name: "Shop", Description: "the shop", Capabilities: []string{"web"}}}
	return w
}

func (w *hostWorld) api(t *testing.T, g *gatetest.World) *API {
	t.Helper()
	o := Options{Resources: func() ([]config.ResourceDef, error) { return fixture(), nil },
		Projects:  func() ([]config.ProjectDef, error) { return w.project, nil },
		Hosts:     FileHosts{HostsPath: filepath.Join(w.root, "hosts.yaml"), StatePath: filepath.Join(w.root, "state", "hosts.json")},
		HostReads: w.reads, Home: t.TempDir()}
	if g != nil {
		o.Gate = g.Gate
	}
	a, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func get(t *testing.T, a *API, method, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req = req.WithContext(libauth.WithPrincipal(req.Context(), apiKey))
	rec := httptest.NewRecorder()
	a.Mux().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// GET /api/hosts/{name} is the same view L9 lists, which is main's (testdata/golden-l9); with no
// monitor status recorded it carries none, as main's.
func TestHostGetIsTheOldView(t *testing.T) {
	w := newHostWorld(t)
	a := w.api(t, nil)
	code, body := get(t, a, "GET", "/api/hosts/zeta")
	var list []any
	if err := json.Unmarshal([]byte(w.golden("list.json")), &list); err != nil {
		t.Fatal(err)
	}
	var want, got any
	for _, v := range list {
		if v.(map[string]any)["host"].(map[string]any)["name"] == "zeta" {
			want = v
		}
	}
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("GET /api/hosts/zeta: %d\n got %s\nwant %v", code, body, want)
	}
	if code, body = get(t, a, "GET", "/api/hosts/nope"); code != 404 || !strings.Contains(body, `"code":"not_found"`) {
		t.Fatalf("unknown host: %d %s", code, body)
	}
}

// In supervise mode every host read is recorded as main's host operation, and a probe measures
// over the outpost with the token file and records the state; status, samples, events and the
// schedule answer with main's shapes. In observe mode a probe is refused (it would write the old
// build's state file) and the reads are not recorded.
func TestHostReadsAreRecordedAndAnswerAsMain(t *testing.T) {
	w := newHostWorld(t)
	g := gatetest.New(t, gatetest.Options{})
	a := w.api(t, g)
	ops := func() []string {
		var out []string
		for _, r := range g.Records(audit.KindIntent) {
			out = append(out, r.Connector+"."+r.Operation)
		}
		return out
	}

	code, body := get(t, a, "POST", "/api/hosts/box/probe")
	if code != 200 || *w.probes != 1 || !strings.Contains(body, `"state":"online"`) || !strings.Contains(body, `"cpu":4`) {
		t.Fatalf("probe: %d %s (probes %d)", code, body, *w.probes)
	}
	at := time.Now().Add(-time.Minute)
	load := 0.5
	if err := w.store.AppendSample(context.Background(), hosts.Sample{Host: "box", At: at, Load1: &load, CPU: 4, ProbeMS: 12}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.AppendEvent(context.Background(), hosts.Event{At: at, Host: "box", Kind: hosts.EventStatusChanged, From: "unknown", To: "online"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ path, want string }{
		{"/api/hosts/box/status", `"monitored":true,"name":"box"`},
		{"/api/hosts/zeta/status", `"code":"never_probed"`},
		{"/api/hosts/mid/status", `"code":"not_monitored"`},
		{"/api/hosts/_status", `"name":"alpha"`},
		{"/api/hosts/box/samples?since=1h", `"cpu":4`},
		{"/api/hosts/box/samples?grain=5m", `[]`},
		{"/api/hosts/box/samples?grain=2m", `"code":"invalid_args"`},
		{"/api/hosts/box/samples?since=yesterday", `"code":"invalid_args"`},
		{"/api/hosts/_events?host=box", `"to":"online"`},
		{"/api/hosts/_monitor", `"name":"box"`},
		{"/api/projects", `"description":"the shop","id":"shop","name":"Shop","resource_count":`},
	} {
		if code, body := get(t, a, "GET", c.path); !strings.Contains(body, c.want) || (code != 200 && !strings.Contains(c.want, "invalid_args")) {
			t.Errorf("GET %s: %d %s, want %s", c.path, code, body, c.want)
		}
	}
	recorded := strings.Join(ops(), " ")
	for _, op := range []string{"host.probe", "host.host_status", "host.host_samples", "host.host_events", "host.host_monitor"} {
		if !strings.Contains(recorded, op) {
			t.Errorf("%s was not recorded: %s", op, recorded)
		}
	}

	observe := w.api(t, nil)
	if code, body := get(t, observe, "POST", "/api/hosts/box/probe"); code != http.StatusConflict || !strings.Contains(body, `"code":"observe_mode"`) || *w.probes != 1 {
		t.Fatalf("probe in observe mode: %d %s (probes %d)", code, body, *w.probes)
	}
	if code, body := get(t, observe, "GET", "/api/hosts/box/status"); code != 200 || !strings.Contains(body, `"name":"box"`) {
		t.Fatalf("status in observe mode: %d %s", code, body)
	}
}

// Every host read answers through the request's redaction scope, as main's socket did: a secret in
// a stored reason (here a bearer token an outpost echoed) never reaches the caller, in the host
// view (with its MCP note built from it), in L9, or in the status.
func TestHostReadsAreRedacted(t *testing.T) {
	w := newHostWorld(t)
	// Assembled at run time, so the repository holds no credential-shaped string.
	secret := "ghp" + "_" + strings.Repeat("Zq7x", 9)
	st := `{"version":1,"hosts":{"zeta":{"state":"offline","reason":"the host did not answer over outpost: Bearer ` + secret + `","checked_at":"2026-01-02T03:04:05Z"}}}`
	if err := os.WriteFile(filepath.Join(w.root, "state", "hosts.json"), []byte(st), 0o600); err != nil {
		t.Fatal(err)
	}
	a := w.api(t, gatetest.New(t, gatetest.Options{}))
	for _, path := range []string{"/api/hosts/zeta", "/api/hosts", "/api/hosts/_status"} {
		code, body := get(t, a, "GET", path)
		if code != 200 || strings.Contains(body, secret) {
			t.Fatalf("GET %s leaks the token: %d %s", path, code, body)
		}
		if path != "/api/hosts/_status" && !strings.Contains(body, "[REDACTED]") {
			t.Fatalf("GET %s: no redaction marker: %s", path, body)
		}
	}
}
