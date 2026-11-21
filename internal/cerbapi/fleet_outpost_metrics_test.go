package cerbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/pkg/outpost"
)

const outpostProbeV2 = `probe_version=2
os=Linux
arch=x86_64
kernel=6.8.0
disk_mount=/
disk_kb=1000000 400000
mount_kb=/ 1000000 600000
mount_kb=/home 1000000 600000
mount_kb=/var 1000000 600000
mount_kb=/var/tmp 1000000 600000
cpu_percent=37.5
cpu=4
mem_total_kb=8000000
mem_available_kb=6000000
load=0.5 0.4 0.3
uptime_seconds=1000
`

const outpostProbeLegacy = `os=Linux
arch=x86_64
kernel=6.8.0
disk_mount=/
disk_kb=1000000 400000
cpu=4
mem_total_kb=8000000
mem_available_kb=6000000
load=0.5 0.4 0.3
uptime_seconds=1000
`

// outpostSnapshot monitors one outpost-transport host whose fake executor
// answers the probe verb with output, and returns the fleet snapshot host.
func outpostSnapshot(t *testing.T, output string) (FleetSnapshotHost, *monitorRig) {
	t.Helper()
	var probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req outpost.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Verb != "probe" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		probes.Add(1)
		_ = json.NewEncoder(w).Encode(outpost.Result{Output: output})
	}))
	t.Cleanup(server.Close)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("fixture-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	yaml := fmt.Sprintf("version: 1\nhosts:\n  - {name: worker-x, role: worker, reach: {transport: outpost, outpost: {url: %q, token_file: %q}}}\n", server.URL, token)
	r := newMonitorRig(t, yaml, nil)
	r.tick(t)
	r.clock.Advance(3 * time.Minute)
	r.tick(t)
	if probes.Load() != 1 {
		t.Fatalf("outpost probes = %d", probes.Load())
	}
	out, err := r.svc.FleetSnapshot(context.Background(), FleetSnapshotQuery{})
	if err != nil || len(out.Hosts) != 1 {
		t.Fatalf("snapshot: %+v %v", out, err)
	}
	return out.Hosts[0], r
}

func TestOutpostTransportRecordsCPUAndMountsWithProbeVersion(t *testing.T) {
	h, r := outpostSnapshot(t, outpostProbeV2)
	ss, err := r.store.Samples(context.Background(), "worker-x", time.Time{}, time.Time{}, 0)
	if err != nil || len(ss) != 1 {
		t.Fatalf("samples: %+v %v", ss, err)
	}
	s := ss[0]
	if s.ProbeVersion != 2 || s.CPUPercent == nil || *s.CPUPercent != 37.5 {
		t.Fatalf("sample: %+v", s)
	}
	got := map[string]int64{}
	for _, m := range s.Mounts {
		got[m.Mount] = m.Used
	}
	for _, m := range []string{"/", "/var/tmp", "/home"} {
		if got[m] != 600000*1024 {
			t.Fatalf("mount %s missing or wrong in %v", m, s.Mounts)
		}
	}
	if h.MetricsGap != nil || h.Latest == nil || h.Latest.CPUPercent == nil || len(h.Latest.Disks) != 4 {
		t.Fatalf("snapshot host: %+v", h)
	}
}

func TestOutpostLegacyPluginIsReportedProbeOutdated(t *testing.T) {
	h, r := outpostSnapshot(t, outpostProbeLegacy)
	ss, _ := r.store.Samples(context.Background(), "worker-x", time.Time{}, time.Time{}, 0)
	if len(ss) != 1 || ss[0].ProbeVersion != 0 || ss[0].CPUPercent != nil || len(ss[0].Mounts) != 0 {
		t.Fatalf("legacy sample must keep null metrics: %+v", ss)
	}
	if h.Reachable == nil || !*h.Reachable {
		t.Fatalf("status semantics must be unchanged: %+v", h)
	}
	if h.Latest == nil || h.Latest.CPUPercent != nil || len(h.Latest.Disks) != 0 {
		t.Fatalf("metrics must stay null: %+v", h.Latest)
	}
	if h.MetricsGap == nil || h.MetricsGap.Code != MetricsGapProbeOutdated || h.MetricsGap.Detail == "" {
		t.Fatalf("metrics_gap: %+v", h.MetricsGap)
	}
}
