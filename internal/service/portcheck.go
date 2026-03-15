package service

import (
	"fmt"
	"os"
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
func ScanAllPorts(services []*ManagedService) []PortConflict {
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

// checkPIDFiles scans the Cerberus PID directory (~/.cerberus/pids/) to see if
// a given PID matches a managed service. Returns the service ID or empty string.
func checkPIDFiles(pid int) string {
	dir, err := pidDir()
	if err != nil {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pid") {
			continue
		}
		serviceID := strings.TrimSuffix(entry.Name(), ".pid")
		filePID, err := ReadPIDFile(serviceID)
		if err != nil {
			continue
		}
		if filePID == pid {
			return serviceID
		}
	}
	return ""
}
