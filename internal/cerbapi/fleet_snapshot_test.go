package cerbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
)

func TestFleetSnapshotMetricsBucketsAndSocket(t *testing.T) {
	r := newMonitorRig(t, oneSSHHost, nil)
	r.stdout += "cpu_percent=0\nmount_kb=/ 1000 300\nmount_kb=/var/tmp 2000 1500\n"
	r.tick(t)
	r.clock.Advance(3 * time.Minute)
	r.tick(t)
	now := r.clock.Now()
	// Distinct samples within a bucket: only the newest survives. A sample on
	// the left boundary is included; one before it and future data are excluded.
	for i, ago := range []time.Duration{61 * time.Minute, 60 * time.Minute, 59 * time.Minute, 56 * time.Minute, 55 * time.Minute, 1 * time.Minute, -time.Minute} {
		n := float64(i + 10)
		x := hosts.Sample{Host: "box-a", At: now.Add(-ago), CPU: 64, CPUPercent: &n, MemoryTotal: 100, MemoryAvailable: 25, Load1: &n, Mounts: []hosts.MountSample{{Mount: "/var/tmp", Used: 20, Total: 100}}}
		if err := r.store.AppendSample(context.Background(), x); err != nil {
			t.Fatal(err)
		}
	}
	client := serveOnSocket(t, NewInProcessClient(WithHostService(r.svc)))
	out, err := client.FleetSnapshot(context.Background(), FleetSnapshotQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Schema != "cerberus.fleet.snapshot.v1" || out.TotalHosts != 1 || out.StepSeconds != 300 || len(out.Hosts) != 1 {
		t.Fatalf("envelope: %+v", out)
	}
	if r.execs.Load() != 1 || r.hostRecords()[OpFleetSnapshot] != 1 {
		t.Fatalf("snapshot must be an audited read without probes: execs=%d audit=%v", r.execs.Load(), r.hostRecords())
	}
	h := out.Hosts[0]
	if h.Reachable == nil || !*h.Reachable || h.Stale || h.LastSeen == nil || !h.LastSeen.Equal(now) {
		t.Fatalf("health: %+v", h)
	}
	if h.Latest == nil || h.Latest.CPUPercent == nil || *h.Latest.CPUPercent != 0 || len(h.Latest.Disks) != 2 || h.Latest.Disks[1].Mount != "/var/tmp" || h.Latest.Disks[1].Used != 1500*1024 {
		t.Fatalf("latest: %+v", h.Latest)
	}
	if len(h.Series) != 12 || h.Series[0].CPUPercent == nil || *h.Series[0].CPUPercent != 13 || h.Series[1].CPUPercent == nil || *h.Series[1].CPUPercent != 14 || h.Series[2].SampleAt != nil || h.Series[11].SampleAt == nil {
		t.Fatalf("series: %+v", h.Series)
	}
	if *h.Series[0].MemoryUsed != 75 || *h.Series[0].MemoryTotal != 100 || len(h.Series[0].Disks) != 1 {
		t.Fatalf("metrics: %+v", h.Series[0])
	}
	if !h.Series[0].TS.Equal(now.Add(-time.Hour)) || !h.Series[0].SampleAt.Equal(now.Add(-56*time.Minute)) {
		t.Fatalf("timestamps: %+v", h.Series[0])
	}
	// Failure changes current reachability without erasing last good metrics.
	st, _, _ := r.store.Status(context.Background(), "box-a")
	st.ConsecutiveFailures = 1
	at := now.Add(time.Second)
	st.LastAttempt = &at
	r.clock.Advance(time.Second)
	if putErr := r.store.PutStatus(context.Background(), st); putErr != nil {
		t.Fatal(putErr)
	}
	out, err = r.svc.FleetSnapshot(context.Background(), FleetSnapshotQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Hosts[0].Reachable == nil || *out.Hosts[0].Reachable {
		t.Fatalf("failed attempt: %+v", out.Hosts[0])
	}
	r.clock.Advance(time.Hour)
	out, err = r.svc.FleetSnapshot(context.Background(), FleetSnapshotQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Hosts[0].Reachable != nil || !out.Hosts[0].Stale || out.Hosts[0].Latest == nil {
		t.Fatalf("stale: %+v", out.Hosts[0])
	}
}

func TestFleetSnapshotEveryHostPaginationUnknownAndNoSecrets(t *testing.T) {
	r := newMonitorRig(t, `version: 1
hosts:
 - {name: zeta, role: worker, labels: {secret: private-value}, reach: {ssh: box-a-ssh}}
 - {name: alpha, role: service, reach: {ssh: box-a-ssh}}
 - {name: never}
`, nil)
	// An old sample has a core count and an anonymous disk, NOT CPU percent or
	// an attributable mount. Never fabricate either.
	if err := r.store.AppendSample(context.Background(), hosts.Sample{Host: "alpha", At: r.clock.Now().Add(-time.Minute), CPU: 64, DiskTotal: 100, DiskFree: 30}); err != nil {
		t.Fatal(err)
	}
	out, err := r.svc.FleetSnapshot(context.Background(), FleetSnapshotQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if out.TotalHosts != 3 || len(out.Hosts) != 2 || out.NextOffset == nil || *out.NextOffset != 2 || out.Hosts[0].Name != "alpha" || out.Hosts[1].Name != "never" {
		t.Fatalf("page: %+v", out)
	}
	h := out.Hosts[0]
	if h.Latest.CPUPercent != nil || len(h.Latest.Disks) != 0 || h.Reachable != nil {
		t.Fatalf("legacy: %+v", h)
	}
	h = out.Hosts[1]
	if h.Latest != nil || h.LastSeen != nil || h.Reachable != nil || len(h.Roles) != 0 || len(h.Series) != 12 {
		t.Fatalf("never observed: %+v", h)
	}
	out, err = r.svc.FleetSnapshot(context.Background(), FleetSnapshotQuery{Offset: 2, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if out.NextOffset != nil || len(out.Hosts) != 1 || out.Hosts[0].Roles[0] != "worker" {
		t.Fatalf("last page: %+v", out)
	}
	b, _ := json.Marshal(out)
	for _, bad := range []string{"private-value", "resource", "token", "session", "labels", "ssh"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("private field %q in %s", bad, b)
		}
	}
	for _, q := range []FleetSnapshotQuery{{Offset: -1}, {Limit: 101}, {Limit: -1}} {
		if _, queryErr := r.svc.FleetSnapshot(context.Background(), q); queryErr == nil {
			t.Fatalf("accepted %+v", q)
		}
	}
	out, err = r.svc.FleetSnapshot(context.Background(), FleetSnapshotQuery{Offset: 999})
	if err != nil || len(out.Hosts) != 0 || out.TotalHosts != 3 {
		t.Fatalf("past end: %+v %v", out, err)
	}
	// Known hosts remain visible with monitoring globally disabled.
	r.mon.settings.Enabled = false
	out, err = r.svc.FleetSnapshot(context.Background(), FleetSnapshotQuery{})
	if err != nil || len(out.Hosts) != 3 {
		t.Fatalf("disabled: %+v %v", out, err)
	}
}

func TestFleetSnapshotHTTPValidationAndUnavailable(t *testing.T) {
	r := newMonitorRig(t, oneSSHHost, nil)
	handler := HostHTTP(NewInProcessClient(WithHostService(r.svc)), "/hosts")
	for _, query := range []string{"offset=no", "limit=1.2", "offset=-1", "limit=101"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hosts/_fleet_snapshot?"+query, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body.String())
		}
	}
	r.svc.mon = nil
	if _, err := r.svc.FleetSnapshot(context.Background(), FleetSnapshotQuery{}); err == nil {
		t.Fatal("missing monitor accepted")
	}
	// Keep empty arrays in the wire shape, including a genuinely empty fleet.
	if err := os.WriteFile(r.path, []byte("version: 1\nhosts: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r.svc.mon = r.mon
	out, err := r.svc.FleetSnapshot(context.Background(), FleetSnapshotQuery{})
	if err != nil || out.Hosts == nil || len(out.Hosts) != 0 {
		t.Fatalf("empty fleet: %+v %v", out, err)
	}
}

func TestFleetSnapshotDefaultPageBound(t *testing.T) {
	var b strings.Builder
	b.WriteString("version: 1\nhosts:\n")
	for i := 0; i < 105; i++ {
		fmt.Fprintf(&b, " - {name: node-%03d}\n", i)
	}
	r := newMonitorRig(t, b.String(), nil)
	out, err := r.svc.FleetSnapshot(context.Background(), FleetSnapshotQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Hosts) != 100 || out.TotalHosts != 105 || out.NextOffset == nil || *out.NextOffset != 100 || out.Hosts[99].Name != "node-099" {
		t.Fatalf("bound: %+v", out)
	}
}
