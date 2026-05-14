package local

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// missingTargetSignal matches GNU make's diagnostic when a probed target is
// absent from the Makefile. BSD make on macOS emits a similar phrase; the
// substring is loose enough to cover both.
const missingTargetSignal = "No rule to make target"

// makefileNames lists the file names `make` looks for when no -f flag is
// passed, in the precedence order GNU make uses (BSD make agrees on the
// first two; GNUmakefile is GNU-only but harmless to probe).
var makefileNames = []string{"GNUmakefile", "makefile", "Makefile"}

// RunInstall executes `make install` in the spec's working directory after a
// successful build. Two short-circuits land before the install runs:
//
//  1. If the working dir has no Makefile at all, install is skipped — the
//     resource doesn't use `make` for its build chain and the install step
//     is feature-absent, not an error.
//  2. If the Makefile exists but lacks an `install` target, probed via
//     `make -q install`, install is likewise skipped.
//
// All other paths fall through to `make install`, whose success/failure is
// authoritative — a failing install_after_build fails the deploy.
//
// Returns (skipped, combined output, error). On skip both output and error
// are empty so the caller can attribute the no-op cleanly in deploy messaging.
func RunInstall(spec ProcessSpec) (bool, string, error) {
	if spec.Dir == "" {
		return false, "", errors.New("install: spec.Dir is empty")
	}

	if !hasMakefile(spec.Dir) {
		return true, "", nil
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

// hasMakefile reports whether `make` invoked in dir would find a default
// makefile. Mirrors make's own discovery so the skip semantics match what
// the user would observe interactively.
func hasMakefile(dir string) bool {
	for _, name := range makefileNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}
