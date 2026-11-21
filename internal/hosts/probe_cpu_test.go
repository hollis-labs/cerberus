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
