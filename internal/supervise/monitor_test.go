package supervise

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/service"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/storage"
)

// monitorWorld is a supervise-mode lane for the monitor's own tests (ported from main's
// internal/cerbapi/resource_monitor_test.go), with a store, a key and brakes it controls.
type monitorWorld struct {
	cfg  *config.ConfigV2
	st   *store.Store
	key  audit.Digester
	gate *gatetest.World
	svc  *Service
}

func newMonitorWorld(t *testing.T) *monitorWorld {
	t.Helper()
	base := t.TempDir()
	t.Setenv("HOME", filepath.Join(base, "home"))
	service.SetStateRoot(filepath.Join(base, "data"))
	t.Cleanup(func() { service.SetStateRoot("") })
	ctx := context.Background()
	scfg := store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: filepath.Join(base, "data", "cerberus.db"), CreateParentDir: true}}
	st, err := store.Open(ctx, scfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fsys, root := storage.Migrations()
	if err = migrate.Ensure(ctx, scfg, st, migrate.Source{FS: fsys, Root: root}); err != nil {
		t.Fatal(err)
	}
	w := &monitorWorld{cfg: &config.ConfigV2{}, st: st}
	if w.key, err = audit.OpenDigester(filepath.Join(base, "data", "audit")); err != nil {
		t.Fatal(err)
	}
	w.gate = gatetest.New(t, gatetest.Options{})
	w.svc, err = New(Options{
		Mode: settings.ModeSupervise, Home: filepath.Join(base, "home"), StateDir: filepath.Join(base, "data"),
		Config: func() (*config.ConfigV2, error) { return w.cfg, nil }, Store: st, Digester: w.key,
		Gate:   w.gate.Gate,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *monitorWorld) monitor(cfg MonitorConfig) *monitor {
	return &monitor{s: w.svc, cfg: cfg, done: make(chan struct{}),
		failureCount: map[string]int{}, lastRestart: map[string]time.Time{}, lastError: map[string]string{}}
}

// appender is an auto_restart session that appends a line to file each time it runs and exits.
func appender(id, file string) config.ResourceDef {
	return config.ResourceDef{ID: id, Type: "process", Connector: "local",
		Config: map[string]any{"auto_restart": true, "command": []any{"/bin/sh", "-c", "echo run >> " + file}}}
}

func runs(file string) int {
	b, _ := os.ReadFile(file) //nolint:gosec // the test's own file
	return strings.Count(string(b), "run")
}

func waitRuns(t *testing.T, file string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for runs(file) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d runs, want %d", runs(file), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Let the run exit, so the next check sees it down.
	time.Sleep(100 * time.Millisecond)
}

func TestResourceMonitorRemovedResourceGetsFreshRetryBudget(t *testing.T) {
	w := newMonitorWorld(t)
	m := w.monitor(DefaultMonitorConfig())
	m.failureCount["returning"] = 3
	m.lastRestart["returning"] = time.Now()
	m.lastError["returning"] = "old failure"
	m.checkAll(context.Background())
	marker := filepath.Join(t.TempDir(), "runs")
	w.cfg.Resources = []config.ResourceDef{appender("returning", marker)}
	m.checkAll(context.Background())
	waitRuns(t, marker, 1)
	if m.lastError["returning"] != "" {
		t.Fatalf("old error survived a successful restart: %v", m.lastError)
	}
}

func TestResourceMonitorChecksAtBootBeforeFirstTick(t *testing.T) {
	w := newMonitorWorld(t)
	marker := filepath.Join(t.TempDir(), "runs")
	w.cfg.Resources = []config.ResourceDef{appender("boot", marker)}
	w.svc.StartMonitor(context.Background(), MonitorConfig{CheckInterval: time.Hour, DefaultMaxRestartAttempts: 3})
	t.Cleanup(w.svc.Shutdown)
	waitRuns(t, marker, 1)
}

// The monitor restarts only the definition last applied: edited since, the workload stays down
// and the monitor says to apply it; applied again, it restarts.
func TestTheMonitorRestartsOnlyTheAppliedDefinition(t *testing.T) {
	w := newMonitorWorld(t)
	dir := t.TempDir()
	applied, edited := filepath.Join(dir, "applied"), filepath.Join(dir, "edited")
	ctx := context.Background()
	if err := storage.SetApplied(ctx, w.st, "svc", w.key.Digest(appender("svc", applied)), time.Now()); err != nil {
		t.Fatal(err)
	}
	w.cfg.Resources = []config.ResourceDef{appender("svc", edited)}
	m := w.monitor(DefaultMonitorConfig())
	m.checkAll(ctx)
	time.Sleep(200 * time.Millisecond)
	if runs(edited) != 0 {
		t.Fatal("the monitor ran a definition nobody applied")
	}
	if !strings.Contains(m.lastError["svc"], "cerberus resource apply svc") {
		t.Fatalf("last error %q", m.lastError["svc"])
	}
	if err := storage.SetApplied(ctx, w.st, "svc", w.key.Digest(appender("svc", edited)), time.Now()); err != nil {
		t.Fatal(err)
	}
	m.checkAll(ctx)
	waitRuns(t, edited, 1)
}

// The restart budget stops the restarts at max_restart_attempts; the cooldown spaces them.
func TestResourceMonitorRestartBudgetAndCooldown(t *testing.T) {
	w := newMonitorWorld(t)
	ctx := context.Background()
	budget := filepath.Join(t.TempDir(), "budget")
	w.cfg.Resources = []config.ResourceDef{appender("flaky", budget)}
	m := w.monitor(MonitorConfig{CheckInterval: time.Hour, DefaultMaxRestartAttempts: 2, DefaultRestartCooldown: time.Nanosecond})
	for i := 1; i <= 4; i++ {
		m.checkAll(ctx)
		if i <= 2 {
			waitRuns(t, budget, i)
		} else {
			time.Sleep(150 * time.Millisecond)
		}
	}
	if n := runs(budget); n != 2 {
		t.Fatalf("%d restarts, want the budget of 2", n)
	}

	cool := filepath.Join(t.TempDir(), "cool")
	w.cfg.Resources = []config.ResourceDef{appender("cool", cool)}
	m = w.monitor(MonitorConfig{CheckInterval: time.Hour, DefaultMaxRestartAttempts: 5, DefaultRestartCooldown: time.Hour})
	m.checkAll(ctx)
	waitRuns(t, cool, 1)
	m.checkAll(ctx)
	time.Sleep(150 * time.Millisecond)
	if n := runs(cool); n != 1 {
		t.Fatalf("%d restarts inside the cooldown, want 1", n)
	}
}

// A paused resource (class A store) and a frozen one are not restarted; a lockdown does not
// stop restarts.
func TestResourceMonitorSkipsPausedAndFrozen(t *testing.T) {
	w := newMonitorWorld(t)
	ctx := context.Background()
	dir := t.TempDir()
	paused, frozen, locked := filepath.Join(dir, "paused"), filepath.Join(dir, "frozen"), filepath.Join(dir, "locked")
	w.cfg.Resources = []config.ResourceDef{appender("paused", paused), appender("frozen", frozen), appender("locked", locked)}
	if err := storage.SetPaused(ctx, w.st, "paused", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	w.gate.Engage(t, brake.State{
		Lockdown: &brake.Lockdown{ID: "lk"},
		Freezes:  []brake.Freeze{{ID: "fz", Match: policy.TargetMatch{ID: "frozen"}}},
	})
	m := w.monitor(DefaultMonitorConfig())
	m.checkAll(ctx)
	waitRuns(t, locked, 1)
	if runs(paused) != 0 || runs(frozen) != 0 {
		t.Fatalf("restarted while paused (%d) or frozen (%d)", runs(paused), runs(frozen))
	}
	// The restart is recorded as the old build recorded it: automation, never gated, with what
	// the monitor saw as the reason, and an outcome.
	intents, outcomes := w.gate.Records(audit.KindIntent), w.gate.Records(audit.KindOutcome)
	if len(intents) != 1 || len(outcomes) != 1 {
		t.Fatalf("want one restart recorded, got %d intents and %d outcomes", len(intents), len(outcomes))
	}
	in := intents[0]
	if in.Principal.Kind != audit.PrincipalAutomation || in.Principal.Via != "monitor" || in.Connector != "local" || in.Operation != "apply" ||
		in.Target.Resource != "locked" || !strings.HasPrefix(in.Reason, "auto_restart: the workload was ") || outcomes[0].OperationID != in.OperationID {
		t.Fatalf("restart records: %+v / %+v", in, outcomes[0])
	}
}
