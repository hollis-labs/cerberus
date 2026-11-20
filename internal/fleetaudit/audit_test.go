package fleetaudit

import (
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/fleetprobe"
	"github.com/hollis-labs/cerberus/internal/testhome"
)

// TestMain keeps these tests out of the operator's home (internal/testhome).
func TestMain(m *testing.M) { testhome.Main(m) }

func n(i int) *int { return &i }

func inventory() *cerbapi.FleetInventory {
	goArt := func(path, mod, rev string, dirty bool) *fleetprobe.Artifact {
		return &fleetprobe.Artifact{Path: path, Kind: fleetprobe.ArtifactGo, SHA256: "ab", Go: &fleetprobe.GoBuild{Module: mod, Revision: rev, Modified: dirty, ModuleVersion: "(devel)"}}
	}
	behind := goArt("/srv/bin/hadron", "m/hadron", "1111111111111111", false)
	behind.Upstream = &fleetprobe.Upstream{Branch: "origin/main", Behind: n(388), Merged: []fleetprobe.MergedPR{{Number: 48, Title: "feat: x"}}}
	slightly := goArt("/srv/bin/tesseract", "m/tesseract", "2222222222222222", false)
	slightly.Upstream = &fleetprobe.Upstream{Branch: "origin/main", Behind: n(2)}
	replaced := goArt("/srv/bin/caster", "m/caster", "3333333333333333", false)
	replaced.ReplacedOnDisk = true
	return &cerbapi.FleetInventory{
		Hosts: []cerbapi.FleetHost{
			{Name: "hub", Status: cerbapi.FleetHostOK, Report: &fleetprobe.Report{Host: "hub", Services: []fleetprobe.Service{
				{Kind: fleetprobe.KindSystemdUser, Name: "u-torque", ResourceID: "hub-torque", Artifact: goArt("/srv/bin/torque", "m/torque", "4444444444444444", true)},
				{Kind: fleetprobe.KindSystemdUser, Name: "u-tether", ResourceID: "hub-tether", Artifact: goArt("/srv/bin/tether", "m/tether", "", false)},
				{Kind: fleetprobe.KindSystemdUser, Name: "u-hadron", ResourceID: "hub-hadron", Artifact: behind},
				{Kind: fleetprobe.KindSystemdUser, Name: "u-tesseract", ResourceID: "hub-tesseract", Artifact: slightly},
				{Kind: fleetprobe.KindSystemdUser, Name: "u-caster", ResourceID: "caster", Artifact: replaced},
				{Kind: fleetprobe.KindSystemdUser, Name: "u-gw", ResourceID: "llm-gateway", Artifact: goArt("/srv/bin/gw", "m/station", "5555555555555555", false)},
			}}},
			{Name: "w1", Status: cerbapi.FleetHostUnreachable, Reason: "ssh: handshake failed"},
			{Name: "w2", Status: cerbapi.FleetHostNoProbe, Reason: "no probe staged"},
		},
		Drift: []fleetprobe.Drift{{Name: "tether", Module: "m/tether", Builds: map[string][]string{"a": {"hub"}, "b": {"w3"}}}},
	}
}

func TestRunFindings(t *testing.T) {
	got := Run(inventory(), Options{
		Resources: []config.ResourceDef{
			{ID: "hub-torque", Config: map[string]any{"auto_start": true, "service_name": "u-torque"}},
			{ID: "warden", Config: map[string]any{"auto_start": true, "service_name": "warden"}},
			{ID: "manual", Config: map[string]any{"auto_start": false, "service_name": "manual"}},
			{ID: "elsewhere", Host: "w9", Config: map[string]any{"auto_start": true, "service_name": "x"}},
		},
		ResourceModules: map[string]string{"llm-gateway": "m/llm-gateway"},
		Descriptors:     []string{"/cfg/hub/agent.cerberus.yaml", "/cfg/stale/agent.cerberus.yaml"},
		Registered:      []string{"/cfg/hub/agent.cerberus.yaml"},
	})
	kinds := map[string][]Finding{}
	for _, f := range got {
		kinds[f.Kind] = append(kinds[f.Kind], f)
	}
	expect := map[string]int{
		KindDirtyBuild: 1, KindUnstamped: 1, KindBehind: 1, KindReplacedOnDisk: 1, KindModuleMismatch: 1,
		KindStaleDescriptor: 1, KindNotRunning: 1, KindHostUnreachable: 1, KindNoProbe: 1, KindDrift: 1,
	}
	for k, want := range expect {
		if len(kinds[k]) != want {
			t.Errorf("%s: %d findings, want %d: %+v", k, len(kinds[k]), want, kinds[k])
		}
	}
	if f := kinds[KindDirtyBuild][0]; f.Severity != High || f.Subject != "resource hub-torque" {
		t.Errorf("dirty = %+v", f)
	}
	if f := kinds[KindBehind][0]; f.Severity != Medium || f.Subject != "resource hub-hadron" {
		t.Errorf("behind = %+v (388 > 5*20 is medium; tesseract at 2 is not a finding)", f)
	}
	if f := kinds[KindNotRunning][0]; f.Subject != "resource warden" {
		t.Errorf("not running = %+v", f)
	}
	if f := kinds[KindStaleDescriptor][0]; f.Subject != "/cfg/stale/agent.cerberus.yaml" {
		t.Errorf("stale = %+v", f)
	}
	if got[0].Severity != High {
		t.Errorf("not sorted by severity: %+v", got[0])
	}
}

func TestIDsStableAndMarkNew(t *testing.T) {
	a := Run(inventory(), Options{})
	b := Run(inventory(), Options{})
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("runs differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatalf("finding %d ID unstable: %s vs %s", i, a[i].ID, b[i].ID)
		}
	}
	prev := []string{a[0].ID, "gone-finding"}
	resolved := MarkNew(b, prev)
	if b[0].New || !b[1].New {
		t.Fatalf("new marks wrong: %+v %+v", b[0], b[1])
	}
	if len(resolved) != 1 || resolved[0] != "gone-finding" {
		t.Fatalf("resolved = %v", resolved)
	}
}
