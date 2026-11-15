package supervise_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
	"gopkg.in/yaml.v3"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/service"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/storage"
	"github.com/hollis-labs/cerberus/internal/supervise"
	"github.com/hollis-labs/cerberus/internal/supervise/approvertest"
)

// systemctlFake is a user manager: `show` answers from a table, `restart` and `start` mark a
// unit running, `stop` marks it dead, and every command is recorded.
type systemctlFake struct {
	mu    sync.Mutex
	units map[string]string // unit -> "active/running"
	calls []string
}

func (f *systemctlFake) CombinedOutput(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, filepath.Base(name)+" "+strings.Join(args, " "))
	if filepath.Base(name) != "systemctl" || len(args) < 2 || args[0] != "--user" {
		return nil, nil
	}
	switch args[1] {
	case "show":
		state, ok := f.units[args[2]]
		if !ok {
			return []byte("Id=" + args[2] + "\nLoadState=not-found\nActiveState=inactive\nSubState=dead\nUnitFileState=\nNeedDaemonReload=no\n"), nil
		}
		active, sub, _ := strings.Cut(state, "/")
		return []byte(fmt.Sprintf("Id=%s\nLoadState=loaded\nActiveState=%s\nSubState=%s\nUnitFileState=enabled\nNeedDaemonReload=no\nMainPID=4242\nNRestarts=0\n", args[2], active, sub)), nil
	case "restart", "start":
		f.units[args[2]] = "active/running"
	case "stop":
		f.units[args[2]] = "inactive/dead"
	}
	return nil, nil
}

// mutating are the recorded commands that change a unit.
func (f *systemctlFake) mutating() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		for _, verb := range []string{" restart ", " start ", " stop ", " enable ", " disable ", " daemon-reload", " kill "} {
			if strings.Contains(c+" ", verb) {
				out = append(out, c)
			}
		}
	}
	return out
}

type lane struct {
	home, data string
	cfg        *config.ConfigV2
	st         *store.Store
	manager    *systemctlFake
	digester   audit.Digester
	brakes     brake.State
	svc        *supervise.Service
}

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	ctx := context.Background()
	cfg := store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: path, CreateParentDir: true}}
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fsys, root := storage.Migrations()
	if err := migrate.Ensure(ctx, cfg, st, migrate.Source{FS: fsys, Root: root}); err != nil {
		t.Fatal(err)
	}
	return st
}

// newLane is a supervise-mode lane over a scratch home and data dir, a fake user manager and
// a migrated class A store. Dev-session pid and lock files go to the data dir.
func newLane(t *testing.T, resources ...config.ResourceDef) *lane {
	t.Helper()
	base := t.TempDir()
	l := &lane{home: filepath.Join(base, "home"), data: filepath.Join(base, "data"), manager: &systemctlFake{units: map[string]string{}}}
	t.Setenv("HOME", l.home)
	for _, d := range []string{l.home, l.data} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	service.SetStateRoot(l.data)
	t.Cleanup(func() { service.SetStateRoot("") })
	l.cfg = &config.ConfigV2{Resources: resources}
	l.st = openStore(t, filepath.Join(l.data, "cerberus.db"))
	var err error
	if l.digester, err = audit.OpenDigester(filepath.Join(l.data, "audit")); err != nil {
		t.Fatal(err)
	}
	l.svc, err = supervise.New(supervise.Options{
		Mode: settings.ModeSupervise, Home: l.home, StateDir: l.data, Runner: l.manager, SelfID: "cerberus-substrate",
		Config:   func() (*config.ConfigV2, error) { return l.cfg, nil },
		Store:    l.st,
		Digester: l.digester,
		Approver: supervise.InterimApprover{Brakes: func() brake.State { return l.brakes }},
		Systemd: localconn.SystemdOptions{
			Runner:   l.manager,
			Env:      localconn.RenderEnv{Home: l.home, Path: l.home + "/.local/bin:/usr/local/bin:/usr/bin:/bin", Cerberus: l.home + "/.local/bin/cerberus"},
			LookPath: func(name string) (string, error) { return name, nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func (l *lane) do(t *testing.T, verb, id string) *supervise.OpResult {
	t.Helper()
	out, err := l.svc.Mutate(context.Background(), supervise.Request{Verb: verb, ID: id, Acknowledged: true})
	if err != nil {
		t.Fatalf("%s %s: %v", verb, id, err)
	}
	return out
}

func devSession(id string) config.ResourceDef {
	return config.ResourceDef{ID: id, Type: "process", Project: "web", Connector: "local",
		Config: map[string]any{"mode": "dev_session", "command": []any{"/bin/sleep", "300"}}}
}

func paused(t *testing.T, l *lane, id string) bool {
	t.Helper()
	p, err := storage.Paused(context.Background(), l.st, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func pidOf(t *testing.T, l *lane, id string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(l.data, "pids", id+".pid")) //nolint:gosec // the test's own tree
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

func alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

// A dev session under supervise mode: apply starts a real process and records its definition
// digest; stop pauses it in the class A store and ends the process; apply resumes; remove
// pauses and destroys. Its pid file is in the data dir, and the old tree's pids/ is untouched.
func TestSuperviseDevSessionLifecycle(t *testing.T) {
	l := newLane(t, devSession("web-dev"))
	oldPids := filepath.Join(l.home, ".cerberus", "pids")

	out := l.do(t, "apply", "web-dev")
	if !out.Success || out.ServiceID != "web-dev" {
		t.Fatalf("apply: %+v", out)
	}
	pid := pidOf(t, l, "web-dev")
	t.Cleanup(func() {
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	if !alive(pid) {
		t.Fatalf("apply started nothing (pid %d)", pid)
	}
	digest, ok, err := storage.Applied(context.Background(), l.st, "web-dev")
	if err != nil || !ok || digest != l.digester.Digest(l.cfg.Resources[0]) {
		t.Fatalf("applied digest = %q %v %v, want the keyed digest of the definition", digest, ok, err)
	}

	if out = l.do(t, "stop", "web-dev"); !out.Success || !strings.Contains(out.Message, "stopped successfully") {
		t.Fatalf("stop: %+v", out)
	}
	if !paused(t, l, "web-dev") {
		t.Fatal("stop did not record the pause")
	}
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(pid) {
		t.Fatal("stop left the process running")
	}

	if out = l.do(t, "apply", "web-dev"); !out.Success {
		t.Fatalf("apply after stop: %+v", out)
	}
	pid = pidOf(t, l, "web-dev")
	if paused(t, l, "web-dev") || !alive(pid) {
		t.Fatalf("apply did not resume (paused %v, pid %d)", paused(t, l, "web-dev"), pid)
	}

	if out = l.do(t, "remove", "web-dev"); !out.Success || !strings.Contains(out.Message, "removed successfully") {
		t.Fatalf("remove: %+v", out)
	}
	if !paused(t, l, "web-dev") {
		t.Fatal("remove did not record the pause")
	}
	if _, err := os.Stat(oldPids); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old tree's pids/ was touched: %v", err)
	}
}

// Before the resource is looked up, a verb without an acknowledgment is refused with the old
// build's 409 acknowledgment_required, an unknown id included.
func TestSuperviseNeedsTheAcknowledgment(t *testing.T) {
	l := newLane(t, devSession("web-dev"))
	for _, verb := range supervise.Verbs {
		for _, id := range []string{"web-dev", "nope"} {
			out, err := l.svc.Mutate(context.Background(), supervise.Request{Verb: verb, ID: id})
			var r *supervise.Refusal
			if out != nil || !errors.As(err, &r) || r.Status != 409 || r.Code != "acknowledgment_required" {
				t.Fatalf("%s %s without ack: %+v %v", verb, id, out, err)
			}
		}
	}
}

// After the gate, a failure is a result, not an error: the old build's 200 {success: false}.
func TestSuperviseFailuresAfterTheGateAreResults(t *testing.T) {
	l := newLane(t, devSession("web-dev"), config.ResourceDef{ID: "box", Type: "server", Connector: "ssh"},
		config.ResourceDef{ID: "a", Type: "process", Connector: "local", Config: map[string]any{"mode": "dev_session", "command": []any{"/bin/true"}, "port": 18080}},
		config.ResourceDef{ID: "b", Type: "process", Connector: "local", Config: map[string]any{"mode": "dev_session", "command": []any{"/bin/true"}, "port": 18080}},
	)
	for _, c := range []struct{ verb, id, want string }{
		{"apply", "nope", `resource "nope" not found`},
		{"remove", "nope", `resource "nope" not found`},
		{"reload", "box", `resource "box" is server/ssh; operation currently supports local process resources only`},
		{"apply", "a", "port 18080"},
		{"deploy", "b", "port 18080"},
	} {
		out := l.do(t, c.verb, c.id)
		if out.Success || out.ServiceID != c.id || !strings.Contains(out.Error, c.want) {
			t.Errorf("%s %s = %+v, want success false naming %q", c.verb, c.id, out, c.want)
		}
	}
}

// The self-guard: every verb on the resource this process runs as is refused as a result, and
// nothing reaches the user manager.
func TestSuperviseRefusesEveryVerbOnItself(t *testing.T) {
	self := config.ResourceDef{ID: "cerberus-substrate", Type: "process", Project: "cerberus", Connector: "local",
		Config: map[string]any{"mode": "os_service", "supervisor": "systemd_user", "command": []any{"/u/op/.local/bin/cerberus", "serve"}}}
	l := newLane(t, self)
	for _, verb := range supervise.Verbs {
		out := l.do(t, verb, "cerberus-substrate")
		if out.Success || !strings.Contains(out.Error, "the resource this Cerberus runs as") || !strings.Contains(out.Error, "fleet-install") {
			t.Errorf("%s on itself = %+v", verb, out)
		}
	}
	if m := l.manager.mutating(); len(m) != 0 {
		t.Fatalf("a refused verb reached the user manager: %v", m)
	}
}

// The interim approver against the old build's routing table.
func TestInterimApproverRefusesWhatOldRoutesAway(t *testing.T) {
	approvertest.RunInterim(t, func(st brake.State) supervise.Approver {
		return supervise.InterimApprover{Brakes: func() brake.State { return st }}
	})
}

// Through the lane, a routed request is refused before anything runs.
func TestSuperviseRefusesRoutedRequestsBeforeRunning(t *testing.T) {
	l := newLane(t, devSession("web-dev"))
	l.brakes = brake.State{Lockdown: &brake.Lockdown{ID: "lk"}}
	_, err := l.svc.Mutate(context.Background(), supervise.Request{Verb: "apply", ID: "web-dev", Acknowledged: true})
	var r *supervise.Refusal
	if !errors.As(err, &r) || r.Code != supervise.ErrApprovalsUnavailable || r.Status != 423 {
		t.Fatalf("under a lockdown: %v", err)
	}
	if pidOf(t, l, "web-dev") != 0 {
		t.Fatal("a refused apply started the session")
	}
}

// parityCase is a parity fixture with the fixed home /u/op moved to the lane's home.
func parityCase(t *testing.T, dir, home string) (config.ResourceDef, []byte, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "resource.yaml")) //nolint:gosec // test fixtures
	if err != nil {
		t.Fatal(err)
	}
	var r config.ResourceDef
	if err = yaml.Unmarshal([]byte(strings.ReplaceAll(string(raw), "/u/op", home)), &r); err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(filepath.Join(dir, "unit.golden")) //nolint:gosec // test fixtures
	if err != nil {
		t.Fatal(err)
	}
	golden = bytes.ReplaceAll(golden, []byte("/u/op"), []byte(home))
	first, _, _ := strings.Cut(string(golden), "\n")
	return r, golden, strings.TrimPrefix(first, "# unit: ")
}

// Renderer parity through a real apply: with the old build's unit on disk and the unit
// running, apply leaves the file byte-identical and restarts nothing. An artifact-mode
// resource installs its artifact on the first apply; the second is the unchanged one.
func TestSuperviseApplyOnAnUnchangedUnitRestartsNothing(t *testing.T) {
	cases, err := filepath.Glob(filepath.Join("..", "connector", "local", "testdata", "parity", "*", "resource.yaml"))
	if err != nil || len(cases) == 0 {
		t.Fatalf("no parity fixtures: %v", err)
	}
	for _, p := range cases {
		dir := filepath.Dir(p)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			l := newLane(t)
			res, golden, unitName := parityCase(t, dir, l.home)
			l.cfg.Resources = []config.ResourceDef{res}
			spec, err := localconn.SpecFromResourceConfig(res.Config)
			if err != nil {
				t.Fatal(err)
			}
			unitPath := filepath.Join(l.home, ".config", "systemd", "user", unitName)
			if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(unitPath, golden[bytes.IndexByte(golden, '\n')+1:], 0o600); err != nil {
				t.Fatal(err)
			}
			l.manager.units[unitName] = "active/running"
			if spec.RunFrom == localconn.ProcessRunFromArtifact {
				src, err := localconn.ResolveArtifactSourcePath(spec)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(src), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(src, []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // a test artifact
					t.Fatal(err)
				}
				if out := l.do(t, "apply", res.ID); !out.Success {
					t.Fatalf("first apply: %+v", out)
				}
				l.manager.calls = nil
			}
			before, _ := os.ReadFile(unitPath) //nolint:gosec // the test's own tree
			out := l.do(t, "apply", res.ID)
			if !out.Success {
				t.Fatalf("apply: %+v", out)
			}
			after, _ := os.ReadFile(unitPath) //nolint:gosec // the test's own tree
			if !bytes.Equal(before, after) {
				t.Fatalf("apply rewrote the unit:\n--- before\n%s\n--- after\n%s", before, after)
			}
			if m := l.manager.mutating(); len(m) != 0 {
				t.Fatalf("apply on an unchanged unit ran %v", m)
			}
		})
	}
}
