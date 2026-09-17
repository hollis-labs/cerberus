package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hollis-labs/cerberus/internal/config"
)

// CerberusVersion is the current version of Cerberus, embedded in PID meta files.
const CerberusVersion = "0.2.0"

// PIDMeta stores metadata alongside a PID file.
type PIDMeta struct {
	PID             int       `json:"pid"`
	ProcessStart    string    `json:"process_start,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	ConfigHash      string    `json:"config_hash"`
	CerberusVersion string    `json:"cerberus_version"`
}

// pidDir returns the path to ~/.cerberus/pids/, creating it if needed.
func pidDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	dir := filepath.Join(home, ".cerberus", "pids")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create pid dir: %w", err)
	}
	return dir, nil
}

// pidDirAt returns a PID directory rooted at a custom base (for testing).
func pidDirAt(base string) (string, error) {
	dir := filepath.Join(base, ".cerberus", "pids")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create pid dir: %w", err)
	}
	return dir, nil
}

// WritePIDFile writes the given PID to ~/.cerberus/pids/<serviceID>.pid.
func WritePIDFile(serviceID string, pid int) error {
	dir, err := pidDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, serviceID+".pid")
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0644)
}

// WritePIDFileAt writes a PID file rooted at a custom base directory.
func WritePIDFileAt(base, serviceID string, pid int) error {
	dir, err := pidDirAt(base)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, serviceID+".pid")
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0644)
}

// ReadPIDFile reads the PID from ~/.cerberus/pids/<serviceID>.pid.
func ReadPIDFile(serviceID string) (int, error) {
	dir, err := pidDir()
	if err != nil {
		return 0, err
	}
	return readPIDFrom(filepath.Join(dir, serviceID+".pid"))
}

// ReadPIDFileAt reads a PID file rooted at a custom base directory.
func ReadPIDFileAt(base, serviceID string) (int, error) {
	dir, err := pidDirAt(base)
	if err != nil {
		return 0, err
	}
	return readPIDFrom(filepath.Join(dir, serviceID+".pid"))
}

func readPIDFrom(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read pid file: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse pid: %w", err)
	}
	return pid, nil
}

// RemovePIDFile removes ~/.cerberus/pids/<serviceID>.pid and its .meta companion.
func RemovePIDFile(serviceID string) error {
	dir, err := pidDir()
	if err != nil {
		return err
	}
	return removePIDFrom(dir, serviceID)
}

// RemovePIDFileAt removes PID and meta files rooted at a custom base directory.
func RemovePIDFileAt(base, serviceID string) error {
	dir, err := pidDirAt(base)
	if err != nil {
		return err
	}
	return removePIDFrom(dir, serviceID)
}

func removePIDFrom(dir, serviceID string) error {
	pidPath := filepath.Join(dir, serviceID+".pid")
	metaPath := filepath.Join(dir, serviceID+".meta")
	os.Remove(metaPath) // best-effort
	if err := os.Remove(pidPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove pid file: %w", err)
	}
	return nil
}

// ValidatePIDFile reads the PID file and checks whether the process is alive
// using kill -0. Returns the PID and true if the process exists.
func ValidatePIDFile(serviceID string) (int, bool) {
	pid, err := ReadPIDFile(serviceID)
	if err != nil {
		return 0, false
	}
	return pid, processAlive(pid)
}

// ValidatePIDFileAt validates a PID file rooted at a custom base directory.
func ValidatePIDFileAt(base, serviceID string) (int, bool) {
	pid, err := ReadPIDFileAt(base, serviceID)
	if err != nil {
		return 0, false
	}
	return pid, processAlive(pid)
}

// processAlive checks if a process with the given PID is running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks existence without sending a real signal.
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// CleanStalePIDFiles scans all PID files in ~/.cerberus/pids/ and removes
// those whose processes are no longer running.
func CleanStalePIDFiles() error {
	dir, err := pidDir()
	if err != nil {
		return err
	}
	return cleanStaleIn(dir)
}

// CleanStalePIDFilesAt cleans stale PID files in a custom base directory.
func CleanStalePIDFilesAt(base string) error {
	dir, err := pidDirAt(base)
	if err != nil {
		return err
	}
	return cleanStaleIn(dir)
}

func cleanStaleIn(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read pid dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pid") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		pid, err := readPIDFrom(path)
		if err != nil {
			// Corrupt PID file — remove it.
			os.Remove(path)
			metaName := strings.TrimSuffix(e.Name(), ".pid") + ".meta"
			os.Remove(filepath.Join(dir, metaName))
			continue
		}
		if !processAlive(pid) {
			os.Remove(path)
			metaName := strings.TrimSuffix(e.Name(), ".pid") + ".meta"
			os.Remove(filepath.Join(dir, metaName))
		}
	}
	return nil
}

// WriteMetaFile writes a JSON meta file alongside the PID file.
func WriteMetaFile(serviceID string, meta PIDMeta) error {
	dir, err := pidDir()
	if err != nil {
		return err
	}
	return writeMetaTo(dir, serviceID, meta)
}

// WriteMetaFileAt writes a meta file rooted at a custom base directory.
func WriteMetaFileAt(base, serviceID string, meta PIDMeta) error {
	dir, err := pidDirAt(base)
	if err != nil {
		return err
	}
	return writeMetaTo(dir, serviceID, meta)
}

func writeMetaTo(dir, serviceID string, meta PIDMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	path := filepath.Join(dir, serviceID+".meta")
	return os.WriteFile(path, data, 0644)
}

// ReadMetaFile reads the JSON meta file for a service.
func ReadMetaFile(serviceID string) (PIDMeta, error) {
	dir, err := pidDir()
	if err != nil {
		return PIDMeta{}, err
	}
	return readMetaFrom(filepath.Join(dir, serviceID+".meta"))
}

// ReadMetaFileAt reads a meta file rooted at a custom base directory.
func ReadMetaFileAt(base, serviceID string) (PIDMeta, error) {
	dir, err := pidDirAt(base)
	if err != nil {
		return PIDMeta{}, err
	}
	return readMetaFrom(filepath.Join(dir, serviceID+".meta"))
}

func readMetaFrom(path string) (PIDMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PIDMeta{}, fmt.Errorf("read meta file: %w", err)
	}
	var meta PIDMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return PIDMeta{}, fmt.Errorf("parse meta: %w", err)
	}
	return meta, nil
}

// ConfigHash computes a stable SHA256 hash of a ServiceDef's key fields.
func ConfigHash(def config.ServiceDef) string {
	h := sha256.New()
	// Hash deterministic fields that affect runtime behavior
	fmt.Fprintf(h, "id:%s\n", def.ID)
	fmt.Fprintf(h, "dir:%s\n", def.Dir)
	fmt.Fprintf(h, "port:%d\n", def.Port)
	fmt.Fprintf(h, "command:%s\n", strings.Join(def.Command, " "))
	fmt.Fprintf(h, "envfile:%s\n", def.EnvFile)
	// Sort env keys for stability
	envKeys := make([]string, 0, len(def.Env))
	for k := range def.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		fmt.Fprintf(h, "env:%s=%s\n", k, def.Env[k])
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
