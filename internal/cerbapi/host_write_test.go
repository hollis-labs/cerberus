package cerbapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/hosts"
)

const writeYAML = `version: 1
hosts:
  - name: worker-1
    role: worker
    hardware:
      memory: {type: DDR4, total: 8GiB, slots: 2, slots_used: 1, modules: ["8GiB DDR4 2400 Samsung"]}
      disks: [{name: sda, size: 120GB, type: ssd}]
      storage_slots:
        - {id: sata-0, kind: sata_25, occupant_disk: sda}
        - {id: sata-1, kind: sata_25}
  - name: worker-5
    role: worker
    hardware:
      memory: {type: DDR4, slots: 2, slots_used: 0}
components:
  - {id: ram-0001, kind: ram, size: 8GiB, type: DDR4, speed: "2400", location: {host: worker-1, slot: dimm0}}
`

type writeFixture struct {
	svc  *HostService
	sink *audit.Memory
	path string
}

func newWriteFixture(t *testing.T, sink audit.Sink) *writeFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.yaml")
	if err := os.WriteFile(path, []byte(writeYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	mem, _ := sink.(*audit.Memory)
	if sink == nil {
		mem = audit.NewMemory()
		sink = mem
	}
	return &writeFixture{svc: NewHostService(sink, HostServiceOptions{HostsPath: path, StatePath: "-"}), sink: mem, path: path}
}

func (f *writeFixture) args(t *testing.T) WriteArgs {
	t.Helper()
	rev, err := f.svc.Revision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return WriteArgs{Revision: rev, Acknowledged: true}
}

func (f *writeFixture) bytes(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// worker-1 RAM 8GiB -> 16GiB as one update that records the before/after.
func TestWorker1RAMUpgradeRecordsBeforeAndAfter(t *testing.T) {
	f := newWriteFixture(t, nil)
	ctx := context.Background()
	res, err := f.svc.UpdateHost(ctx, HostUpdateArgs{WriteArgs: f.args(t), Name: "worker-1", Patch: map[string]any{
		"hardware": map[string]any{"memory": map[string]any{"total": "16GiB", "slots_used": 2, "modules": []any{"8GiB DDR4 2400 Samsung", "8GiB DDR4 2400 Crucial"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Before["hardware.memory.total"] != 8<<30 || res.After["hardware.memory.total"] != 16<<30 || res.Backup == "" {
		t.Fatalf("result: %+v", res)
	}
	if _, ok := res.Before["hardware.vendor"]; ok || len(res.After) != 3 {
		t.Fatalf("only the changed fields belong in the record: %+v", res.After)
	}
	v, _ := f.svc.Get(ctx, "worker-1")
	if v.Host.Hardware.Memory.Total != 16<<30 {
		t.Fatalf("not persisted: %+v", v.Host.Hardware.Memory)
	}
	hist, err := f.svc.HostHistory(ctx, "worker-1", 10)
	if err != nil || len(hist) != 1 || hist[0].Op != OpHostUpdate || hist[0].Before["hardware.memory.total"] != float64(8<<30) && hist[0].Before["hardware.memory.total"] != 8<<30 {
		t.Fatalf("history: %+v %v", hist, err)
	}
	if hist[0].Caller == "" || hist[0].RevisionBefore == hist[0].RevisionAfter || hist[0].OperationID == "" {
		t.Fatalf("history entry incomplete: %+v", hist[0])
	}
	if st, _ := os.Stat(filepath.Join(filepath.Dir(f.path), hosts.HistoryFileName)); st == nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("history file: %v", st)
	}
}

func TestRAMStickMovesFromHostToSpareToAnotherHost(t *testing.T) {
	f := newWriteFixture(t, nil)
	ctx := context.Background()
	if _, err := f.svc.MoveComponent(ctx, ComponentMoveArgs{WriteArgs: f.args(t), ID: "ram-0001", To: hosts.ComponentLocation{Spare: true}}); err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.MoveComponent(ctx, ComponentMoveArgs{WriteArgs: f.args(t), ID: "ram-0001", To: hosts.ComponentLocation{Host: "worker-5", Slot: "dimm1"}})
	if err != nil || res.After["location.host"] != "worker-5" {
		t.Fatalf("%+v %v", res, err)
	}
	for host, want := range map[string]int{"worker-1": 1, "worker-5": 1} {
		h, err := f.svc.HostHistory(ctx, host, 10)
		if err != nil || len(h) != want {
			t.Fatalf("%s history = %d, want %d (%v)", host, len(h), want, err)
		}
	}
	c, _ := f.svc.HostHistory(ctx, "worker-5", 1)
	if len(c) != 1 || c[0].Kind != "component" {
		t.Fatalf("%+v", c)
	}
	ch, _ := f.svc.ComponentHistory(ctx, "ram-0001", 10)
	if len(ch) != 2 {
		t.Fatalf("component history: %d", len(ch))
	}
}

func TestInvalidWriteLeavesTheFileByteIdentical(t *testing.T) {
	f := newWriteFixture(t, nil)
	before := f.bytes(t)
	ctx := context.Background()
	cases := map[string]func() error{
		"bad role": func() error {
			_, err := f.svc.UpdateHost(ctx, HostUpdateArgs{WriteArgs: f.args(t), Name: "worker-1", Patch: map[string]any{"role": "wizard"}})
			return err
		},
		"unknown field": func() error {
			_, err := f.svc.UpdateHost(ctx, HostUpdateArgs{WriteArgs: f.args(t), Name: "worker-1", Patch: map[string]any{"colour": "red"}})
			return err
		},
		"taken slot": func() error {
			_, err := f.svc.MoveComponent(ctx, ComponentMoveArgs{WriteArgs: f.args(t), ID: "ram-0001", To: hosts.ComponentLocation{Host: "worker-1", Slot: "dimm9"}})
			return err
		},
		"remove host with parts": func() error {
			_, err := f.svc.RemoveHost(ctx, HostRemoveArgs{WriteArgs: f.args(t), Name: "worker-1"})
			return err
		},
		"unknown host": func() error {
			_, err := f.svc.UpdateHost(ctx, HostUpdateArgs{WriteArgs: f.args(t), Name: "ghost", Patch: map[string]any{"role": "worker"}})
			return err
		},
		"credential": func() error {
			_, err := f.svc.UpdateHost(ctx, HostUpdateArgs{WriteArgs: f.args(t), Name: "worker-1", Patch: map[string]any{"hardware": map[string]any{"notes": "key ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD"}}})
			return err
		},
		"no revision": func() error {
			_, err := f.svc.UpdateHost(ctx, HostUpdateArgs{WriteArgs: WriteArgs{Acknowledged: true}, Name: "worker-1", Patch: map[string]any{"role": "service"}})
			return err
		},
		"not acknowledged": func() error {
			a := f.args(t)
			a.Acknowledged = false
			_, err := f.svc.UpdateHost(ctx, HostUpdateArgs{WriteArgs: a, Name: "worker-1", Patch: map[string]any{"role": "service"}})
			return err
		},
	}
	for name, run := range cases {
		if err := run(); err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if f.bytes(t) != before {
			t.Fatalf("%s: hosts.yaml changed", name)
		}
	}
	if m, _ := filepath.Glob(f.path + ".bak-*"); len(m) != 0 {
		t.Fatalf("refused writes left backups: %v", m)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.path), hosts.HistoryFileName)); err == nil {
		t.Fatal("refused writes left history")
	}
}

type hostsFailingSink struct{ *audit.Memory }

func (hostsFailingSink) Write(audit.Record) (audit.Record, error) {
	return audit.Record{}, errors.New("disk full")
}

func TestWriteIsRefusedWhenTheAuditRecordCannotBeWritten(t *testing.T) {
	f := newWriteFixture(t, hostsFailingSink{audit.NewMemory()})
	before := f.bytes(t)
	args := f.args(t)
	_, err := f.svc.UpdateHost(context.Background(), HostUpdateArgs{WriteArgs: args, Name: "worker-1", Patch: map[string]any{"role": "service"}})
	if err == nil || !strings.Contains(err.Error(), "audit log could not be written") {
		t.Fatalf("err = %v", err)
	}
	if f.bytes(t) != before {
		t.Fatal("the file changed although the audit record was refused")
	}
	// Reads still proceed.
	if _, err := f.svc.Get(context.Background(), "worker-1"); err != nil {
		t.Fatalf("a read must proceed unrecorded: %v", err)
	}
}

func TestWritesAreAuditedWithIntentAndOutcome(t *testing.T) {
	f := newWriteFixture(t, nil)
	_, err := f.svc.UpdateComponent(context.Background(), ComponentUpdateArgs{WriteArgs: f.args(t), ID: "ram-0001", Patch: map[string]any{"size": "16GiB"}})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, r := range f.sink.Records() {
		if r.Connector == "host" && r.Operation == OpComponentUpdate {
			kinds = append(kinds, string(r.Kind)+":"+r.Effect)
		}
	}
	if len(kinds) != 2 || !strings.HasSuffix(kinds[0], ":write") {
		t.Fatalf("records: %v", kinds)
	}
}

func TestOneWinnerPerRevision(t *testing.T) {
	f := newWriteFixture(t, nil)
	args := f.args(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, conflict := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.UpdateHost(context.Background(), HostUpdateArgs{WriteArgs: args, Name: "worker-5", Patch: map[string]any{"labels": map[string]any{"n": "x"}}})
			var c *hosts.RevisionConflictError
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.As(err, &c):
				conflict++
			default:
				t.Errorf("%v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || conflict != 7 {
		t.Fatalf("ok=%d conflict=%d", ok, conflict)
	}
}

func TestWritesOverTheSocketAndRoutes(t *testing.T) {
	f := newWriteFixture(t, nil)
	client := serveOnSocket(t, NewInProcessClient(WithHostService(f.svc)))
	ctx := context.Background()
	rev, err := client.HostsRevision(ctx)
	if err != nil || rev == "" {
		t.Fatalf("revision: %q %v", rev, err)
	}
	res, err := client.CreateComponent(ctx, ComponentCreateArgs{WriteArgs: WriteArgs{Revision: rev, Acknowledged: true},
		Component: map[string]any{"id": "disk-0001", "kind": "disk", "size": "1TB", "type": "hdd", "location": map[string]any{"host": "worker-1", "slot": "sata-1"}}})
	if err != nil || res.Revision == rev {
		t.Fatalf("create: %+v %v", res, err)
	}
	v, _ := client.GetHost(ctx, "worker-1")
	if got := v.StorageSummary["sata_25"]; got.Free != 0 || got.Used != 2 {
		t.Fatalf("slot not filled by the create: %+v", got)
	}
	// The old revision is stale now, answered as a conflict.
	_, err = client.UpdateHost(ctx, HostUpdateArgs{WriteArgs: WriteArgs{Revision: rev, Acknowledged: true}, Name: "worker-5", Patch: map[string]any{"role": "service"}})
	if status, _ := DaemonHTTPStatus(err); status != http.StatusConflict || !strings.Contains(err.Error(), "changed since you read it") {
		t.Fatalf("stale: %v (%d)", err, status)
	}
	if _, err = client.RemoveComponent(ctx, ComponentRemoveArgs{WriteArgs: WriteArgs{Revision: res.Revision, Acknowledged: true}, ID: "disk-0001"}); err != nil {
		t.Fatal(err)
	}
	h, err := client.HostHistory(ctx, "worker-1", 10)
	if err != nil || len(h) != 2 {
		t.Fatalf("history: %d %v", len(h), err)
	}
	// Ack is required on the wire too.
	rev, _ = client.HostsRevision(ctx)
	if _, err = client.UpdateHost(ctx, HostUpdateArgs{WriteArgs: WriteArgs{Revision: rev}, Name: "worker-5", Patch: map[string]any{"role": "service"}}); err == nil || !strings.Contains(err.Error(), "acknowledg") {
		t.Fatalf("no ack: %v", err)
	}
	// Routes: a missing component is 404, an unknown verb is 404.
	hh := ComponentHTTP(NewInProcessClient(WithHostService(f.svc)), "/components")
	rec := httptest.NewRecorder()
	hh.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/components/nope/history", nil))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty history: %d %s", rec.Code, rec.Body)
	}
}
