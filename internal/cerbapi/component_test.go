package cerbapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
)

const partsYAML = `version: 1
hosts:
  - name: plain
    hardware:
      memory: {type: DDR4, total: 8GiB, slots: 2, slots_used: 1}
      disks: [{name: sda, size: 120GB, type: ssd}]
  - name: rich
    hardware:
      disks: [{name: nvme0n1, size: 1TB, type: nvme}]
      memory: {slots: 2, slots_used: 1}
      ceilings:
        memory:
          slots: {value: 2, source: dmi}
          max_total: {value: 32GiB, source: vendor_spec}
      storage_slots:
        - {id: m2-0, kind: nvme_m2, occupant_disk: nvme0n1}
        - {id: sata-0, kind: sata_25, occupant_component: d1}
        - {id: sata-1, kind: sata_25}
components:
  - {id: d1, kind: disk, size: 2TB, type: hdd, serial: SN1, location: {host: rich, slot: sata-0}}
  - {id: r-spare, kind: ram, size: 8GiB, type: DDR4, location: {spare: true}}
  - {id: r1, kind: ram, size: 8GiB, location: {host: rich, slot: dimm0}}
`

func partsService(t *testing.T) (*HostService, *audit.Memory) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.yaml")
	if err := os.WriteFile(path, []byte(partsYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := audit.NewMemory()
	return NewHostService(sink, HostServiceOptions{HostsPath: path, StatePath: "-"}), sink
}

func TestHostWithHardwareButNoCeilingsReadsUnknownNotZero(t *testing.T) {
	svc, _ := partsService(t)
	v, err := svc.Get(context.Background(), "plain")
	if err != nil {
		t.Fatal(err)
	}
	if v.Ceilings != nil || v.StorageSlots != nil || v.StorageSummary != nil {
		t.Fatalf("unrecorded parts must be nil: %+v", v)
	}
	data, _ := json.Marshal(v)
	for _, want := range []string{`"ceilings":null`, `"storage_slots":null`, `"storage_summary":null`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("%s missing from %s", want, data)
		}
	}
	if strings.Contains(string(data), `"storage_summary":{`) || strings.Contains(string(data), `"free":0`) {
		t.Fatalf("zero counts invented: %s", data)
	}
}

func TestHostViewCarriesSlotsSummaryAndComponents(t *testing.T) {
	svc, _ := partsService(t)
	v, err := svc.Get(context.Background(), "rich")
	if err != nil {
		t.Fatal(err)
	}
	if v.Ceilings == nil || v.Ceilings.Memory.MaxTotal.Source != "vendor_spec" || len(v.StorageSlots) != 3 {
		t.Fatalf("view: %+v", v)
	}
	if got := v.StorageSummary["sata_25"]; got.Total != 2 || got.Used != 1 || got.Free != 1 {
		t.Fatalf("sata summary: %+v", got)
	}
	if len(v.Components) != 2 {
		t.Fatalf("components on the host: %+v", v.Components)
	}
}

func TestComponentReadsAndAuditNotes(t *testing.T) {
	svc, sink := partsService(t)
	ctx := context.Background()
	all, err := svc.ListComponents(ctx, ComponentFilter{})
	if err != nil || len(all) != 3 || all[0].ID != "d1" {
		t.Fatalf("list: %+v %v", all, err)
	}
	spare, _ := svc.ListComponents(ctx, ComponentFilter{Spare: true})
	disks, _ := svc.ListComponents(ctx, ComponentFilter{Kind: "disk"})
	onHost, _ := svc.ListComponents(ctx, ComponentFilter{Host: "rich"})
	if len(spare) != 1 || len(disks) != 1 || len(onHost) != 2 {
		t.Fatalf("filters: spare=%d disks=%d host=%d", len(spare), len(disks), len(onHost))
	}
	if c, err := svc.GetComponent(ctx, "d1"); err != nil || c.Serial != "SN1" {
		t.Fatalf("get: %+v %v", c, err)
	}
	if _, err := svc.GetComponent(ctx, "nope"); err == nil || !strings.Contains(err.Error(), `component "nope" is not declared`) {
		t.Fatalf("not found: %v", err)
	}
	if _, err := svc.List(ctx, HostFilter{}); err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, r := range sink.Records() {
		notes = append(notes, r.Reason)
	}
	joined := strings.Join(notes, "|")
	for _, want := range []string{"components=3 spare=1", "components=1 spare=0", "hosts=2 components=2"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("audit notes lack %q: %s", want, joined)
		}
	}
}

func TestComponentRoutes(t *testing.T) {
	svc, _ := partsService(t)
	h := ComponentHTTP(NewInProcessClient(WithHostService(svc)), "/components")
	get := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}
	if rec := get(http.MethodGet, "/components?kind=ram"); rec.Code != 200 || strings.Count(rec.Body.String(), `"kind":"ram"`) != 2 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := get(http.MethodGet, "/components/d1"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"slot":"sata-0"`) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	if rec := get(http.MethodGet, "/components/nope"); rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
	if rec := get(http.MethodPost, "/components"); rec.Code != 404 {
		t.Fatalf("a component route accepts only GET: %d", rec.Code)
	}
}

func TestComponentsOverTheSocket(t *testing.T) {
	svc, _ := partsService(t)
	client := serveOnSocket(t, NewInProcessClient(WithHostService(svc)))
	comps, err := client.ListComponents(context.Background(), ComponentFilter{Host: "rich"})
	if err != nil || len(comps) != 2 {
		t.Fatalf("list: %+v %v", comps, err)
	}
	c, err := client.GetComponent(context.Background(), "r-spare")
	if err != nil || !c.Location.Spare {
		t.Fatalf("get: %+v %v", c, err)
	}
	if _, err := client.GetComponent(context.Background(), "nope"); err == nil {
		t.Fatal("unknown component accepted")
	}
}
