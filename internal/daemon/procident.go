package daemon

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// ProcIdentifier inspects running processes by PID to confirm identity
// (is this PID actually a cerberus daemon?) and to discover stray daemons.
//
// This is an interface so tests can inject deterministic fakes — spawning
// real processes in unit tests is fragile and slow.
type ProcIdentifier interface {
	// IsCerberusDaemon reports whether the given PID's command line identifies
	// it as a cerberus daemon process. Returns (false, nil) for PIDs that
	// don't exist or don't match. Returns an error only for unexpected
	// probe failures.
	IsCerberusDaemon(ctx context.Context, pid int) (bool, error)

	// FindCerberusDaemonPIDs returns all PIDs owned by the current uid whose
	// command lines identify them as cerberus daemon processes. Best-effort:
	// returns the empty slice + nil on probe failure rather than erroring,
	// so callers can always make forward progress.
	FindCerberusDaemonPIDs(ctx context.Context) ([]int, error)
}

// PSIdentifier is the production ProcIdentifier that shells out to `ps`.
// It is portable across macOS and Linux (both ship BSD-compatible `ps`).
type PSIdentifier struct{}

// daemonCmdRegex matches a cerberus daemon command line. The binary basename
// must be either the developer binary ("cerberus") or the installed launchd
// artifact name ("cerberus-daemon-service"), and the first arg must be
// "daemon". This is intentionally strict to avoid matching `cerberus status`,
// `cerberus mcp`, the web service binary, or similarly named processes.
var daemonCmdRegex = regexp.MustCompile(`(?:^|/)(?:cerberus|cerberus-daemon-service)\s+daemon(?:\s|$)`)

// IsCerberusDaemon returns true if the PID's command identifies it as a
// cerberus daemon. Uses `ps -p <pid> -o command=` — the `=` suppresses the
// header, and `command` gives the full argv (same on BSD + GNU ps).
func (PSIdentifier) IsCerberusDaemon(ctx context.Context, pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	// #nosec G204 — args are literal flags; pid is an integer formatted via strconv.
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		// `ps -p <pid>` exits non-zero when the PID is gone. Treat that as
		// "not a cerberus daemon" rather than a probe error.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("ps probe pid %d: %w", pid, err)
	}
	cmd := strings.TrimSpace(string(out))
	if cmd == "" {
		return false, nil
	}
	return daemonCmdRegex.MatchString(cmd), nil
}

// FindCerberusDaemonPIDs lists all processes owned by the current uid and
// returns PIDs whose command lines identify them as cerberus daemons.
func (p PSIdentifier) FindCerberusDaemonPIDs(ctx context.Context) ([]int, error) {
	// `ps -x -o pid=,command=` lists our own processes. Using `-x` with no
	// `-u` picks up the current user's processes only (BSD semantics on
	// macOS; equivalent on Linux procps).
	out, err := exec.CommandContext(ctx, "ps", "-x", "-o", "pid=,command=").Output()
	if err != nil {
		// Probe failure — return nothing rather than erroring so stray-sweep
		// can still proceed (at worst we miss strays; the lock guard catches
		// the next bad start).
		return nil, nil //nolint:nilerr
	}

	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Split into (pid, command) on first whitespace run.
		var pidStr, cmd string
		if idx := strings.IndexFunc(line, func(r rune) bool { return r == ' ' || r == '\t' }); idx > 0 {
			pidStr = line[:idx]
			cmd = strings.TrimSpace(line[idx+1:])
		} else {
			continue
		}
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue
		}
		if daemonCmdRegex.MatchString(cmd) {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}
