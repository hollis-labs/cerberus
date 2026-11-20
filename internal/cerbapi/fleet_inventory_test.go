package cerbapi

import (
	"runtime"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/hosts"
)

func TestFleetProbeCommandQuotesBinDirs(t *testing.T) {
	cmd := fleetProbeCommand([]string{"/opt/my tools/bin", "/x'y"})
	if !strings.Contains(cmd, `--bin-dir '/opt/my tools/bin'`) || !strings.Contains(cmd, `--bin-dir '/x'\''y'`) {
		t.Fatalf("command = %s", cmd)
	}
	if !strings.Contains(cmd, "--no-registry") || !strings.Contains(cmd, noProbeMarker) || !strings.Contains(cmd, fleetProbeRel) {
		t.Fatalf("command = %s", cmd)
	}
}

func TestSelectFleetHosts(t *testing.T) {
	all := []hosts.Host{{Name: "w2", Role: "worker"}, {Name: "hub", Role: "hub"}, {Name: "w1", Role: "worker"}}
	got, err := selectFleetHosts(all, FleetInventoryQuery{Role: "worker"})
	if err != nil || len(got) != 2 || got[0].Name != "w1" || got[1].Name != "w2" {
		t.Fatalf("role select = %+v %v", got, err)
	}
	if _, err := selectFleetHosts(all, FleetInventoryQuery{Hosts: []string{"w1", "nope"}}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown host not refused: %v", err)
	}
}

func TestHostBinDirsLabel(t *testing.T) {
	h := hosts.Host{Labels: map[string]string{fleetBinDirsLabel: " /a : /b/c ::"}}
	if got := hostBinDirs(h); len(got) != 2 || got[0] != "/a" || got[1] != "/b/c" {
		t.Fatalf("dirs = %v", got)
	}
	if hostBinDirs(hosts.Host{}) != nil {
		t.Fatal("no label should mean the probe default")
	}
}

func TestFleetResourceRefs(t *testing.T) {
	refs := FleetResourceRefs([]config.ResourceDef{
		{ID: "warden", Project: "warden", Config: map[string]any{"service_name": "warden", "health_check": map[string]any{"url": "http://127.0.0.1:1/healthz"}, "url": "https://example.invalid"}},
		{ID: "grafana", Project: "otel", Config: map[string]any{"container": "grafana", "url": "https://grafana.example.invalid"}},
		{ID: "tcp", Config: map[string]any{"url": "tcp://x"}},
	})
	if refs[0].Units[0] != "warden" || refs[0].HealthURL != "http://127.0.0.1:1/healthz" {
		t.Fatalf("warden = %+v", refs[0])
	}
	if refs[1].Containers[0] != "grafana" || refs[1].HealthURL != "https://grafana.example.invalid" {
		t.Fatalf("grafana = %+v", refs[1])
	}
	if refs[2].HealthURL != "" {
		t.Fatalf("non-http url used for health: %+v", refs[2])
	}
}

func TestMatchesPlatform(t *testing.T) {
	if !matchesPlatform(map[string]string{"linux": "Linux", "darwin": "Darwin"}[goosForTest()], archForTest()) {
		t.Fatal("own platform not matched")
	}
	if matchesPlatform("Plan9", "x86_64") {
		t.Fatal("foreign OS matched")
	}
}

func goosForTest() string { return runtime.GOOS }

func archForTest() string {
	return map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
}
