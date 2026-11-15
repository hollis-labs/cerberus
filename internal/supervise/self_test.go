package supervise

import (
	"io"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/settings"
)

func TestUnitFromCgroup(t *testing.T) {
	for name, c := range map[string]struct{ cgroup, want string }{
		"v2 user service":   {"0::/user.slice/user-1000.slice/user@1000.service/app.slice/cerberus-substrate.service\n", "cerberus-substrate.service"},
		"v2 prefixed":       {"0::/user.slice/user-1000.slice/user@1000.service/app.slice/com.hollis-labs.cerberus.cerberus.cerberus-substrate.service\n", "com.hollis-labs.cerberus.cerberus.cerberus-substrate.service"},
		"v2 shell":          {"0::/user.slice/user-1000.slice/session-84.scope\n", ""},
		"v2 manager itself": {"0::/user.slice/user-1000.slice/user@1000.service/init.scope\n", "user@1000.service"},
		"v1 name=systemd":   {"12:cpu,cpuacct:/\n1:name=systemd:/user.slice/user-1000.slice/user@1000.service/app.slice/x.service\n", "x.service"},
		"root":              {"0::/\n", ""},
		"empty":             {"", ""},
	} {
		got, err := UnitFromCgroup(strings.NewReader(c.cgroup))
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", name, got, err, c.want)
		}
	}
}

func selfResources() []config.ResourceDef {
	return []config.ResourceDef{
		process("cerberus-substrate", "cerberus", map[string]any{"mode": "os_service", "supervisor": "systemd_user", "command": []any{"/bin/true"}}),
		process("named", "x", map[string]any{"mode": "os_service", "supervisor": "systemd_user", "service_name": "cerberus-new", "command": []any{"/bin/true"}}),
	}
}

func cg(unit string) io.Reader {
	return strings.NewReader("0::/user.slice/user-1000.slice/user@1000.service/app.slice/" + unit + "\n")
}

// The start knows which unit is itself: the cgroup's unit must be the one
// self_resource_id installs as.
func TestCheckSelf(t *testing.T) {
	res := selfResources()
	const home = "/u/op"
	check, err := CheckSelf(cg("com.hollis-labs.cerberus.cerberus.cerberus-substrate.service"), "cerberus-substrate", res, home)
	if err != nil || check.Unit != check.Expected || check.Expected != "com.hollis-labs.cerberus.cerberus.cerberus-substrate.service" {
		t.Fatalf("match: %+v %v", check, err)
	}
	if _, err := CheckSelf(cg("cerberus-new.service"), "named", res, home); err != nil {
		t.Fatalf("service_name match: %v", err)
	}
	if _, err := CheckSelf(cg("tachyon.service"), "cerberus-substrate", res, home); err == nil || !strings.Contains(err.Error(), "tachyon.service") {
		t.Fatalf("another unit was accepted: %v", err)
	}
	if _, err := CheckSelf(cg("x.service"), "ghost", res, home); err == nil || !strings.Contains(err.Error(), "lists no such resource") {
		t.Fatalf("an unknown self id: %v", err)
	}
	if check, err := CheckSelf(strings.NewReader("0::/user.slice/user-1000.slice/session-2.scope\n"), "cerberus-substrate", res, home); err != nil || check.Unit != "" {
		t.Fatalf("outside a unit: %+v %v", check, err)
	}
	if check, err := CheckSelf(cg("anything.service"), "", res, home); err != nil || check.Expected != "" {
		t.Fatalf("no self id checks nothing: %+v %v", check, err)
	}
}

// On Linux the process's own cgroup reads and parses.
func TestCheckSelfOnThisHost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cgroups are Linux only")
	}
	f, err := OpenCgroup()
	if err != nil || f == nil {
		t.Fatalf("open %s: %v", CgroupPath, err)
	}
	defer func() { _ = f.Close() }()
	unit, err := UnitFromCgroup(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("this test runs in unit %q", unit)
	if _, err := os.Stat(CgroupPath); err != nil {
		t.Fatal(err)
	}
}

// Outside any service unit, observe mode starts and supervise mode refuses, naming the unit.
func TestSuperviseRefusesToRunOutsideItsUnit(t *testing.T) {
	res := selfResources()
	check, err := CheckSelf(strings.NewReader("0::/user.slice/user-1000.slice/session-2.scope\n"), "cerberus-substrate", res, "/u/op")
	if err != nil || check.Unit != "" {
		t.Fatalf("outside a unit: %+v %v", check, err)
	}
	if err = check.OutsideUnit(settings.ModeObserve, "cerberus-substrate"); err != nil {
		t.Fatalf("observe refused: %v", err)
	}
	err = check.OutsideUnit(settings.ModeSupervise, "cerberus-substrate")
	if err == nil || !strings.Contains(err.Error(), "outside its systemd unit") || !strings.Contains(err.Error(), check.Expected) || !strings.Contains(err.Error(), "observe") {
		t.Fatalf("supervise outside its unit: %v", err)
	}
	inUnit, err := CheckSelf(cg(check.Expected), "cerberus-substrate", res, "/u/op")
	if err != nil || inUnit.OutsideUnit(settings.ModeSupervise, "cerberus-substrate") != nil {
		t.Fatalf("supervise in its unit: %+v %v", inUnit, err)
	}
	if (SelfCheck{}).OutsideUnit(settings.ModeSupervise, "") != nil {
		t.Fatal("no self id refused")
	}
}
