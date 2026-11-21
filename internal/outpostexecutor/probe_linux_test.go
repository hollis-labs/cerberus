//go:build linux

package outpostexecutor

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/pkg/outpost"
)

// Runs the real probe script the plugin serves for the probe verb and parses it.
func TestPluginProbeScriptReportsCPUAndMounts(t *testing.T) {
	command, args, err := Command("linux", os.Getuid(), outpost.Request{Verb: "probe"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, args...).Output() //nolint:gosec // fixed probe command from Command
	if err != nil {
		t.Skipf("probe script cannot run here: %v", err)
	}
	p := hosts.ParsePosixProbe(string(out))
	if p.ProbeVersion < 2 {
		t.Fatalf("probe_version = %d", p.ProbeVersion)
	}
	if p.CPUPercent == nil {
		t.Fatalf("cpu_percent not parsed from:\n%s", out)
	}
	for _, m := range p.Mounts {
		if m.Mount == "/" && m.Total > 0 {
			return
		}
	}
	t.Fatalf("mount / not parsed: %+v", p.Mounts)
}
