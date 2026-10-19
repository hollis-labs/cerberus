package cerbapi

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

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/pausectl"
	"github.com/hollis-labs/cerberus/internal/target"
)

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

func TestPortfolioHealthProjectionContractAndSemantics(t *testing.T) {
	now := time.Date(2026, 10, 19, 3, 0, 0, 0, time.UTC)
	freshTime := now.Add(-2 * time.Minute)
	staleTime := now.Add(-48 * time.Hour)

	// Fake HTTP servers for Outpost readyz/healthz (each Outpost endpoint must be a pure origin).
	var workLaptopHits atomic.Int32
	agentOSTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/readyz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer agentOSTS.Close()

	worker1TS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/readyz":
			w.WriteHeader(http.StatusNotFound)
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`ok`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer worker1TS.Close()

	hubTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/readyz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"degraded"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer hubTS.Close()

	workLaptopTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		workLaptopHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer workLaptopTS.Close()

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	tokenPath := filepath.Join(dir, "outpost.token")
	secretToken := "outpost-secret-token-998877665544"
	if err := os.WriteFile(tokenPath, []byte(secretToken+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	hostsPath := filepath.Join(dir, "hosts.yaml")
	hostsJSON := `{
  "version": 1,
  "hosts": [
    {
      "name": "agent-os",
      "role": "operator",
      "reach": {
        "transport": "outpost",
        "outpost": {"url": "` + agentOSTS.URL + `", "token_file": "` + tokenPath + `"}
      }
    },
    {
      "name": "worker-1",
      "role": "worker",
      "reach": {
        "transport": "outpost",
        "outpost": {"url": "` + worker1TS.URL + `", "token_file": "` + tokenPath + `"}
      }
    },
    {
      "name": "hub",
      "role": "hub",
      "reach": {
        "transport": "outpost",
        "outpost": {"url": "` + hubTS.URL + `", "token_file": "` + tokenPath + `"}
      }
    },
    {
      "name": "home-laptop",
      "role": "operator",
      "reach": {
        "transport": "outpost",
        "outpost": {"url": "http://127.0.0.1:1", "token_file": "` + tokenPath + `"}
      }
    },
    {
      "name": "work-laptop",
      "role": "operator",
      "labels": {"lifecycle": "deferred"},
      "reach": {
        "transport": "outpost",
        "outpost": {"url": "` + workLaptopTS.URL + `", "token_file": "` + tokenPath + `"}
      }
    }
  ]
}`
	if err := os.WriteFile(hostsPath, []byte(hostsJSON), 0600); err != nil {
		t.Fatal(err)
	}

	sink := audit.NewMemory()
	hostSvc := NewHostService(sink, HostServiceOptions{
		HostsPath:  hostsPath,
		StatePath:  "-",
		StaleAfter: 24 * time.Hour,
		Now:        func() time.Time { return now },
	})
	_ = hostSvc.store.Put("agent-os", hosts.State{
		State:     hosts.StateOnline,
		LastSeen:  &freshTime,
		ProbedAt:  &freshTime,
		CheckedAt: &freshTime,
		Probed:    &hosts.Probed{OS: "Linux", Arch: "amd64", CPU: 16},
	})
	_ = hostSvc.store.Put("worker-1", hosts.State{
		State:     hosts.StateOnline,
		LastSeen:  &freshTime,
		ProbedAt:  &freshTime,
		CheckedAt: &freshTime,
		Probed: &hosts.Probed{
			OS:   "Linux",
			Arch: "amd64",
			Units: map[string]string{
				"worker-agent.service": "running",
				"worker-agent":         "running",
				"worker-dead.service":  "failed",
				"worker-dead":          "failed",
			},
		},
	})
	_ = hostSvc.store.Put("hub", hosts.State{
		State:     hosts.StateOnline,
		LastSeen:  &staleTime,
		ProbedAt:  &staleTime,
		CheckedAt: &staleTime,
		Probed:    &hosts.Probed{OS: "Linux", Arch: "amd64"},
	})
	_ = hostSvc.store.Put("home-laptop", hosts.State{
		State:     hosts.StateOffline,
		CheckedAt: &freshTime,
		Reason:    "connection refused",
	})

	if err := pausectl.PauseService("paused-dev-svc"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pausectl.ResumeService("paused-dev-svc") })

	testSensitiveVal := "sk_live_" + "fixture_redaction_check_9999" //nolint:gosec // test fixture for redaction verification
	cfg := &config.ConfigV2{
		Version: 2,
		Projects: []config.ProjectDef{
			{ID: "station", Name: "Station"},
			{ID: "outpost-recovery", Name: "Outpost SSH recovery"},
			{ID: "worker-fleet", Name: "Worker Fleet"},
		},
		Resources: []config.ResourceDef{
			{
				ID:        "paused-dev-svc",
				Name:      "Paused Dev Service",
				Type:      "process",
				Connector: "local",
				Project:   "station",
				Config: map[string]any{
					"dir":     dir,
					"command": []string{"/bin/sleep", "1"},
					"mode":    "dev_session",
					"env": map[string]string{
						"API_SECRET": testSensitiveVal,
					},
				},
			},
			{
				ID:        "on-demand-dev-svc",
				Name:      "On Demand Dev Service",
				Type:      "process",
				Connector: "local",
				Project:   "station",
				Config: map[string]any{
					"dir":        dir,
					"command":    []string{"/bin/sleep", "1"},
					"mode":       "dev_session",
					"auto_start": false,
				},
			},
			{
				ID:        "station-gateway-ledger",
				Name:      "Station gateway PostgreSQL ledger",
				Type:      "container",
				Connector: "docker",
				Project:   "station",
				Config: map[string]any{
					"compose_file": "/srv/station/compose.yaml",
				},
			},
			{
				ID:        "station-unhealthy-db",
				Name:      "Station unhealthy container",
				Type:      "container",
				Connector: "docker",
				Project:   "station",
				Config: map[string]any{
					"container": "station-unhealthy-pg",
				},
			},
			{
				ID:        "remote-worker-agent",
				Name:      "Remote Worker Agent",
				Type:      "process",
				Connector: "local",
				Project:   "worker-fleet",
				Host:      "worker-1",
				Config: map[string]any{
					"service_name": "worker-agent.service",
				},
			},
			{
				ID:        "remote-worker-dead",
				Name:      "Remote Worker Dead",
				Type:      "process",
				Connector: "local",
				Project:   "worker-fleet",
				Host:      "worker-1",
				Config: map[string]any{
					"service_name": "worker-dead.service",
				},
			},
			{
				ID:        "outpost-recovery-worker-1",
				Name:      "Outpost SSH recovery worker-1",
				Type:      "server",
				Connector: "ssh",
				Project:   "outpost-recovery",
				Config: map[string]any{
					"host": "192.168.1.252",
				},
			},
		},
	}

	runtimeSvc := NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(cfg))
	dockerFake := &fakeDockerInspector{
		containers: map[string]*dockerconn.Container{
			"station-ledger-pg-1": {
				ID:     "c1",
				Name:   "station-ledger-pg-1",
				State:  "running",
				Status: "running",
				Health: "healthy",
			},
			"station-unhealthy-pg": {
				ID:     "c2",
				Name:   "station-unhealthy-pg",
				State:  "running",
				Status: "running",
				Health: "unhealthy",
			},
		},
		stacks: map[string]*dockerconn.ComposeStack{
			"/srv/station/compose.yaml": {
				Name: "station",
				Services: []dockerconn.ComposeService{
					{Name: "station-ledger-pg-1", State: "running"},
				},
			},
		},
	}

	client := NewInProcessClient(
		WithInProcessAudit(sink),
		WithResourceRuntimeService(runtimeSvc),
		WithHostService(hostSvc),
		WithPortfolioHealthOptions(PortfolioHealthOptions{
			ControllerID:     "agent-os",
			ProviderInstance: "cerberus-local",
			Now:              func() time.Time { return now },
			HTTPClient:       agentOSTS.Client(),
			Docker:           dockerFake,
		}),
	)

	proj, err := client.PortfolioHealth(context.Background(), PortfolioHealthQuery{})
	if err != nil {
		t.Fatalf("PortfolioHealth failed: %v", err)
	}

	if proj.SchemaVersion != PortfolioHealthSchemaVersion || proj.Contract != PortfolioHealthContract {
		t.Fatalf("unexpected contract metadata: version=%d contract=%q", proj.SchemaVersion, proj.Contract)
	}
	if proj.ControllerID != "agent-os" || proj.ProviderInstance != "cerberus-local" {
		t.Fatalf("unexpected controller metadata: %+v", proj)
	}

	// Verify work-laptop was NEVER probed over HTTP.
	if workLaptopHits.Load() != 0 {
		t.Fatalf("deferred host work-laptop was probed %d times, want 0", workLaptopHits.Load())
	}

	// Verify host readiness and fallback semantics.
	hostByName := map[string]PortfolioHostHealth{}
	for _, h := range proj.Hosts {
		hostByName[h.Name] = h
	}
	if got := hostByName["agent-os"]; got.Readiness != ReadinessReady || got.ReadinessSource != "outpost_readyz" || got.RollupStatus != RollupHealthy {
		t.Fatalf("agent-os host = %+v", got)
	}
	// worker-1 had 404 on /api/readyz and 200 on /healthz -> liveness_only_healthz, never ReadinessReady!
	if got := hostByName["worker-1"]; got.Readiness != ReadinessUnknown || got.ReadinessSource != "outpost_healthz" || got.ReadinessDetail != "liveness_only_healthz" {
		t.Fatalf("worker-1 host readiness should be liveness_only_healthz, got %+v", got)
	}
	// hub had {"status":"degraded"} on /api/readyz -> degraded!
	if got := hostByName["hub"]; got.Readiness != ReadinessDegraded || got.RollupStatus != RollupDegraded {
		t.Fatalf("hub host = %+v", got)
	}
	// work-laptop is deferred -> expected_stopped.
	if got := hostByName["work-laptop"]; got.Lifecycle != "deferred" || got.Reachability != "deferred" || got.RollupStatus != RollupExpectedStopped {
		t.Fatalf("work-laptop host = %+v", got)
	}

	// Verify deployments.
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

	// Verify project rollups: outpost-recovery has only connector handles -> unsupervised.
	projByID := map[string]PortfolioProjectHealth{}
	for _, p := range proj.Projects {
		projByID[p.ID] = p
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

	// Verify no secrets leaked anywhere in the JSON output.
	raw, err := json.Marshal(proj)
	if err != nil {
		t.Fatal(err)
	}
	jsonStr := string(raw)
	for _, secret := range []string{secretToken, testSensitiveVal} {
		if strings.Contains(jsonStr, secret) {
			t.Fatalf("secret %q leaked in projection JSON", secret)
		}
	}
}

func TestPortfolioHealthPreAggregationAuthorizationAndSocket(t *testing.T) {
	sink := audit.NewMemory()
	cfg := &config.ConfigV2{
		Version: 2,
		Projects: []config.ProjectDef{
			{ID: "public-proj", Name: "Public Project"},
			{ID: "secret-proj", Name: "Secret Project"},
		},
		Resources: []config.ResourceDef{
			{
				ID:        "public-svc",
				Name:      "Public Service",
				Type:      "server",
				Connector: "ssh",
				Project:   "public-proj",
				Env:       "dev",
				Owner:     "team-a",
				Admin:     target.Admin{Default: "self"},
			},
			{
				ID:        "restricted-svc",
				Name:      "Restricted Service",
				Type:      "process",
				Connector: "local",
				Project:   "secret-proj",
				Env:       "prod",
				Owner:     "team-b",
				Admin:     target.Admin{Default: "self"},
				Config: map[string]any{
					"dir":     t.TempDir(),
					"command": []string{"/bin/false"},
					"mode":    "os_service",
				},
			},
		},
	}
	runtimeSvc := NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(cfg))

	// Filter out prod targets before aggregation.
	filter := func(_ context.Context, targetType, id string, labels target.Labels) bool {
		if labels.Env == target.EnvProd || id == "secret-proj" {
			return false
		}
		return true
	}

	inProc := NewInProcessClient(
		WithInProcessAudit(sink),
		WithResourceRuntimeService(runtimeSvc),
		WithPortfolioHealthOptions(PortfolioHealthOptions{
			TargetFilter: filter,
		}),
	)

	// 1. OAuth caller with empty scopes is refused before any projection runs.
	unscopedCtx := WithPrincipal(context.Background(), Principal{
		Kind:       PrincipalAgent,
		Via:        ViaMCPHTTP,
		AuthMethod: AuthOAuth,
		Subject:    "agent-1",
		Issuer:     "https://issuer.example",
		Scopes:     []string{},
	})
	if _, err := inProc.PortfolioHealth(unscopedCtx, PortfolioHealthQuery{}); err == nil || connectorErrorCode(err) != ExternalConnectorInsufficientScope {
		t.Fatalf("expected insufficient_scope error for OAuth token without read scope, got %v", err)
	}

	// 2. Authorized caller over unix socket receives filtered rows AND pre-filtered summary counts.
	sockClient := serveOnSocket(t, inProc)
	proj, err := sockClient.PortfolioHealth(context.Background(), PortfolioHealthQuery{})
	if err != nil {
		t.Fatalf("socket PortfolioHealth failed: %v", err)
	}
	if !proj.Authorization.Filtered {
		t.Fatalf("expected Authorization.Filtered=true, got %+v", proj.Authorization)
	}
	if proj.Authorization.Surface != "socket" || proj.Authorization.Scope != "portfolio:read" || proj.Authorization.PrincipalKind == "[REDACTED]" {
		t.Fatalf("unexpected Authorization over socket (should not be [REDACTED]): %+v", proj.Authorization)
	}
	if len(proj.Deployments) != 1 || proj.Deployments[0].ID != "public-svc" {
		t.Fatalf("expected only public-svc in deployments, got %+v", proj.Deployments)
	}
	if len(proj.Projects) != 1 || proj.Projects[0].ID != "public-proj" {
		t.Fatalf("expected only public-proj in projects, got %+v", proj.Projects)
	}
	// Summary counts must NOT include the filtered-out restricted-svc (which would have been "down").
	if proj.Summary.ByStatus[RollupDown] != 0 {
		t.Fatalf("restricted-svc leaked into summary.by_status[down]: %+v", proj.Summary.ByStatus)
	}
	if proj.Coverage.ResourcesDeclared != 1 {
		t.Fatalf("restricted-svc leaked into coverage.resources_declared: %+v", proj.Coverage)
	}
}

func TestPortfolioHealthReadinessVsLivenessAndRoutes(t *testing.T) {
	var wildcardProbed atomic.Int32
	hcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ready":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ready":true}`))
		case "/unready":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"ready":false}`))
		default:
			wildcardProbed.Add(1)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer hcServer.Close()

	dir := t.TempDir()
	sink := audit.NewMemory()
	cfg := &config.ConfigV2{
		Version: 2,
		Projects: []config.ProjectDef{
			{ID: "app", Name: "App"},
		},
		Resources: []config.ResourceDef{
			{
				ID:        "pid-only-svc",
				Name:      "PID Only Service",
				Type:      "process",
				Connector: "local",
				Project:   "app",
				Config: map[string]any{
					"dir":     dir,
					"command": []string{"/bin/sleep", "30"},
					"mode":    "dev_session",
					"url":     "http://127.0.0.1:19999",
				},
			},
			{
				ID:        "wildcard-url-svc",
				Name:      "Wildcard Bind URL Service",
				Type:      "process",
				Connector: "local",
				Project:   "app",
				Config: map[string]any{
					"dir":     dir,
					"command": []string{"/bin/sleep", "30"},
					"mode":    "dev_session",
					"health":  "http://0.0.0.0:19999/healthz",
				},
			},
			{
				ID:        "hc-ready-svc",
				Name:      "Healthcheck Ready Service",
				Type:      "process",
				Connector: "local",
				Project:   "app",
				Config: map[string]any{
					"dir":     dir,
					"command": []string{"/bin/sleep", "30"},
					"mode":    "dev_session",
					"health_check": map[string]any{
						"url": hcServer.URL + "/ready",
					},
				},
			},
			{
				ID:        "hc-unready-svc",
				Name:      "Healthcheck Unready Service",
				Type:      "process",
				Connector: "local",
				Project:   "app",
				Config: map[string]any{
					"dir":     dir,
					"command": []string{"/bin/sleep", "30"},
					"mode":    "dev_session",
					"health_check": map[string]any{
						"url": hcServer.URL + "/unready",
					},
				},
			},
		},
	}

	runtimeSvc := NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(cfg))
	ctx := context.Background()
	for _, id := range []string{"pid-only-svc", "wildcard-url-svc", "hc-ready-svc", "hc-unready-svc"} {
		res := findResourceDef(cfg, id)
		if _, err := runtimeSvc.localConnector().Apply(ctx, resourceDefToDomain(res)); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
		id := id
		t.Cleanup(func() {
			r := findResourceDef(cfg, id)
			_ = runtimeSvc.localConnector().Stop(context.Background(), resourceDefToDomain(r))
		})
	}

	inProc := NewInProcessClient(
		WithInProcessAudit(sink),
		WithResourceRuntimeService(runtimeSvc),
		WithPortfolioHealthOptions(PortfolioHealthOptions{
			HTTPClient: hcServer.Client(),
		}),
	)

	proj, err := inProc.PortfolioHealth(ctx, PortfolioHealthQuery{Mode: PortfolioModeLiveBounded})
	if err != nil {
		t.Fatalf("PortfolioHealth failed: %v", err)
	}

	byID := map[string]PortfolioDeploymentHealth{}
	for _, d := range proj.Deployments {
		byID[d.ID] = d
	}

	// 1. PID-only process must NEVER fake readiness="ready".
	if got := byID["pid-only-svc"]; got.ObservedState != "running" || got.Readiness != ReadinessUnknown || got.ReadinessSource != "none" || got.ReadinessDetail != "process_liveness_only" || got.RollupStatus != RollupHealthy {
		t.Fatalf("pid-only-svc = %+v", got)
	}
	// 2. Wildcard bind URL must not be probed and must report wildcard_bind_url_not_probed.
	if got := byID["wildcard-url-svc"]; got.ObservedState != "running" || got.Readiness != ReadinessUnknown || got.ReadinessDetail != "wildcard_bind_url_not_probed" {
		t.Fatalf("wildcard-url-svc = %+v", got)
	}
	if wildcardProbed.Load() != 0 {
		t.Fatalf("wildcard URL was probed %d times", wildcardProbed.Load())
	}
	// 3. Explicit health_check.url returning 200 -> readiness="ready", source="healthcheck_http".
	if got := byID["hc-ready-svc"]; got.ObservedState != "running" || got.Readiness != ReadinessReady || got.ReadinessSource != "healthcheck_http" || got.RollupStatus != RollupHealthy {
		t.Fatalf("hc-ready-svc = %+v", got)
	}
	// 4. Explicit health_check.url returning 503 while PID is alive -> readiness="unready", rollup="degraded".
	if got := byID["hc-unready-svc"]; got.ObservedState != "running" || got.Readiness != ReadinessUnready || got.ReadinessSource != "healthcheck_http" || got.RollupStatus != RollupDegraded {
		t.Fatalf("hc-unready-svc = %+v", got)
	}

	// 5. Verify both /portfolio/health and /v1/portfolio/health routes and query filtering.
	handler := PortfolioHealthHTTP(inProc)
	for _, path := range []string{"/portfolio/health?rollup_status=degraded", "/v1/portfolio/health?rollup_status=degraded"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body = %s", path, rec.Code, rec.Body.String())
		}
		var filteredProj PortfolioHealthProjection
		if err := json.Unmarshal(rec.Body.Bytes(), &filteredProj); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if len(filteredProj.Deployments) != 1 || filteredProj.Deployments[0].ID != "hc-unready-svc" {
			t.Fatalf("GET %s deployments = %+v, want only hc-unready-svc", path, filteredProj.Deployments)
		}
	}
}
