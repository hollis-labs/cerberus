package local

import (
	"errors"
	"os/exec"
	"strings"
)

// missingTargetSignal matches GNU make's diagnostic when a probed target is
// absent from the Makefile. BSD make on macOS emits a similar phrase; the
// substring is loose enough to cover both.
const missingTargetSignal = "No rule to make target"

// RunInstall executes `make install` in the spec's working directory after a
// successful build. It first probes the install target with `make -q install`:
// when stderr indicates the target is missing the install is skipped (the
// resource simply does not opt into install staging). All other probe results
// fall through to `make install` itself, whose success/failure is authoritative.
//
// Returns (skipped, combined output, error). On skip both output and error are
// empty so the caller can attribute the no-op cleanly in deploy messaging.
func RunInstall(spec ProcessSpec) (bool, string, error) {
	if spec.Dir == "" {
		return false, "", errors.New("install: spec.Dir is empty")
	}

	probe := exec.Command("make", "-q", "install") //nolint:gosec // make + literal args, no user input
	probe.Dir = spec.Dir
	probe.Env = sessionEnv(spec)
	probeOut, _ := probe.CombinedOutput()
	if strings.Contains(string(probeOut), missingTargetSignal) {
		return true, "", nil
	}

	run := exec.Command("make", "install") //nolint:gosec // make + literal args, no user input
	run.Dir = spec.Dir
	run.Env = sessionEnv(spec)
	out, err := run.CombinedOutput()
	return false, string(out), err
}
