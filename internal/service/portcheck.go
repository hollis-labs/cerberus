package service

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// PortConflict describes a port that is already in use.
type PortConflict struct {
	Port             int
	PID              int
	ProcessName      string
	CerberusManaged  bool
	ManagedServiceID string
}

// String returns a human-readable description of the conflict.
func (pc PortConflict) String() string {
	if pc.CerberusManaged {
		return fmt.Sprintf("port %d in use by Cerberus service %q (pid %d, %s)",
			pc.Port, pc.ManagedServiceID, pc.PID, pc.ProcessName)
	}
	return fmt.Sprintf("port %d in use by external process %q (pid %d)",
		pc.Port, pc.ProcessName, pc.PID)
}

// CheckPortConflict checks whether a port is in use and returns conflict info.
// serviceID is the ID of the service that wants to use this port (used for
// context in the result). Returns nil if the port is free.
func CheckPortConflict(port int, serviceID string) (*PortConflict, error) {
	pid := findPIDByPort(port)
	if pid <= 0 {
		return nil, nil
	}

	procName := processName(pid)

	conflict := &PortConflict{
		Port:        port,
		PID:         pid,
		ProcessName: procName,
	}

	// Check if this PID belongs to a Cerberus-managed service by looking at
	// PID files. Gracefully handle the case where PID file support is not yet
	// integrated — if readPIDFile is unavailable we just skip.
	managedID := checkPIDFiles(pid)
	if managedID != "" {
		conflict.CerberusManaged = true
		conflict.ManagedServiceID = managedID
	}

	return conflict, nil
}

// ScanAllPorts checks every service's declared port for conflicts and returns
// a slice of all detected conflicts.
func ScanAllPorts(services []*Service) []PortConflict {
	var conflicts []PortConflict
	for _, svc := range services {
		if svc.Def.Port == 0 {
			continue
		}
		conflict, err := CheckPortConflict(svc.Def.Port, svc.Def.ID)
		if err != nil {
			continue
		}
		if conflict != nil {
			conflicts = append(conflicts, *conflict)
		}
	}
	return conflicts
}

// processName returns the command name for a given PID using ps.
func processName(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return "unknown"
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return "unknown"
	}
	return name
}

// checkPIDFiles scans the Cerberus PID directory (if it exists) to see if a
// given PID matches a managed service. Returns the service ID or empty string.
// This gracefully handles the case where PID file support (TASK-042) is not
// yet integrated.
func checkPIDFiles(pid int) string {
	// PID files are expected at /tmp/cerberus-<serviceID>.pid
	// containing just the PID number.
	matches, _ := exec.Command("sh", "-c",
		`for f in /tmp/cerberus-*.pid; do [ -f "$f" ] && echo "$f $(cat "$f")"; done`).Output()
	if len(matches) == 0 {
		return ""
	}

	pidStr := strconv.Itoa(pid)
	for _, line := range strings.Split(strings.TrimSpace(string(matches)), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		filePath := parts[0]
		filePID := strings.TrimSpace(parts[1])
		if filePID == pidStr {
			// Extract service ID from /tmp/cerberus-<id>.pid
			base := filePath
			base = strings.TrimPrefix(base, "/tmp/cerberus-")
			base = strings.TrimSuffix(base, ".pid")
			return base
		}
	}
	return ""
}
