package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OrphanProcess describes a process that matches a known service command but
// is not tracked by a Cerberus PID file. These are candidates for orphaned
// processes from prior Cerberus runs.
type OrphanProcess struct {
	ServiceID   string
	PID         int
	Port        int
	ProcessName string
}

// String returns a human-readable description of the orphan.
func (o OrphanProcess) String() string {
	if o.Port > 0 {
		return fmt.Sprintf("service=%s pid=%d port=%d process=%s", o.ServiceID, o.PID, o.Port, o.ProcessName)
	}
	return fmt.Sprintf("service=%s pid=%d process=%s", o.ServiceID, o.PID, o.ProcessName)
}

// DetectOrphans scans for processes that match known service definitions but
// are not tracked by Cerberus PID files. This is intended to be called on
// daemon startup. It only detects — it never kills processes.
//
// Detection strategy:
//   - For each service with a port, check if something is listening on that port.
//   - If a process is found on the port but no valid PID file exists for that
//     service, it is flagged as a potential orphan.
//   - Only port-based services can be detected this way. Services without ports
//     that have stale PID files are cleaned up by CleanStalePIDFiles instead.
func DetectOrphans(services []*Service) []OrphanProcess {
	var orphans []OrphanProcess

	// Build a set of PIDs that Cerberus is currently tracking.
	trackedPIDs := loadTrackedPIDs()

	for _, svc := range services {
		if svc.Def.Port <= 0 {
			continue
		}

		pid := findPIDByPort(svc.Def.Port)
		if pid <= 0 {
			continue
		}

		// If this PID is already tracked by a Cerberus PID file, it is not
		// an orphan — it is a managed process.
		if trackedPIDs[pid] {
			continue
		}

		// A process is on this service's port but Cerberus has no PID file
		// for it. This is a potential orphan.
		name := processName(pid)
		orphans = append(orphans, OrphanProcess{
			ServiceID:   svc.Def.ID,
			PID:         pid,
			Port:        svc.Def.Port,
			ProcessName: name,
		})
	}

	return orphans
}

// loadTrackedPIDs reads all PID files in ~/.cerberus/pids/ and returns a set
// of PIDs that are currently tracked and alive.
func loadTrackedPIDs() map[int]bool {
	tracked := make(map[int]bool)

	dir, err := pidDir()
	if err != nil {
		return tracked
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return tracked
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pid") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		pid, err := readPIDFrom(path)
		if err != nil {
			continue
		}
		if processAlive(pid) {
			tracked[pid] = true
		}
	}

	return tracked
}
