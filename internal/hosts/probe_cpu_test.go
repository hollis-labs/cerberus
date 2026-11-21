package hosts

import (
	"os/exec"
	"strings"
	"testing"
)

// Exercise the actual probe expressions with fixture counters/top output,
// without collecting any host facts or running the platform's top command.
func TestProbeCPUExpressions(t *testing.T) {
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("awk unavailable")
	}
	var expressions []string
	for _, line := range strings.Split(posixProbeScript, "\n") {
		if strings.Contains(line, `echo "cpu_percent=`) {
			expressions = append(expressions, line)
		}
	}
	if len(expressions) != 2 {
		t.Fatalf("CPU expressions: %v", expressions)
	}
	fixtures := []string{
		`top(){ printf '%s\n' 'CPU usage: 1.0% user, 2.0% sys, 97.0% idle' 'CPU usage: 10.0% user, 20.0% sys, 70.0% idle'; };` + expressions[0],
		`before='cpu 10 0 10 80 0 0 0 0 1000 0'; after='cpu 30 0 30 140 0 0 0 0 2000 0'; ` + expressions[1],
	}
	for i, script := range fixtures {
		b, err := exec.Command("sh", "-c", script).CombinedOutput() //nolint:gosec // only fixed probe expressions and fixture literals, no external input
		if err != nil {
			t.Fatalf("fixture %d: %v %s", i, err, b)
		}
		p := ParsePosixProbe(string(b))
		want := []float64{30, 40}[i]
		if p.CPUPercent == nil || *p.CPUPercent != want {
			t.Fatalf("fixture %d: %s", i, b)
		}
	}
}

func TestProbeVersionAndSharedFilesystemMounts(t *testing.T) {
	if !strings.Contains(posixProbeScript, `echo "probe_version=2"`) {
		t.Fatal("probe script must emit probe_version=2")
	}
	// Paths that share one filesystem stay separate entries.
	p := ParsePosixProbe("probe_version=2\nmount_kb=/ 100 50\nmount_kb=/var/tmp 100 50\nmount_kb=/home 100 50\nmount_kb=/home 100 50\n")
	if p.ProbeVersion != 2 || len(p.Mounts) != 3 {
		t.Fatalf("%+v", p)
	}
	if ParsePosixProbe("os=Linux\n").ProbeVersion != 0 {
		t.Fatal("legacy output must have probe_version 0")
	}
}

// The mount loop prints every existing allow-listed path, even ones that share
// a filesystem with /.
func TestProbeScriptMountLoopCoversRequiredPaths(t *testing.T) {
	for _, d := range []string{"/", "/var/tmp", "/home"} {
		if !strings.Contains(posixProbeScript, " "+d+" ") && !strings.Contains(posixProbeScript, "for d in "+d+" ") {
			t.Fatalf("mount loop lacks %s", d)
		}
	}
}
