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
		{"hardware", fileWith(Host{Name: "a", Hardware: &Hardware{Vendor: "LENOVO", Model: "ThinkCentre M715q", Serial: "PC0XXXXX",
			Memory: &HardwareMemory{Type: "DDR4", Total: 8 << 30, Max: 32 << 30, Slots: 2, SlotsUsed: 1},
			Disks:  []HardwareDisk{{Name: "sda", Size: 120 << 30, Type: "ssd", Bus: "sata"}},
			NICs:   []HardwareNIC{{Name: "enp2s0f0", MAC: "6c:4b:90:59:4e:78"}}}}), ""},
		{"hardware bad mac", fileWith(Host{Name: "a", Hardware: &Hardware{NICs: []HardwareNIC{{Name: "eth0", MAC: "6C-4B-90-59-4E-78"}}}}), "hardware.nics[0].mac"},
		{"hardware bad disk type", fileWith(Host{Name: "a", Hardware: &Hardware{Disks: []HardwareDisk{{Name: "sda", Type: "floppy"}}}}), "hardware.disks[0].type"},
		{"hardware slots", fileWith(Host{Name: "a", Hardware: &Hardware{Memory: &HardwareMemory{Slots: 2, SlotsUsed: 3}}}), "slots_used"},
		{"hardware control char", fileWith(Host{Name: "a", Hardware: &Hardware{Serial: "a\nb"}}), "hardware.serial"},
		{"hardware too long", fileWith(Host{Name: "a", Hardware: &Hardware{Model: strings.Repeat("x", 129)}}), "hardware.model"},
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
	hw := good + `    hardware:
      vendor: HP
      model: 750-217c
      serial: ABC123
      memory: {type: DDR3, total: 12GiB, slots: 4, slots_used: 2, modules: ["8GiB DDR3 1600", "4GiB DDR3 1600"]}
      disks: [{name: sda, model: WDC WD10EZEX-60W, size: 1TB, type: hdd, bus: sata}]
      nics: [{name: enp2s0, mac: "c0:7c:d1:fb:63:78"}]
      recorded_at: 2026-10-23T22:00:00Z
`
	if f, err := Parse([]byte(hw)); err != nil || f.Hosts[0].Hardware == nil || f.Hosts[0].Hardware.Memory.Total != 12<<30 ||
		f.Hosts[0].Hardware.Disks[0].Size != 1_000_000_000_000 || f.Hosts[0].Hardware.RecordedAt == nil {
		t.Fatalf("hardware parse = %+v, %v", f, err)
	}
	if _, err := Parse([]byte(good + "    hardware: {colour: beige}\n")); err == nil || !strings.Contains(err.Error(), "colour") {
		t.Fatalf("unknown hardware field error = %v", err)
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

// Hardware lines as the Linux branch prints them (from a ThinkCentre and an HP).
const hardwareFixture = "hw_vendor=LENOVO\nhw_model=10M2S08Y00\nhw_version=ThinkCentre M715q\nboard=LENOVO 30FD\nbios=M11KT45A 08/09/2018\n" +
	`disk=NAME="sda" SIZE="120034123776" ROTA="0" TRAN="sata" MODEL="INTEL SSDSC2BB12"` + "\n" +
	`disk=NAME="sdb" SIZE="0" ROTA="1" TRAN="usb" MODEL="SD/MMC/MS PRO"` + "\n" +
	`disk=NAME="nvme0n1" SIZE="512110190592" ROTA="0" TRAN="nvme" MODEL="SAMSUNG MZVLB512HBJQ-000L7"` + "\n" +
	`disk=NAME="zram0" SIZE="4294967296" ROTA="0" TRAN="" MODEL=""` + "\n" +
	"nic=enp2s0f0 6c:4b:90:59:4e:78\nnic=wlp1s0 AC:2B:6E:BB:9F:1D\nnic=bad 00:00:00:00:00:00\nnic=nomac\n"

func TestParsePosixProbeHardware(t *testing.T) {
	p := ParsePosixProbe(hardwareFixture)
	if p.HWVendor != "LENOVO" || p.HWModel != "10M2S08Y00" || p.HWVersion != "ThinkCentre M715q" || p.Board != "LENOVO 30FD" || p.BIOS != "M11KT45A 08/09/2018" {
		t.Fatalf("identity = %+v", p)
	}
	if len(p.Disks) != 2 || p.Disks[0].Name != "sda" || p.Disks[0].Size != 120034123776 || p.Disks[0].Rotational == nil || *p.Disks[0].Rotational ||
		p.Disks[0].Transport != "sata" || p.Disks[0].Model != "INTEL SSDSC2BB12" || p.Disks[1].Name != "nvme0n1" {
		t.Fatalf("disks = %+v", p.Disks)
	}
	if len(p.NICs) != 2 || p.NICs[0] != (ProbedNIC{Name: "enp2s0f0", MAC: "6c:4b:90:59:4e:78"}) || p.NICs[1].MAC != "ac:2b:6e:bb:9f:1d" {
		t.Fatalf("nics = %+v", p.NICs)
	}
	q := ParsePosixProbe("hw_vendor=To Be Filled By O.E.M.\nhw_model=Default string\nboard= \nbios=sk-abcdefghijklmnop1234\n" +
		`disk=NAME="../etc" SIZE="10" ROTA="0" TRAN="sata" MODEL="x"` + "\n")
	if q.HWVendor != "" || q.HWModel != "" || q.Board != "" || q.BIOS != "" || len(q.Disks) != 0 {
		t.Fatalf("placeholders and bad values kept: %+v", q)
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

func TestParseUnitStatesAndPosixUnits(t *testing.T) {
	systemdOut := "" +
		"agent-os-outpost.service loaded active running Outpost daemon\n" +
		"broken-worker.service loaded failed failed Broken worker\n" +
		"idle-job.service loaded inactive dead Idle job\n" +
		"sk_live_abcdefgh12345678.service loaded active running Secret shaped\n"
	got := ParseUnitStates(systemdOut)
	if got["agent-os-outpost.service"] != "running" || got["agent-os-outpost"] != "running" {
		t.Fatalf("expected agent-os-outpost running, got %+v", got)
	}
	if got["broken-worker.service"] != "failed" || got["broken-worker"] != "failed" {
		t.Fatalf("expected broken-worker failed, got %+v", got)
	}
	if got["idle-job.service"] != "stopped" || got["idle-job"] != "stopped" {
		t.Fatalf("expected idle-job stopped, got %+v", got)
	}
	for k := range got {
		if strings.Contains(k, "sk_live") {
			t.Fatalf("secret-shaped unit leaked: %q", k)
		}
	}

	launchdOut := "" +
		"PID\tStatus\tLabel\n" +
		"412\t0\tio.tether.outpost\n" +
		"-\t0\tcom.example.stopped\n" +
		"-\t78\tcom.example.crashed\n"
	lgot := ParseUnitStates(launchdOut)
	if lgot["io.tether.outpost"] != "running" || lgot["com.example.stopped"] != "stopped" || lgot["com.example.crashed"] != "failed" {
		t.Fatalf("unexpected launchd units: %+v", lgot)
	}

	probe := ParsePosixProbe(linuxFixture + "unit_agent-os-outpost.service=active\nunit_sk_live_abcdefgh12345678=active\n")
	if probe.Units["agent-os-outpost.service"] != "running" || probe.Units["agent-os-outpost"] != "running" {
		t.Fatalf("expected unit from posix probe, got %+v", probe.Units)
	}
	if len(probe.Units) != 2 {
		t.Fatalf("expected only allow-listed unit entries, got %+v", probe.Units)
	}
}
