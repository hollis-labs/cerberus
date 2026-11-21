package hosts_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
)

func TestFleetProbeParsesOnlyBoundedMetrics(t *testing.T) {
	p := hosts.ParsePosixProbe("cpu=16\ncpu_percent=0\nmount_kb=/var/tmp 1000 0\nmount_kb=/ 2000 100\nmount_kb=/private/secret 1000 500\nmount_kb=/var/tmp 10 9\n" + strings.Repeat("mount_kb=/ 2000 100\n", 1000))
	if p.CPUPercent == nil || *p.CPUPercent != 0 || p.CPU != 16 || len(p.Mounts) != 2 || p.Mounts[0].Used != 0 || p.Mounts[1].Used != 102400 {
		t.Fatalf("%+v", p)
	}
	x := hosts.SampleFrom("box", time.Now(), &p, 1)
	if x.CPUPercent == nil || len(x.Mounts) != 2 {
		t.Fatalf("sample lost metrics: %+v", x)
	}
	for _, raw := range []string{"NaN", "+Inf", "-1", "100.01", "token"} {
		if p := hosts.ParsePosixProbe("cpu_percent=" + raw + "\nload=" + raw); p.CPUPercent != nil || (raw != "100.01" && p.Load1 != nil) {
			t.Fatalf("accepted %q: %+v", raw, p)
		}
	}
	for _, raw := range []string{"/ 1 2", "/ -1 0", "/ 9223372036854775807 0", "/ 0 0", "/ token 0"} {
		if p := hosts.ParsePosixProbe("mount_kb=" + raw); len(p.Mounts) != 0 {
			t.Fatalf("accepted %q", raw)
		}
	}
}
