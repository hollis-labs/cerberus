package service

import (
	"os"
	"os/exec"
)

// DoctorResult captures one diagnostic check for a service.
type DoctorResult struct {
	ServiceID string
	Check     string
	Status    string // "ok", "warn", "error"
	Message   string
}

// RunDoctor performs health checks across all services and returns results.
// Checks include: port conflicts, missing binaries, missing working directories.
func RunDoctor(services []*ManagedService) []DoctorResult {
	var results []DoctorResult

	for _, svc := range services {
		// Check 1: port conflict
		results = append(results, checkPort(svc)...)

		// Check 2: missing binary (command[0])
		results = append(results, checkBinary(svc))

		// Check 3: missing working directory
		results = append(results, checkDir(svc))
	}

	return results
}

func checkPort(svc *ManagedService) []DoctorResult {
	if svc.Def.Port == 0 {
		return []DoctorResult{{
			ServiceID: svc.Def.ID,
			Check:     "port",
			Status:    "warn",
			Message:   "no port configured",
		}}
	}

	conflict, err := CheckPortConflict(svc.Def.Port, svc.Def.ID)
	if err != nil {
		return []DoctorResult{{
			ServiceID: svc.Def.ID,
			Check:     "port",
			Status:    "warn",
			Message:   "could not check port: " + err.Error(),
		}}
	}

	if conflict != nil {
		return []DoctorResult{{
			ServiceID: svc.Def.ID,
			Check:     "port",
			Status:    "error",
			Message:   conflict.String(),
		}}
	}

	return []DoctorResult{{
		ServiceID: svc.Def.ID,
		Check:     "port",
		Status:    "ok",
		Message:   "port available",
	}}
}

func checkBinary(svc *ManagedService) DoctorResult {
	if len(svc.Def.Command) == 0 {
		return DoctorResult{
			ServiceID: svc.Def.ID,
			Check:     "binary",
			Status:    "error",
			Message:   "no command configured",
		}
	}

	bin := svc.Def.Command[0]

	// If the binary is a relative or absolute path, check it relative to the
	// service working directory.
	if bin[0] == '.' || bin[0] == '/' {
		path := bin
		if bin[0] == '.' && svc.Def.Dir != "" {
			path = svc.Def.Dir + "/" + bin
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return DoctorResult{
				ServiceID: svc.Def.ID,
				Check:     "binary",
				Status:    "error",
				Message:   "binary not found: " + path,
			}
		}
	} else {
		// It's on PATH — check with which/LookPath
		if _, err := exec.LookPath(bin); err != nil {
			return DoctorResult{
				ServiceID: svc.Def.ID,
				Check:     "binary",
				Status:    "error",
				Message:   "binary not in PATH: " + bin,
			}
		}
	}

	return DoctorResult{
		ServiceID: svc.Def.ID,
		Check:     "binary",
		Status:    "ok",
		Message:   "binary found",
	}
}

func checkDir(svc *ManagedService) DoctorResult {
	if svc.Def.Dir == "" {
		return DoctorResult{
			ServiceID: svc.Def.ID,
			Check:     "directory",
			Status:    "warn",
			Message:   "no working directory configured",
		}
	}

	info, err := os.Stat(svc.Def.Dir)
	if os.IsNotExist(err) {
		return DoctorResult{
			ServiceID: svc.Def.ID,
			Check:     "directory",
			Status:    "error",
			Message:   "directory not found: " + svc.Def.Dir,
		}
	}

	if err != nil {
		return DoctorResult{
			ServiceID: svc.Def.ID,
			Check:     "directory",
			Status:    "warn",
			Message:   "cannot access directory: " + err.Error(),
		}
	}

	if !info.IsDir() {
		return DoctorResult{
			ServiceID: svc.Def.ID,
			Check:     "directory",
			Status:    "error",
			Message:   "path is not a directory: " + svc.Def.Dir,
		}
	}

	return DoctorResult{
		ServiceID: svc.Def.ID,
		Check:     "directory",
		Status:    "ok",
		Message:   "directory exists",
	}
}
