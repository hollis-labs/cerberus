package hosts

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fileWith(h ...Host) *File { return &File{Version: FileVersion, Hosts: h} }

func TestValidate(t *testing.T) {
	ok := Host{Name: "box-a", Hostname: "box-a.example.test", TailnetName: "box-a.tailnet.example.test",
		TailnetIP: "100.64.0.10", LanIP: "192.0.2.10", Role: "worker", Provider: "homelab",
		Labels: map[string]string{"agents": "true", "team/zone": "a"}, TTL: "72h"}
	tests := []struct {
		name string
		file *File
		want string // substring of the error; "" means valid
	}{
		{"valid", fileWith(ok), ""},
		{"empty registry", &File{Version: FileVersion}, ""},
		{"bad version", &File{Version: 2}, "version"},
		{"missing name", fileWith(Host{}), "hosts[0].name: missing"},
		{"bad name", fileWith(Host{Name: "Box A"}), "name"},
		{"duplicate", fileWith(Host{Name: "a"}, Host{Name: "a"}), "duplicate"},
		{"bad hostname", fileWith(Host{Name: "a", Hostname: "not a host"}), "hostname"},
		{"tailnet ip not an ip", fileWith(Host{Name: "a", TailnetIP: "nope"}), "tailnet_ip"},
		{"tailnet ip outside range", fileWith(Host{Name: "a", TailnetIP: "198.51.100.4"}), "outside the tailnet range"},
		{"tailnet ip just below range", fileWith(Host{Name: "a", TailnetIP: "100.63.255.255"}), "outside the tailnet range"},
		{"tailnet ip top of range", fileWith(Host{Name: "a", TailnetIP: "100.127.255.255"}), ""},
		{"tailnet ipv6", fileWith(Host{Name: "a", TailnetIP: "fd7a:115c:a1e0::1"}), ""},
		{"lan ip invalid", fileWith(Host{Name: "a", LanIP: "192.0.2.300"}), "lan_ip"},
		{"unknown role", fileWith(Host{Name: "a", Role: "wizard"}), "unknown role"},
		{"extended role", &File{Version: 1, ExtraRoles: []string{"gpu-farm"}, Hosts: []Host{{Name: "a", Role: "gpu-farm"}}}, ""},
		{"bad extra role", &File{Version: 1, ExtraRoles: []string{"Bad Role"}}, "extra_roles"},
		{"unknown provider", fileWith(Host{Name: "a", Provider: "acme"}), "unknown provider"},
		{"extended provider", &File{Version: 1, ExtraProviders: []string{"acme"}, Hosts: []Host{{Name: "a", Provider: "acme"}}}, ""},
		{"label key invalid", fileWith(Host{Name: "a", Labels: map[string]string{"Bad Key": "x"}}), "labels.Bad Key"},
		{"label value too long", fileWith(Host{Name: "a", Labels: map[string]string{"k": strings.Repeat("x", 64)}}), "longer than"},
		{"label value control char", fileWith(Host{Name: "a", Labels: map[string]string{"k": "a\nb"}}), "control characters"},
		{"too many labels", fileWith(Host{Name: "a", Labels: manyLabels(33)}), "at most 32"},
		{"bad ttl", fileWith(Host{Name: "a", TTL: "soon"}), "ttl"},
		{"negative ttl", fileWith(Host{Name: "a", TTL: "-1h"}), "ttl"},
		{"negative cost", fileWith(Host{Name: "a", Cost: &Cost{Monthly: -1}}), "cost.monthly"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.file)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func manyLabels(n int) map[string]string {
	m := map[string]string{}
	for i := 0; i < n; i++ {
		m["k"+string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
	}
	return m
}

func TestValidateReportsEveryProblem(t *testing.T) {
	err := Validate(fileWith(Host{Name: "a", TailnetIP: "x", LanIP: "y", Role: "z"}))
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Problems) != 3 {
		t.Fatalf("err = %#v, want 3 problems", err)
	}
}

func TestParse(t *testing.T) {
	good := `version: 1
hosts:
  - name: box-a
    tailnet_ip: 100.64.0.10
    role: worker
    reach: {ssh: box-a-ssh}
    declared: {os: linux, arch: amd64, cpu: 8, memory: 16GiB, disk: 512GB}
    cost: {monthly: 12.5, currency: USD}
`
	f, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	h := f.Hosts[0]
	if h.Declared.Memory != 16<<30 || h.Declared.Disk != 512_000_000_000 || h.Reach.SSH != "box-a-ssh" {
		t.Fatalf("parsed %+v", h)
	}
	if _, err := Parse([]byte("version: 1\nhosts:\n  - name: a\n    tailnet_ipp: 100.64.0.1\n")); err == nil || !strings.Contains(err.Error(), "tailnet_ipp") {
		t.Fatalf("unknown field error = %v", err)
	}
	if _, err := Parse([]byte("")); err == nil {
		t.Fatal("empty file accepted")
	}
	if _, err := Parse([]byte("version: 1\nhosts:\n  - name: a\n    declared: {memory: lots}\n")); err == nil {
		t.Fatal("bad size accepted")
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "hosts.yaml"))
	if err != nil || len(f.Hosts) != 0 {
		t.Fatalf("Load = %+v, %v", f, err)
	}
}

func TestParsePosixProbe(t *testing.T) {
	out := "os=Linux\narch=x86_64\nkernel=6.1.0-test\ncpu=8\nmem_total_kb=16000000\nmem_available_kb=8000000\n" +
		"load=0.50 0.40 0.30\nuptime_seconds=1000\ndisk_kb=100000000 40000000\ntool_docker=1\ntool_tmux=0\ntool_other=1\nunknown=zzz\n"
	p := ParsePosixProbe(out)
	if p.Source != "posix" || p.OS != "Linux" || p.CPU != 8 || p.MemoryTotal != 16_000_000*1024 || p.MemoryAvailable != 8_000_000*1024 ||
		p.DiskTotal != 100_000_000*1024 || p.DiskFree != 40_000_000*1024 || p.UptimeSeconds != 1000 {
		t.Fatalf("probed = %+v", p)
	}
	if p.Load1 == nil || *p.Load1 != 0.5 || *p.Load15 != 0.3 {
		t.Fatalf("load = %v %v", p.Load1, p.Load15)
	}
	if !p.Tools["docker"] || p.Tools["tmux"] || len(p.Tools) != 2 {
		t.Fatalf("tools = %v", p.Tools)
	}
	if (ParsePosixProbe("garbage\nno equals")).Empty() != true {
		t.Fatal("garbage should be empty")
	}
}

func TestProbeFactsAreAllowListed(t *testing.T) {
	out := "os=Linux\narch=password=hunter2hunter2\nkernel=sk-abcdefghijklmnop1234\ncpu=99999\nload=-1 x y\n"
	p := ParsePosixProbe(out)
	if p.Arch != "" || p.Kernel != "" || p.CPU != 0 || p.Load1 != nil || p.OS != "Linux" {
		t.Fatalf("probed = %+v", p)
	}
	g := ParsePosixProbe("cpu_model=Model X sk-abcdefghijklmnop1234\ngpu_model=ok card\ngpu_count=2\ndisk_mount=/etc\n")
	if g.CPUModel != "" || g.GPUModel != "ok card" || g.GPUCount == nil || *g.GPUCount != 2 || g.DiskMount != "" {
		t.Fatalf("text facts = %+v", g)
	}
}

const linuxFixture = "os=Linux\narch=x86_64\nkernel=6.1.0-test\ndisk_mount=/data\ndisk_kb=200000000 150000000\nos_version=12\ncpu=16\n" +
	"cpu_model=Example CPU 9000 @ 3.00GHz\nmem_total_kb=32000000\nmem_available_kb=24000000\nload=1.00 0.50 0.25\nuptime_seconds=86400\n" +
	"gpu_count=1\ngpu_model=Example GPU 24GB\ntool_docker=1\ntool_tmux=1\ntool_tailscale=1\ntool_caddy=0\n"

// A macOS report, as the script's Darwin branch prints it.
const macFixture = "os=Darwin\narch=arm64\nkernel=24.0.0\ndisk_mount=/System/Volumes/Data\ndisk_kb=480000000 300000000\nos_version=15.1\ncpu=10\n" +
	"cpu_model=Example Silicon M9\nmem_total_bytes=34359738368\nmem_available_bytes=425984000\nload=1.20 1.10 1.00 \nuptime_seconds=3600\n" +
	"tool_docker=0\ntool_tmux=1\ntool_tailscale=1\ntool_caddy=0\n"

func TestParsePosixProbeFixtures(t *testing.T) {
	l := ParsePosixProbe(linuxFixture)
	if l.OS != "Linux" || l.OSVersion != "12" || l.CPU != 16 || l.CPUModel != "Example CPU 9000 @ 3.00GHz" || l.MemoryTotal != 32_000_000*1024 ||
		l.DiskMount != "/data" || l.DiskFree != 150_000_000*1024 || l.GPUCount == nil || *l.GPUCount != 1 || l.GPUModel != "Example GPU 24GB" || l.UptimeSeconds != 86400 {
		t.Fatalf("linux = %+v", l)
	}
	m := ParsePosixProbe(macFixture)
	if m.OS != "Darwin" || m.Arch != "arm64" || m.MemoryTotal != 34359738368 || m.MemoryAvailable != 425984000 || m.UptimeSeconds != 3600 ||
		m.Load1 == nil || *m.Load1 != 1.2 || m.DiskMount != "/System/Volumes/Data" || m.GPUCount != nil || m.Tools["docker"] {
		t.Fatalf("mac = %+v", m)
	}
}

func TestPosixProbeCommandIsOneQuotedShCall(t *testing.T) {
	if !strings.HasPrefix(PosixProbeCommand, "sh -c '") || !strings.HasSuffix(PosixProbeCommand, "'") {
		t.Fatalf("command shape: %.40s", PosixProbeCommand)
	}
	for _, bad := range []string{"rm ", "mv ", " > ", "kill", "sudo", "systemctl", "tee "} {
		if strings.Contains(posixProbeScript, bad) {
			t.Fatalf("probe script contains %q: it must stay read-only", bad)
		}
	}
}

func TestStoreRoundTripAndForget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "hosts.json")
	s := NewStore(path)
	if got := s.Get("a"); got.State != StateUnknown {
		t.Fatalf("fresh state = %+v", got)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.Put("a", State{State: StateOnline, ProbedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("b", State{State: StateOffline}); err != nil {
		t.Fatal(err)
	}
	s2 := NewStore(path)
	if got := s2.Get("a"); got.State != StateOnline || got.ProbedAt == nil || !got.ProbedAt.Equal(now) {
		t.Fatalf("reloaded = %+v", got)
	}
	if err := s2.Forget(map[string]bool{"a": true}); err != nil {
		t.Fatal(err)
	}
	if got := s2.Get("b"); got.State != StateUnknown {
		t.Fatalf("forgotten host kept state: %+v", got)
	}
	mem := NewStore("")
	_ = mem.Put("a", State{State: StateOnline})
	if mem.Get("a").State != StateOnline {
		t.Fatal("memory store lost state")
	}
}
