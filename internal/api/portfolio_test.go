package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/pausectl"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The tests are main's (cerbapi portfolio_health_test.go) over this build's sources: host views
// stand in for main's HostService, an observer for its local connector, and the API route for
// its socket.

type fakeDockerInspector struct {
	containers map[string]*dockerconn.Container
	stacks     map[string]*dockerconn.ComposeStack
}

func (f *fakeDockerInspector) ContainerStatus(_ context.Context, nameOrID string) (*dockerconn.Container, error) {
	if c, ok := f.containers[nameOrID]; ok {
		return c, nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeDockerInspector) ComposePS(_ context.Context, composeFile string) (*dockerconn.ComposeStack, error) {
	if s, ok := f.stacks[composeFile]; ok {
		return s, nil
	}
	return nil, os.ErrNotExist
}

// stateObserver reports a fixed state per resource; any other is unknown.
type stateObserver map[string]string

func (o stateObserver) Observe(_ context.Context, r config.ResourceDef) Observation {
	if st, ok := o[r.ID]; ok {
		return Observation{State: st}
	}
	return Observation{State: "unknown", Detail: "not observed"}
}

func staticConfig(cfg *config.ConfigV2) func() (*config.ConfigV2, ResolveDiagnostics, error) {
	return func() (*config.ConfigV2, ResolveDiagnostics, error) { return cfg, ResolveDiagnostics{}, nil }
}

func portfolioAPI(t *testing.T, cfg *config.ConfigV2, obs Observer, g *gate.Gate, p *Portfolio) *API {
	t.Helper()
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return cfg.Resources, nil },
		Observer: obs, Gate: g, Portfolio: p, Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func getPortfolio(t *testing.T, a *API, query string) (int, string, PortfolioHealthProjection) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/portfolio/health"+query, nil)
	req = req.WithContext(libauth.WithPrincipal(req.Context(), apiKey))
	rec := httptest.NewRecorder()
	a.Mux().ServeHTTP(rec, req)
	var proj PortfolioHealthProjection
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &proj); err != nil {
			t.Fatalf("decode: %v: %s", err, rec.Body.String())
		}
	}
	return rec.Code, rec.Body.String(), proj
}

func TestPortfolioHealthProjectionContractAndSemantics(t *testing.T) {
	now := time.Date(2026, 10, 19, 3, 0, 0, 0, time.UTC)
	freshTime := now.Add(-2 * time.Minute)
	staleTime := now.Add(-48 * time.Hour)

	var workLaptopHits atomic.Int32
	controllerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/readyz" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer controllerTS.Close()
	worker1TS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte(`ok`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer worker1TS.Close()
	hubTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/readyz" {
			_, _ = w.Write([]byte(`{"status":"degraded"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer hubTS.Close()
	workLaptopTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		workLaptopHits.Add(1)
	}))
	defer workLaptopTS.Close()

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	tokenPath := filepath.Join(dir, "outpost.token")
	secretToken := "outpost-" + "secret-token-998877665544"
	if err := os.WriteFile(tokenPath, []byte(secretToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outpostHost := func(name, role, url string, labels map[string]string) hosts.Host {
		return hosts.Host{Name: name, Role: role, Labels: labels,
			Reach: hosts.Reach{Transport: "outpost", Outpost: &hosts.OutpostReach{URL: url, TokenFile: tokenPath}}}
	}
	views := []HostView{
		{Host: outpostHost("controller-1", "operator", controllerTS.URL, nil), State: hosts.StateOnline,
			LastSeen: &freshTime, ProbedAt: &freshTime, CheckedAt: &freshTime, Probed: &hosts.Probed{OS: "Linux", Arch: "amd64", CPU: 16}},
		{Host: outpostHost("worker-1", "worker", worker1TS.URL, nil), State: hosts.StateOnline,
			LastSeen: &freshTime, ProbedAt: &freshTime, CheckedAt: &freshTime, Probed: &hosts.Probed{OS: "Linux", Arch: "amd64",
				Units: map[string]string{"worker-agent.service": "running", "worker-agent": "running", "worker-dead.service": "failed", "worker-dead": "failed"}}},
		{Host: outpostHost("hub", "hub", hubTS.URL, nil), State: hosts.StateOnline, Stale: true,
			LastSeen: &staleTime, ProbedAt: &staleTime, CheckedAt: &staleTime, Probed: &hosts.Probed{OS: "Linux", Arch: "amd64"}},
		{Host: outpostHost("home-laptop", "operator", "http://127.0.0.1:1", nil), State: hosts.StateOffline,
			CheckedAt: &freshTime, Reason: "connection refused"},
		{Host: outpostHost("work-laptop", "operator", workLaptopTS.URL, map[string]string{"lifecycle": "deferred"}), State: hosts.StateUnknown},
	}

	if err := pausectl.PauseService("paused-dev-svc"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pausectl.ResumeService("paused-dev-svc") })

	testSensitiveVal := "sk" + "_live_" + "fixture_redaction_check_9999"
	cfg := &config.ConfigV2{
		Version: 2,
		Projects: []config.ProjectDef{
			{ID: "station", Name: "Station"},
			{ID: "outpost-recovery", Name: "Outpost SSH recovery"},
			{ID: "worker-fleet", Name: "Worker Fleet"},
		},
		Resources: []config.ResourceDef{
			{ID: "paused-dev-svc", Name: "Paused Dev Service", Type: "process", Connector: "local", Project: "station",
				Config: map[string]any{"dir": dir, "command": []string{"/bin/sleep", "1"}, "mode": "dev_session",
					"env": map[string]string{"API_SECRET": testSensitiveVal}}},
			{ID: "on-demand-dev-svc", Name: "On Demand Dev Service", Type: "process", Connector: "local", Project: "station",
				Config: map[string]any{"dir": dir, "command": []string{"/bin/sleep", "1"}, "mode": "dev_session", "auto_start": false}},
			{ID: "station-gateway-ledger", Name: "Station gateway PostgreSQL ledger", Type: "container", Connector: "docker", Project: "station",
				Config: map[string]any{"compose_file": "/srv/station/compose.yaml"}},
			{ID: "station-unhealthy-db", Name: "Station unhealthy container", Type: "container", Connector: "docker", Project: "station",
				Config: map[string]any{"container": "station-unhealthy-pg"}},
			{ID: "remote-worker-agent", Name: "Remote Worker Agent", Type: "process", Connector: "local", Project: "worker-fleet", Host: "worker-1",
				Config: map[string]any{"service_name": "worker-agent.service"}},
			{ID: "remote-worker-dead", Name: "Remote Worker Dead", Type: "process", Connector: "local", Project: "worker-fleet", Host: "worker-1",
				Config: map[string]any{"service_name": "worker-dead.service"}},
			{ID: "outpost-recovery-worker-1", Name: "Outpost SSH recovery worker-1", Type: "server", Connector: "ssh", Project: "outpost-recovery",
				Config: map[string]any{"host": "192.0.2.52"}},
		},
	}
	dockerFake := &fakeDockerInspector{
		containers: map[string]*dockerconn.Container{
			"station-ledger-pg-1":  {ID: "c1", Name: "station-ledger-pg-1", State: "running", Status: "running", Health: "healthy"},
			"station-unhealthy-pg": {ID: "c2", Name: "station-unhealthy-pg", State: "running", Status: "running", Health: "unhealthy"},
		},
		stacks: map[string]*dockerconn.ComposeStack{
			"/srv/station/compose.yaml": {Name: "station", Services: []dockerconn.ComposeService{{Name: "station-ledger-pg-1", State: "running"}}},
		},
	}
	p := NewPortfolio(PortfolioOptions{ControllerID: "controller-1", ProviderInstance: "cerberus-local",
		Now: func() time.Time { return now }, HTTPClient: controllerTS.Client(), Docker: dockerFake, Config: staticConfig(cfg)})
	call := &portfolioCall{Portfolio: p, observer: stateObserver{"paused-dev-svc": "stopped", "on-demand-dev-svc": "stopped"},
		views: func() ([]HostView, error) { return views, nil }}

	proj, err := call.Project(context.Background(), PortfolioHealthQuery{})
	if err != nil {
		t.Fatalf("PortfolioHealth failed: %v", err)
	}
	if proj.SchemaVersion != PortfolioHealthSchemaVersion || proj.Contract != PortfolioHealthContract {
		t.Fatalf("unexpected contract metadata: version=%d contract=%q", proj.SchemaVersion, proj.Contract)
	}
	if proj.ControllerID != "controller-1" || proj.ProviderInstance != "cerberus-local" {
		t.Fatalf("unexpected controller metadata: %+v", proj)
	}
	if workLaptopHits.Load() != 0 {
		t.Fatalf("deferred host work-laptop was probed %d times, want 0", workLaptopHits.Load())
	}

	hostByName := map[string]PortfolioHostHealth{}
	for _, h := range proj.Hosts {
		hostByName[h.Name] = h
	}
	if got := hostByName["controller-1"]; got.Readiness != ReadinessReady || got.ReadinessSource != "outpost_readyz" || got.RollupStatus != RollupHealthy {
		t.Fatalf("controller-1 host = %+v", got)
	}
	if got := hostByName["worker-1"]; got.Readiness != ReadinessUnknown || got.ReadinessSource != "outpost_healthz" || got.ReadinessDetail != "liveness_only_healthz" {
		t.Fatalf("worker-1 host readiness should be liveness_only_healthz, got %+v", got)
	}
	if got := hostByName["hub"]; got.Readiness != ReadinessDegraded || got.RollupStatus != RollupDegraded {
		t.Fatalf("hub host = %+v", got)
	}
	if got := hostByName["work-laptop"]; got.Lifecycle != "deferred" || got.Reachability != "deferred" || got.RollupStatus != RollupExpectedStopped {
		t.Fatalf("work-laptop host = %+v", got)
	}

	depByID := map[string]PortfolioDeploymentHealth{}
	for _, d := range proj.Deployments {
		depByID[d.ID] = d
	}
	if got := depByID["paused-dev-svc"]; got.RollupStatus != RollupExpectedStopped || !got.OperatorStopped || got.DesiredState != "stopped" {
		t.Fatalf("paused-dev-svc = %+v", got)
	}
	if got := depByID["on-demand-dev-svc"]; got.RollupStatus != RollupExpectedStopped || got.DesiredState != "on_demand" {
		t.Fatalf("on-demand-dev-svc = %+v", got)
	}
	if got := depByID["station-gateway-ledger"]; got.Kind != "shared_infra" || got.Supervision != "docker_engine" || got.Readiness != ReadinessReady || got.ReadinessSource != "docker_healthcheck" || got.RollupStatus != RollupHealthy {
		t.Fatalf("station-gateway-ledger = %+v", got)
	}
	if got := depByID["station-unhealthy-db"]; got.Readiness != ReadinessUnready || got.RollupStatus != RollupDegraded {
		t.Fatalf("station-unhealthy-db = %+v", got)
	}
	if got := depByID["remote-worker-agent"]; got.Kind != "remote_service" || got.Host != "worker-1" || got.ObservedState != "running" || got.RollupStatus != RollupHealthy {
		t.Fatalf("remote-worker-agent = %+v", got)
	}
	if got := depByID["remote-worker-dead"]; got.Kind != "remote_service" || got.ObservedState != "failed" || got.RollupStatus != RollupDown {
		t.Fatalf("remote-worker-dead = %+v", got)
	}
	if got := depByID["outpost-recovery-worker-1"]; got.Kind != "connector_handle" || got.Supervision != "unmanaged_handle" || got.RollupStatus != RollupUnsupervised {
		t.Fatalf("outpost-recovery-worker-1 = %+v", got)
	}

	projByID := map[string]PortfolioProjectHealth{}
	for _, pr := range proj.Projects {
		projByID[pr.ID] = pr
	}
	if got := projByID["outpost-recovery"]; got.RollupStatus != RollupUnsupervised {
		t.Fatalf("outpost-recovery project rollup = %+v, want unsupervised", got)
	}
	if got := projByID["station"]; got.RollupStatus != RollupDegraded {
		t.Fatalf("station project rollup = %+v, want degraded", got)
	}
	if got := projByID["worker-fleet"]; got.RollupStatus != RollupDown {
		t.Fatalf("worker-fleet project rollup = %+v, want down", got)
	}

	raw, err := json.Marshal(proj)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{secretToken, testSensitiveVal} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret %q leaked in projection JSON", secret)
		}
	}
}

// denyPDP denies a read of the named targets and allows everything else.
type denyPDP map[string]bool

func (d denyPDP) Authorize(req policy.Request) policy.Result {
	if req.Operation == "read" && d[req.Target.ID] {
		return policy.Result{Decision: policy.Deny, WouldBlock: true}
	}
	return policy.Result{Decision: policy.Allow}
}

func (denyPDP) GlobalPosture() string { return policy.PosturePermissive }

// Main's pre-aggregation authorization, over the API: rows the caller filter or policy hides
// are dropped before the summary and coverage are counted, the projection says it was
// filtered, and the read is recorded as main's portfolio.health.
func TestPortfolioHealthPreAggregationAuthorization(t *testing.T) {
	cfg := &config.ConfigV2{
		Version: 2,
		Projects: []config.ProjectDef{
			{ID: "public-proj", Name: "Public Project"},
			{ID: "secret-proj", Name: "Secret Project"},
		},
		Resources: []config.ResourceDef{
			{ID: "public-svc", Name: "Public Service", Type: "server", Connector: "ssh", Project: "public-proj",
				Env: "dev", Owner: "team-a", Admin: target.Admin{Default: "self"}},
			{ID: "restricted-svc", Name: "Restricted Service", Type: "process", Connector: "local", Project: "secret-proj",
				Env: "prod", Owner: "team-b", Admin: target.Admin{Default: "self"},
				Config: map[string]any{"dir": t.TempDir(), "command": []string{"/bin/false"}, "mode": "os_service"}},
		},
	}
	filter := func(_ context.Context, _, id string, labels target.Labels) bool {
		return labels.Env != target.EnvProd && id != "secret-proj"
	}
	g := gatetest.New(t, gatetest.Options{Policy: denyPDP{"hidden-plugin": true}, Enforcement: enforceAll{}})
	p := NewPortfolio(PortfolioOptions{ControllerID: "controller-1", TargetFilter: filter, Config: staticConfig(cfg),
		Connectors: func(context.Context) (defs, live []contract.Definition) {
			d := contract.Definition{ID: "ssh", Version: "builtin"}
			return []contract.Definition{d}, []contract.Definition{d}
		},
		Plugins: func(context.Context) []admin.PluginState {
			return []admin.PluginState{{ID: "hidden-plugin", Loaded: true}, {ID: "shown-plugin", GaveUp: "crashed"}}
		}})
	a := portfolioAPI(t, cfg, stateObserver{"restricted-svc": "failed"}, g.Gate, p)

	code, body, proj := getPortfolio(t, a, "")
	if code != http.StatusOK {
		t.Fatalf("portfolio: %d %s", code, body)
	}
	if !proj.Authorization.Filtered || proj.Authorization.Scope == "" || proj.Authorization.PrincipalKind == "[REDACTED]" {
		t.Fatalf("authorization = %+v", proj.Authorization)
	}
	if len(proj.Deployments) != 1 || proj.Deployments[0].ID != "public-svc" {
		t.Fatalf("expected only public-svc in deployments, got %+v", proj.Deployments)
	}
	if len(proj.Projects) != 1 || proj.Projects[0].ID != "public-proj" {
		t.Fatalf("expected only public-proj in projects, got %+v", proj.Projects)
	}
	if proj.Summary.ByStatus[RollupDown] != 0 || proj.Coverage.ResourcesDeclared != 1 {
		t.Fatalf("restricted-svc leaked into the counts: %+v %+v", proj.Summary.ByStatus, proj.Coverage)
	}
	conns := map[string]string{}
	for _, c := range proj.Connectors {
		conns[c.ID] = c.RollupStatus
	}
	if len(conns) != 2 || conns["ssh"] != RollupHealthy || conns["shown-plugin"] != RollupDown {
		t.Fatalf("connectors = %+v (hidden-plugin is denied by policy)", proj.Connectors)
	}
	var recorded int
	for _, r := range g.Records(audit.KindIntent) {
		if r.Connector == "portfolio" && r.Operation == "health" {
			recorded++
		}
	}
	if recorded != 1 {
		t.Fatalf("portfolio.health recorded %d times", recorded)
	}
	if code, body, _ := getPortfolio(t, a, "?mode=fast"); code != http.StatusBadRequest || !strings.Contains(body, `"code":"invalid_args"`) {
		t.Fatalf("bad mode: %d %s", code, body)
	}
	if code, body, _ := getPortfolio(t, portfolioAPI(t, cfg, nil, nil, nil), ""); code != http.StatusServiceUnavailable || !strings.Contains(body, "portfolio_unavailable") {
		t.Fatalf("no portfolio: %d %s", code, body)
	}
}

func TestPortfolioHealthReadinessVsLivenessAndRoutes(t *testing.T) {
	var wildcardProbed atomic.Int32
	hcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ready":
			_, _ = w.Write([]byte(`{"ready":true}`))
		case "/unready":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"ready":false}`))
		default:
			wildcardProbed.Add(1)
		}
	}))
	defer hcServer.Close()

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	devSvc := func(id string, extra map[string]any) config.ResourceDef {
		c := map[string]any{"dir": dir, "command": []string{"/bin/sleep", "30"}, "mode": "dev_session"}
		for k, v := range extra {
			c[k] = v
		}
		return config.ResourceDef{ID: id, Name: id, Type: "process", Connector: "local", Project: "app", Config: c}
	}
	cfg := &config.ConfigV2{
		Version:  2,
		Projects: []config.ProjectDef{{ID: "app", Name: "App"}},
		Resources: []config.ResourceDef{
			devSvc("pid-only-svc", map[string]any{"url": "http://127.0.0.1:19999"}),
			devSvc("wildcard-url-svc", map[string]any{"health": "http://0.0.0.0:19999/healthz"}),
			devSvc("hc-ready-svc", map[string]any{"health_check": map[string]any{"url": hcServer.URL + "/ready"}}),
			devSvc("hc-unready-svc", map[string]any{"health_check": map[string]any{"url": hcServer.URL + "/unready"}}),
		},
	}
	running := stateObserver{"pid-only-svc": "running", "wildcard-url-svc": "running", "hc-ready-svc": "running", "hc-unready-svc": "running"}
	a := portfolioAPI(t, cfg, running, nil, NewPortfolio(PortfolioOptions{HTTPClient: hcServer.Client(), Config: staticConfig(cfg)}))

	code, body, proj := getPortfolio(t, a, "?mode=live_bounded")
	if code != http.StatusOK {
		t.Fatalf("portfolio: %d %s", code, body)
	}
	if proj.ControllerID == "" || proj.ControllerID != DefaultControllerID() || strings.Contains(proj.ControllerID, ".") {
		t.Fatalf("controller_id = %q, want this host's short name %q", proj.ControllerID, DefaultControllerID())
	}
	byID := map[string]PortfolioDeploymentHealth{}
	for _, d := range proj.Deployments {
		byID[d.ID] = d
	}
	if got := byID["pid-only-svc"]; got.ObservedState != "running" || got.Readiness != ReadinessUnknown || got.ReadinessSource != "none" || got.ReadinessDetail != "process_liveness_only" || got.RollupStatus != RollupHealthy {
		t.Fatalf("pid-only-svc = %+v", got)
	}
	if got := byID["wildcard-url-svc"]; got.ObservedState != "running" || got.Readiness != ReadinessUnknown || got.ReadinessDetail != "wildcard_bind_url_not_probed" {
		t.Fatalf("wildcard-url-svc = %+v", got)
	}
	if wildcardProbed.Load() != 0 {
		t.Fatalf("wildcard URL was probed %d times", wildcardProbed.Load())
	}
	if got := byID["hc-ready-svc"]; got.ObservedState != "running" || got.Readiness != ReadinessReady || got.ReadinessSource != "healthcheck_http" || got.RollupStatus != RollupHealthy {
		t.Fatalf("hc-ready-svc = %+v", got)
	}
	if got := byID["hc-unready-svc"]; got.ObservedState != "running" || got.Readiness != ReadinessUnready || got.ReadinessSource != "healthcheck_http" || got.RollupStatus != RollupDegraded {
		t.Fatalf("hc-unready-svc = %+v", got)
	}

	code, body, filtered := getPortfolio(t, a, "?rollup_status=degraded")
	if code != http.StatusOK || len(filtered.Deployments) != 1 || filtered.Deployments[0].ID != "hc-unready-svc" {
		t.Fatalf("rollup_status=degraded: %d %s", code, body)
	}
}
