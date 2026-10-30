package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pluginhost "github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-host"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-host/pluginhosttest"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/capability"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
	auth "github.com/hollis-labs/libs/security/auth"
)

func TestMain(m *testing.M) { pluginhosttest.MaybeRunFixture(); os.Exit(m.Run()) }

type fakePlugin struct {
	startErr, healthErr, stopErr error
	starts, stops                int
}

func (f *fakePlugin) Start(context.Context) error  { f.starts++; return f.startErr }
func (f *fakePlugin) Stop(context.Context) error   { f.stops++; return f.stopErr }
func (f *fakePlugin) Health(context.Context) error { return f.healthErr }
func (f *fakePlugin) APIRoutes() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
}

func readyStatus(t *testing.T, inv *inventory, code, healthy int) {
	t.Helper()
	w := httptest.NewRecorder()
	inv.Readiness(w, httptest.NewRequest("GET", "/api/readyz", nil))
	var body struct {
		Status                string
		Plugins, Healthy, PID int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != code || body.Healthy != healthy || body.Plugins != len(inv.entries) || body.PID != os.Getpid() {
		t.Fatalf("readiness: %d %s", w.Code, w.Body.String())
	}
	if (code == 503 && body.Status != "degraded") || (code == 200 && body.Status != "ok") {
		t.Fatalf("wrong status: %s", body.Status)
	}
}

func TestOptionalFailureIsolation(t *testing.T) {
	for _, phase := range []string{"init", "initial-health", "runtime-health"} {
		t.Run(phase, func(t *testing.T) {
			good, bad := &fakePlugin{}, &fakePlugin{}
			failure := errors.New("synthetic-private-config-value")
			if phase == "init" {
				bad.startErr = failure
			}
			if phase == "initial-health" {
				bad.healthErr = failure
			}
			inv := &inventory{entries: []*pluginEntry{{id: "good", runtime: good}, {id: "bad", optional: true, runtime: bad}}}
			if err := inv.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer inv.Stop(context.Background())
			if phase == "runtime-health" {
				readyStatus(t, inv, 200, 2)
				bad.healthErr = failure
				var wg sync.WaitGroup
				wg.Go(func() {
					for range 50 {
						inv.Readiness(httptest.NewRecorder(), nil)
					}
				})
				if err := inv.Check(context.Background()); err != nil {
					t.Fatal(err)
				}
				wg.Wait()
			}
			readyStatus(t, inv, 503, 1)
			if good.stops != 0 || bad.stops != 1 {
				t.Fatalf("stops good=%d bad=%d", good.stops, bad.stops)
			}
			// No silent retry / new runtime incarnation on a subsequent check.
			if err := inv.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			if bad.starts != 1 || good.starts != 1 {
				t.Fatal("plugin restarted")
			}
			for id, want := range map[string]int{"good": 204, "bad": 503, "absent": 404} {
				w := httptest.NewRecorder()
				inv.APIRoutes().ServeHTTP(w, httptest.NewRequest("GET", "/api/plugins/"+id+"/status", nil))
				if w.Code != want || strings.Contains(w.Body.String(), failure.Error()) {
					t.Fatalf("%s: %d %s", id, w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestRequiredFailureAndFailedCleanupRemainFatal(t *testing.T) {
	for _, runtime := range []bool{false, true} {
		good, bad := &fakePlugin{}, &fakePlugin{}
		inv := &inventory{entries: []*pluginEntry{{id: "good", runtime: good}, {id: "bad", runtime: bad}}}
		if !runtime {
			bad.startErr = errors.New("private-value")
		}
		err := inv.Start(context.Background())
		if runtime {
			if err != nil {
				t.Fatal(err)
			}
			bad.healthErr = errors.New("private-value")
			err = inv.Check(context.Background())
			_ = inv.Stop(context.Background())
		}
		if err == nil || strings.Contains(err.Error(), "private-value") || good.stops == 0 {
			t.Fatalf("failure/rollback: %v", err)
		}
	}
	bad := &fakePlugin{startErr: errors.New("failed"), stopErr: errors.New("unreaped")}
	inv := &inventory{entries: []*pluginEntry{{id: "optional", optional: true, runtime: bad}}}
	if err := inv.Start(context.Background()); err == nil {
		t.Fatal("accepted failed cleanup")
	}
}

func TestUnavailableOptionalPluginStillRequiresItsScope(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{CredentialsFile: filepath.Join(dir, "pairings.json"), Plugins: []Plugin{{ID: "good"}, {ID: "bad"}}}
	inv := &inventory{entries: []*pluginEntry{{id: "bad", optional: true, runtime: &fakePlugin{}}}}
	gate, err := auth.New(auth.Config{Mode: auth.ModeEnforce, Verifier: FileVerifier{Path: cfg.CredentialsFile}})
	if err != nil {
		t.Fatal(err)
	}
	h := gate.Middleware(AuthorizePlugins(inv.APIRoutes()))
	for _, id := range []string{"good", "bad"} {
		path := filepath.Join(dir, id+".token")
		if err := Pair(cfg, id, id, path); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path) //nolint:gosec // Test-owned temporary credential.
		if err != nil {
			t.Fatal(err)
		}
		want := 403
		if id == "bad" {
			want = 503
		}
		for _, authorized := range []bool{false, true} {
			r := httptest.NewRequest("POST", "/api/plugins/bad/execute", nil)
			if authorized {
				r.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(b)))
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			expected := 401
			if authorized {
				expected = want
			}
			if w.Code != expected {
				t.Fatalf("%s authorized=%v: %d", id, authorized, w.Code)
			}
		}
	}
}

func TestRealPluginSurvivesOptionalAdmissionFailure(t *testing.T) {
	command, env := pluginhosttest.FixtureCommand(pluginhosttest.BehaviourEcho, t.TempDir())
	spec := pluginhost.Spec{ID: "fixture", Command: command, Env: env, HandshakeTimeout: time.Second, UnloadTimeout: time.Second, ReapTimeout: time.Second,
		Init: subprocess.InitParams{PluginDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), HostInfo: subprocess.HostInfo{Version: "test", Protocol: subprocess.ProtocolVersion},
			CapabilityContract: capability.ContractVersion, Incarnation: capability.RuntimeIdentity{HostInstance: "test", OwnerID: "fixture", OwnerGeneration: 1}, Grants: capability.GrantSet{}}}
	good := newPluginRuntime(spec)
	badSpec := spec
	badSpec.ID = "absent"
	badSpec.Command = filepath.Join(t.TempDir(), "missing")
	inv := &inventory{entries: []*pluginEntry{{id: "fixture", runtime: good}, {id: "absent", optional: true, runtime: newPluginRuntime(badSpec)}}}
	if err := inv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer inv.Stop(context.Background())
	readyStatus(t, inv, 503, 1)
	if err := good.Health(context.Background()); err != nil {
		t.Fatalf("healthy subprocess lost: %v", err)
	}
	if err := inv.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := good.Health(context.Background()); err == nil {
		t.Fatal("subprocess survived shutdown")
	}
}

func TestHTTPHostRemainsAvailableWithFailedOptionalPlugin(t *testing.T) {
	command, env := pluginhosttest.FixtureCommand(pluginhosttest.BehaviourEcho, t.TempDir())
	dir := t.TempDir()
	cfg := Config{StateDir: dir, CredentialsFile: filepath.Join(dir, "pairings.json"), Plugins: []Plugin{
		{ID: "fixture", Command: command, Dir: dir, Env: env},
		{ID: "missing", Optional: true, Command: filepath.Join(dir, "missing"), Dir: dir},
	}}
	// Exercise the actual config decoder so omitting optional remains required.
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "inventory.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = ReadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Plugins[0].Optional || !cfg.Plugins[1].Optional {
		t.Fatal("optional declaration lost")
	}
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWithListener(ctx, cfg, "test", ln, auth.ModeEnforce) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("shutdown did not finish")
		}
	}()
	client := &http.Client{Timeout: time.Second}
	origin := "http://" + ln.Addr().String()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, requestErr := client.Get(origin + "/api/readyz")
		if requestErr == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != 503 {
				t.Fatalf("degraded host readiness=%d", resp.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(requestErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for path, want := range map[string]int{"/healthz": 200, "/api/plugins/missing/execute": 401} {
		resp, err := client.Get(origin + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s=%d", path, resp.StatusCode)
		}
	}
}
