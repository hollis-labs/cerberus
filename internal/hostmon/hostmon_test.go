package hostmon

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
	"gopkg.in/yaml.v3"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/storage"
	"github.com/hollis-labs/cerberus/internal/testhome"
	"github.com/hollis-labs/cerberus/pkg/outpost"
)

func TestMain(m *testing.M) { testhome.Main(m) }

const goodProbeOutput = "os=Linux\narch=x86_64\nkernel=6.1.0-test\ncpu=4\nmem_total_kb=8000000\nmem_available_kb=4000000\n" +
	"load=0.10 0.20 0.30\nuptime_seconds=60\ndisk_kb=1000000 500000\ntool_docker=1\ntool_tmux=0\ntool_tailscale=1\ntool_caddy=0\n"

// sqliteStore opens and migrates a SQLite file the way the database module does.
func sqliteStore(t *testing.T) *hosts.SampleStore {
	t.Helper()
	ctx := context.Background()
	cfg := store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: filepath.Join(t.TempDir(), "cerberus.db"), CreateParentDir: true}}
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fsys, root := storage.Migrations()
	if err := migrate.Ensure(ctx, cfg, st, migrate.Source{FS: fsys, Root: root}); err != nil {
		t.Fatal(err)
	}
	return hosts.NewSampleStore(st)
}

var errDown = errors.New("connect: connection refused (database unavailable)")

// downStore is a SampleStore whose every call fails while down is set: the Telemetry store's
// Postgres is unreachable.
type downStore struct {
	*hosts.SampleStore
	down   atomic.Bool
	writes atomic.Int32
}

func (d *downStore) fail() error {
	if d.down.Load() {
		return errDown
	}
	return nil
}

func (d *downStore) AppendSample(ctx context.Context, x hosts.Sample) error {
	if err := d.fail(); err != nil {
		return err
	}
	d.writes.Add(1)
	return d.SampleStore.AppendSample(ctx, x)
}

func (d *downStore) PutStatus(ctx context.Context, st hosts.HostStatus) error {
	if err := d.fail(); err != nil {
		return err
	}
	d.writes.Add(1)
	return d.SampleStore.PutStatus(ctx, st)
}

func (d *downStore) Status(ctx context.Context, host string) (hosts.HostStatus, bool, error) {
	if err := d.fail(); err != nil {
		return hosts.HostStatus{}, false, err
	}
	return d.SampleStore.Status(ctx, host)
}

func (d *downStore) Recent(ctx context.Context, host string, n int) ([]hosts.Sample, error) {
	if err := d.fail(); err != nil {
		return nil, err
	}
	return d.SampleStore.Recent(ctx, host, n)
}

func (d *downStore) AppendEvent(ctx context.Context, e hosts.Event) (int64, error) {
	if err := d.fail(); err != nil {
		return 0, err
	}
	d.writes.Add(1)
	return d.SampleStore.AppendEvent(ctx, e)
}

func (d *downStore) ForgetStatus(ctx context.Context, keep map[string]bool) error {
	if err := d.fail(); err != nil {
		return err
	}
	return d.SampleStore.ForgetStatus(ctx, keep)
}

// outpostHosts writes hosts.yaml with n outpost hosts served by one fake executor, and
// returns its path and the executor's call count.
func outpostHosts(t *testing.T, n int) (string, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("the token file was not sent")
		}
		_ = json.NewEncoder(w).Encode(outpost.Result{Output: goodProbeOutput})
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	token := filepath.Join(dir, "outpost-tokens", "box")
	if err := os.MkdirAll(filepath.Dir(token), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("fixture-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := hosts.File{Version: 1}
	for i := range n {
		f.Hosts = append(f.Hosts, hosts.Host{Name: "box-" + string(rune('a'+i)),
			Reach: hosts.Reach{Transport: "outpost", Outpost: &hosts.OutpostReach{URL: server.URL, TokenFile: token}}})
	}
	f.Hosts = append(f.Hosts, hosts.Host{Name: "ssh-box", Reach: hosts.Reach{SSH: "box-ssh"}})
	b, err := yaml.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "hosts.yaml")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, &calls
}

func TestTelemetryBoundsDropsOldestAndRecovers(t *testing.T) {
	ctx := context.Background()
	tel := NewTelemetry(3, nil)
	down := true
	var mu sync.Mutex
	var done []string
	write := func(name string) {
		tel.Write(ctx, "sample", name, func(context.Context) error {
			if down {
				return errDown
			}
			mu.Lock()
			done = append(done, name)
			mu.Unlock()
			return nil
		})
	}
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		write(n)
	}
	st := tel.State()
	if st.Held != 3 || st.Dropped != 2 {
		t.Fatalf("held %d dropped %d, want 3 and 2", st.Held, st.Dropped)
	}
	err := tel.Check(ctx)
	if err == nil || !strings.Contains(err.Error(), "3 host-monitor writes held") || !strings.Contains(err.Error(), "2 dropped") {
		t.Fatalf("check: %v", err)
	}
	down = false
	tel.Flush(ctx)
	if strings.Join(done, ",") != "3,4,5" {
		t.Fatalf("drained %v, want the newest three in order", done)
	}
	if tel.Check(ctx) != nil || tel.State().Dropped != 2 {
		t.Fatalf("after recovery: %v %+v", tel.Check(ctx), tel.State())
	}
	// A late sample is not held: retrying cannot succeed.
	tel.Write(ctx, "sample", "x", func(context.Context) error { return hosts.ErrLateSample })
	if tel.State().Held != 0 {
		t.Fatal("a late sample was held")
	}
}

// With the Telemetry store down, probes go on, status still advances from memory, the held
// writes stay within the bound with the oldest dropped, and the check reports the counts.
// When the store comes back the held writes land.
func TestMonitorWithTelemetryStoreDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostsPath, calls := outpostHosts(t, 3)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0).UTC() }
	prober, err := NewProber(ProbeOptions{HostsPath: hostsPath, StatePath: filepath.Join(t.TempDir(), "state", "hosts.json"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	ds := &downStore{SampleStore: sqliteStore(t)}
	ds.down.Store(true)
	settings := hosts.DefaultMonitorSettings()
	settings.Enabled = true
	tel := NewTelemetry(4, nil)
	var events []string
	var evMu sync.Mutex
	m := New(Options{Prober: prober, Store: ds, Telemetry: tel, Settings: settings, Now: now, Rand: func() float64 { return 0 },
		Record: func(_ context.Context, what, _ string) { evMu.Lock(); events = append(events, what); evMu.Unlock() }})

	// Drive the schedule by hand: the first tick stages each host, later ticks probe the due ones.
	m.tick(ctx)
	for range 3 {
		clock.Add(int64(settings.OutpostInterval / time.Second))
		m.tick(ctx)
		m.wg.Wait()
	}
	if calls.Load() < 6 {
		t.Fatalf("probes ran %d times; supervision must go on while the store is down", calls.Load())
	}
	st := tel.State()
	if st.Held != 4 || st.Dropped == 0 {
		t.Fatalf("held %d dropped %d: want the bound (4) and the oldest dropped", st.Held, st.Dropped)
	}
	if err := tel.Check(ctx); err == nil || !strings.Contains(err.Error(), "dropped") {
		t.Fatalf("check while down: %v", err)
	}
	m.mu.Lock()
	last := m.last["box-a"]
	m.mu.Unlock()
	if last.LastOK == nil || last.ConsecutiveFailures != 0 || last.Status == hosts.StatusUnknown {
		t.Fatalf("status from memory: %+v", last)
	}
	if ds.writes.Load() != 0 {
		t.Fatal("a write reached the store while it was down")
	}

	ds.down.Store(false)
	tel.Flush(ctx)
	if tel.State().Held != 0 || tel.Check(ctx) != nil || ds.writes.Load() != 4 {
		t.Fatalf("after recovery: %+v %v, %d writes landed", tel.State(), tel.Check(ctx), ds.writes.Load())
	}
	if _, ok, err := ds.Status(ctx, "box-c"); err != nil || !ok {
		t.Fatalf("held status did not land: %v %v", ok, err)
	}
}

// An ssh host is not probed by a build without the ssh connector, and is never marked offline
// for it.
func TestMonitorSkipsSSHWithoutConnector(t *testing.T) {
	ctx := context.Background()
	hostsPath, _ := outpostHosts(t, 1)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0).UTC() }
	prober, err := NewProber(ProbeOptions{HostsPath: hostsPath, StatePath: filepath.Join(t.TempDir(), "hosts.json"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	settings := hosts.DefaultMonitorSettings()
	settings.Enabled = true
	ss := sqliteStore(t)
	m := New(Options{Prober: prober, Store: ss, Settings: settings, Now: now, Rand: func() float64 { return 0 }})
	m.tick(ctx)
	clock.Add(int64(settings.SSHInterval / time.Second))
	m.tick(ctx)
	m.wg.Wait()
	if _, ok, _ := ss.Status(ctx, "ssh-box"); ok {
		t.Fatal("the ssh host got a status without being probed")
	}
	if _, ok, _ := ss.Status(ctx, "box-a"); !ok {
		t.Fatal("the outpost host was not probed")
	}
}
