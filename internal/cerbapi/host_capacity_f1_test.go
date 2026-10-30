package cerbapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
)

const f1Hosts = `version: 1
hosts:
  - name: zbig
    role: worker
    reach: {ssh: box-a-ssh}
    hardware:
      memory: {type: DDR4, total: 32GiB, slots: 4, slots_used: 2}
      disks: [{name: nvme0n1, size: 1TB, type: nvme}]
      ceilings:
        memory:
          max_total: {value: 128GiB, source: vendor_spec}
      storage_slots:
        - {id: m2-0, kind: nvme_m2, occupant_disk: nvme0n1}
        - {id: m2-1, kind: nvme_m2}
  - name: asmall
    role: worker
    reach: {ssh: box-a-ssh}
    hardware:
      memory: {type: DDR4, total: 8GiB, slots: 2, slots_used: 2}
      disks: [{name: sda, size: 500GB, type: hdd}]
      ceilings:
        memory:
          max_total: {value: 8GiB, source: vendor_spec}
      storage_slots:
        - {id: sata-0, kind: sata_25, occupant_disk: sda}
  - name: mbare
    role: worker
    reach: {ssh: box-a-ssh}
    hardware:
      memory: {type: DDR4, total: 8GiB}
  - name: nohw
    role: worker
    reach: {ssh: box-a-ssh}
`

func capNames(res CapacityResult) (matched, unknown, excluded []string) {
	for _, h := range res.Matched {
		matched = append(matched, h.Host.Name)
	}
	for _, h := range res.Unknown {
		unknown = append(unknown, h.Name)
	}
	for _, h := range res.Excluded {
		excluded = append(excluded, h.Name)
	}
	return
}

func joined(s []string) string { return strings.Join(s, ",") }

func f1(t *testing.T, r *monitorRig, q CapacityQuery) CapacityResult {
	t.Helper()
	q.Explain = true
	res, err := r.svc.Capacity(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestCapacityOnHardwareFacts(t *testing.T) {
	r := newMonitorRig(t, f1Hosts, nil)
	cases := []struct {
		name                       string
		q                          CapacityQuery
		matched, unknown, excluded string
	}{
		{"memory total", CapacityQuery{MinMemoryTotal: 16 << 30}, "zbig", "nohw", "asmall,mbare"},
		{"upgrade room", CapacityQuery{MinMemoryUpgrade: 32 << 30}, "zbig", "mbare,nohw", "asmall"},
		{"free memory slots", CapacityQuery{FreeMemorySlots: 2}, "zbig", "mbare,nohw", "asmall"},
		{"free nvme slot", CapacityQuery{FreeStorageSlots: []SlotNeed{{Kind: "nvme_m2", Min: 1}}}, "zbig", "mbare,nohw", "asmall"},
		{"two free nvme slots", CapacityQuery{FreeStorageSlots: []SlotNeed{{Kind: "nvme_m2", Min: 2}}}, "", "mbare,nohw", "asmall,zbig"},
		{"disk type", CapacityQuery{DiskTypes: []string{"nvme", "ssd"}}, "zbig", "mbare,nohw", "asmall"},
		{"disk size", CapacityQuery{MinDiskSize: 800e9}, "zbig", "mbare,nohw", "asmall"},
	}
	for _, c := range cases {
		m, u, x := capNames(f1(t, r, c.q))
		if joined(m) != c.matched || joined(u) != c.unknown || joined(x) != c.excluded {
			t.Errorf("%s: matched=%s unknown=%s excluded=%s, want %s | %s | %s", c.name, joined(m), joined(u), joined(x), c.matched, c.unknown, c.excluded)
		}
	}
	// A missing fact says what is missing and is never zero.
	res := f1(t, r, CapacityQuery{FreeStorageSlots: []SlotNeed{{Kind: "nvme_m2", Min: 1}}})
	for _, u := range res.Unknown {
		if !strings.Contains(strings.Join(u.Reasons, ";"), "not recorded") {
			t.Fatalf("%s: %v", u.Name, u.Reasons)
		}
	}
}

func TestCapacityOnMonitorStatus(t *testing.T) {
	r := newMonitorRig(t, f1Hosts, nil)
	q := CapacityQuery{StatusIn: []string{hosts.StatusOnline, hosts.StatusDegraded}}
	if m, u, _ := capNames(f1(t, r, q)); len(m) != 0 || len(u) != 4 {
		t.Fatalf("before any probe nothing is judged online: %v %v", m, u)
	}
	r.tick(t)
	r.clock.Advance(3 * time.Minute)
	r.tick(t)
	m, u, x := capNames(f1(t, r, q))
	if joined(m) != "asmall,mbare,nohw,zbig" || len(u) != 0 || len(x) != 0 {
		t.Fatalf("%v %v %v", m, u, x)
	}
	r.down.Store(true)
	for i := 0; i < 3; i++ {
		r.clock.Advance(25 * time.Minute)
		r.tick(t)
	}
	m, _, x = capNames(f1(t, r, q))
	if len(m) != 0 || len(x) != 4 {
		t.Fatalf("offline hosts are excluded by status_in: %v %v", m, x)
	}
}

func seedSamples(t *testing.T, r *monitorRig, host string, n int, load float64, memAvail, diskFree int64) {
	t.Helper()
	for i := 0; i < n; i++ {
		l := load
		at := r.clock.Now().Add(-time.Duration(i+1) * 3 * time.Minute)
		if err := r.store.AppendSample(context.Background(), hosts.Sample{Host: host, At: at, Load1: &l, CPU: 4, MemoryAvailable: memAvail, DiskFree: diskFree, MemoryTotal: 16 << 30, DiskTotal: 200 << 30}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCapacityOnRecentHeadroomUsesTheWorstSample(t *testing.T) {
	r := newMonitorRig(t, f1Hosts, nil)
	seedSamples(t, r, "zbig", 4, 1.0, 8<<30, 100<<30)
	seedSamples(t, r, "asmall", 4, 1.0, 8<<30, 100<<30)
	// asmall had one bad sample inside the window: an average would hide it.
	l := 12.0
	if err := r.store.AppendSample(context.Background(), hosts.Sample{Host: "asmall", At: r.clock.Now().Add(-time.Minute), Load1: &l, CPU: 4, MemoryAvailable: 8 << 30, DiskFree: 100 << 30}); err != nil {
		t.Fatal(err)
	}
	seedSamples(t, r, "mbare", 1, 1.0, 8<<30, 100<<30) // 1 of 3 expected: not enough to judge
	q := CapacityQuery{Recent: &RecentNeed{Window: 15 * time.Minute, MaxLoadPerCore: 1.0, MinMemoryAvailable: 4 << 30, MinDiskFree: 50 << 30}}
	res := f1(t, r, q)
	m, u, x := capNames(res)
	if joined(m) != "zbig" || joined(u) != "mbare,nohw" || joined(x) != "asmall" {
		t.Fatalf("matched=%v unknown=%v excluded=%v", m, u, x)
	}
	j := res.Judged["zbig"]
	if j.Samples != 4 || j.Expected != 3 || j.WorstLoadPerCore != 0.25 || j.MinMemoryAvailable != 8<<30 {
		t.Fatalf("judged: %+v", j)
	}
	if !strings.Contains(strings.Join(res.Excluded[0].Reasons, ";"), "load per core reached 3.00") {
		t.Fatalf("the reason names the worst value: %v", res.Excluded[0].Reasons)
	}
	if !strings.Contains(strings.Join(res.Unknown[0].Reasons, ";"), "1 of 3 expected samples") {
		t.Fatalf("an unjudgeable host says why: %v", res.Unknown[0].Reasons)
	}
	// Memory limit: raising it above the worst sample excludes zbig.
	q.Recent.MinMemoryAvailable = 9 << 30
	if m, _, _ := capNames(f1(t, r, q)); len(m) != 0 {
		t.Fatalf("%v", m)
	}
}

func TestCapacityF1NeverRanks(t *testing.T) {
	r := newMonitorRig(t, f1Hosts, nil)
	// zbig is the roomiest and the quietest, asmall the opposite: the order
	// stays by name whatever the facts are.
	seedSamples(t, r, "zbig", 4, 0.1, 30<<30, 150<<30)
	seedSamples(t, r, "asmall", 4, 3.0, 1<<30, 60<<30)
	seedSamples(t, r, "mbare", 4, 1.0, 4<<30, 80<<30)
	res := f1(t, r, CapacityQuery{Recent: &RecentNeed{Window: 15 * time.Minute, MaxLoadPerCore: 4, MinDiskFree: 1 << 30}})
	m, _, _ := capNames(res)
	if joined(m) != "asmall,mbare,zbig" {
		t.Fatalf("matched must be in name order: %v", m)
	}
	raw, _ := json.Marshal(res)
	for _, banned := range []string{"score", "rank", "best", "prefer", "weight"} {
		if strings.Contains(strings.ToLower(string(raw)), banned) {
			t.Fatalf("%q in %s", banned, raw)
		}
	}
}

func TestCapacityF1ValidationAndWire(t *testing.T) {
	r := newMonitorRig(t, f1Hosts, nil)
	for name, q := range map[string]CapacityQuery{
		"bad kind":      {FreeStorageSlots: []SlotNeed{{Kind: "floppy", Min: 1}}},
		"zero min":      {FreeStorageSlots: []SlotNeed{{Kind: "nvme_m2"}}},
		"bad disk type": {DiskTypes: []string{"tape"}},
		"bad status":    {StatusIn: []string{"fine"}},
		"no limits":     {Recent: &RecentNeed{Window: time.Minute}},
		"window > 7d":   {Recent: &RecentNeed{Window: 8 * 24 * time.Hour, MaxLoadPerCore: 1}},
		"negative":      {MinMemoryTotal: -1},
	} {
		if _, err := r.svc.Capacity(context.Background(), q); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	in := CapacityQuery{MinMemoryTotal: 16 << 30, MinMemoryUpgrade: 8 << 30, FreeMemorySlots: 1, DiskTypes: []string{"nvme"}, MinDiskSize: 500e9,
		FreeStorageSlots: []SlotNeed{{Kind: "nvme_m2", Min: 1}}, StatusIn: []string{"online"},
		Recent: &RecentNeed{Window: 30 * time.Minute, MaxLoadPerCore: 0.5, MinMemoryAvailable: 2 << 30, MinDiskFree: 10 << 30}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out CapacityQuery
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	if out.MinMemoryTotal != in.MinMemoryTotal || out.FreeStorageSlots[0] != in.FreeStorageSlots[0] || *out.Recent != *in.Recent || out.StatusIn[0] != "online" || out.MinDiskSize != in.MinDiskSize {
		t.Fatalf("round trip: %+v", out)
	}
	if err := json.Unmarshal([]byte(`{"min_memory_total":"16GiB","recent":{"max_load_per_core":1}}`), &out); err != nil || out.Recent.Window != 15*time.Minute {
		t.Fatalf("the window defaults to 15m: %+v %v", out.Recent, err)
	}
	if err := json.Unmarshal([]byte(`{"min_gpu":1}`), &out); err == nil {
		t.Fatal("an unknown constraint must still be refused")
	}
}
