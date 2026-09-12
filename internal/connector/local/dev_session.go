package local

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/service"
)

type devSession struct {
	mu      sync.Mutex
	id      string
	name    string
	spec    ProcessSpec
	status  domain.State
	pid     int
	uptime  time.Time
	errMsg  string
	logPath string
	exited  chan struct{}
}

func newDevSession(res *domain.Resource, spec ProcessSpec) *devSession {
	return &devSession{
		id:     res.ID,
		name:   res.Name,
		spec:   spec,
		status: domain.StateStopped,
	}
}

func (s *devSession) Update(res *domain.Resource, spec ProcessSpec) {
	s.name = res.Name
	s.spec = spec
}

func (s *devSession) Start() error {
	lock, err := service.AcquireLock(s.id)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	s.Poll()
	if s.status == domain.StateUnknown {
		return errors.New(s.errMsg)
	}
	if isActiveState(s.status) {
		return fmt.Errorf("already running (pid %d)", s.pid)
	}

	if len(s.spec.Command) == 0 {
		return fmt.Errorf("no command configured")
	}
	if s.spec.Port > 0 {
		conflict, conflictErr := service.CheckPortConflict(s.spec.Port, s.id)
		if conflictErr == nil && conflict != nil {
			if conflict.CerberusManaged && conflict.ManagedServiceID == s.id {
				return fmt.Errorf("already running on port %d (pid %d)", conflict.Port, conflict.PID)
			}
			if conflict.CerberusManaged {
				return fmt.Errorf("port %d in use by Cerberus service %q (pid %d)",
					conflict.Port, conflict.ManagedServiceID, conflict.PID)
			}
			return fmt.Errorf("port %d in use by external process %q (pid %d)",
				conflict.Port, conflict.ProcessName, conflict.PID)
		}
	}

	// Launch the declared dev-session command directly from the resource spec.
	cmd := exec.Command(s.spec.Command[0], s.spec.Command[1:]...) //nolint:gosec // command comes from trusted local Cerberus config
	cmd.Dir = s.spec.Dir
	cmd.Env = sessionEnv(s.spec)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	s.logPath = DevSessionLogPath(s.id, s.spec)
	if dir := filepath.Dir(s.logPath); dir != "" {
		_ = os.MkdirAll(dir, 0750)
	}
	logFile, err := os.Create(s.logPath)
	if err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		s.errMsg = err.Error()
		return err
	}

	s.status = domain.StateStarting
	s.uptime = time.Now()
	s.errMsg = ""
	if cmd.Process != nil {
		s.pid = cmd.Process.Pid
		_ = service.WritePIDFile(s.id, s.pid)
		_ = service.WriteMetaFile(s.id, service.PIDMeta{
			PID:             s.pid,
			StartedAt:       s.uptime,
			ConfigHash:      sessionConfigHash(s.id, s.spec),
			CerberusVersion: service.CerberusVersion,
			ProcessStart:    processStartIdentity(s.pid),
		})
	}

	s.exited = make(chan struct{})
	exited := s.exited
	id, pid := s.id, s.pid
	go func() {
		_ = cmd.Wait()
		if logFile != nil {
			_ = logFile.Close()
		}
		if current, readErr := service.ReadPIDFile(id); readErr == nil && current == pid {
			_ = service.RemovePIDFile(id)
		}
		close(exited)
	}()

	return nil
}

// Stop waits for the owned process to exit before a caller can restart it.
// A listening port is evidence of occupancy, never evidence of ownership.
func (s *devSession) Stop() error {
	return s.stopContext(context.Background())
}

func (s *devSession) stopContext(ctx context.Context) error {
	lock, err := service.AcquireLock(s.id)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	pid, ownershipErr := s.ownedPID()
	if ownershipErr != nil {
		return ownershipErr
	}
	if pid == 0 {
		if portPID := findPIDByPort(s.spec.Port); portPID != 0 {
			return s.foreignPortError(portPID)
		}
		s.status, s.pid = domain.StateStopped, 0
		_ = service.RemovePIDFile(s.id)
		return nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	group, err := syscall.Getpgid(pid)
	if err != nil {
		return fmt.Errorf("verify process group for %d: %w", pid, err)
	}
	if group != pid {
		return fmt.Errorf("refusing to stop pid %d: it is not the recorded session's process-group leader", pid)
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop process group %d: %w", pid, err)
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	escalated := false
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for processAlive(pid) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if escalated {
				return fmt.Errorf("process %d did not exit after SIGKILL", pid)
			}
			escalated = true
			// Recheck ownership before escalating; never leave a delayed kill
			// behind that could target a later process reusing this PID.
			owned, checkErr := s.ownedPID()
			if checkErr != nil {
				return checkErr
			}
			if owned != pid {
				return fmt.Errorf("process %d ownership changed while stopping", pid)
			}
			if killErr := syscall.Kill(-pid, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
				return killErr
			}
			timer.Reset(time.Second)
		case <-ticker.C:
		}
	}
	if s.exited != nil {
		<-s.exited
	}
	s.status, s.pid = domain.StateStopped, 0
	_ = service.RemovePIDFile(s.id)
	return nil
}

func (s *devSession) Reload() error {
	if err := s.Stop(); err != nil {
		return err
	}
	return s.Start()
}

func (s *devSession) ownedPID() (int, error) {
	if s.pid > 0 && s.exited != nil {
		select {
		case <-s.exited:
			return 0, nil
		default:
			if processAlive(s.pid) {
				return s.pid, nil
			}
		}
	}
	pid, alive := service.ValidatePIDFile(s.id)
	if !alive {
		return 0, nil
	}
	meta, err := service.ReadMetaFile(s.id)
	if err != nil || meta.PID != pid || meta.StartedAt.IsZero() {
		return 0, fmt.Errorf("refusing to adopt pid %d for %q: no matching launch metadata", pid, s.id)
	}
	identity := processStartIdentity(pid)
	if identity == "" {
		return 0, fmt.Errorf("cannot verify launch identity of pid %d for %q", pid, s.id)
	}
	if meta.ProcessStart != "" {
		if identity == meta.ProcessStart {
			return pid, nil
		}
	} else {
		// Older Cerberus launches recorded wall time but no ps identity.
		// Allow those only when the OS start time agrees with that launch.
		started, parseErr := time.ParseInLocation("Mon Jan 2 15:04:05 2006", identity, time.Local)
		if parseErr == nil && meta.StartedAt.Sub(started) >= 0 && meta.StartedAt.Sub(started) < 5*time.Second {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("refusing to adopt pid %d for %q: process identity differs from the recorded launch", pid, s.id)
}

func processStartIdentity(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output() //nolint:gosec // numeric PID only; no command or environment is read
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(out)), " ")
}

func (s *devSession) foreignPortError(pid int) error {
	return fmt.Errorf("port %d is occupied by an unowned process (pid %d); refusing to adopt or stop it for resource %q", s.spec.Port, pid, s.id)
}

func (s *devSession) Poll() domain.State {
	if s.status == domain.StateBuilding {
		return s.status
	}
	pid, err := s.ownedPID()
	if err != nil {
		s.status, s.errMsg = domain.StateUnknown, err.Error()
		return s.status
	}
	if pid > 0 {
		s.pid, s.status, s.errMsg = pid, domain.StateRunning, ""
		return s.status
	}
	if portPID := findPIDByPort(s.spec.Port); portPID != 0 {
		s.status, s.errMsg, s.pid = domain.StateUnknown, s.foreignPortError(portPID).Error(), 0
		return s.status
	}
	s.status, s.pid = domain.StateStopped, 0
	_ = service.RemovePIDFile(s.id)
	return s.status
}

func (s *devSession) BuildSync() (string, error) {
	return BuildProcess(s.spec)
}

func (s *devSession) LogPath() string {
	return DevSessionLogPath(s.id, s.spec)
}

func (s *devSession) tailLog() string {
	if s.logPath == "" {
		return "process exited"
	}
	data, err := os.ReadFile(s.logPath) //nolint:gosec // log path is derived from resource config/runtime state
	if err != nil || len(data) == 0 {
		return "process exited (no log)"
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if len(line) > 80 {
			line = line[:77] + "..."
		}
		return line
	}
	return "process exited"
}

func DevSessionLogPath(id string, spec ProcessSpec) string {
	if spec.LogFile != "" {
		return spec.LogFile
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("cerberus-%s.log", id))
}

func sessionEnv(spec ProcessSpec) []string {
	env := os.Environ()
	env = ensurePath(env)
	if spec.EnvFile != "" {
		envPath := spec.EnvFile
		if !strings.HasPrefix(envPath, "/") {
			envPath = filepath.Join(spec.Dir, envPath)
		}
		env = append(env, loadEnvFile(envPath)...)
	}
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	return env
}

func sessionConfigHash(id string, spec ProcessSpec) string {
	return service.ConfigHash(config.ServiceDef{
		ID:      id,
		Dir:     spec.Dir,
		Command: append([]string(nil), spec.Command...),
		EnvFile: spec.EnvFile,
		Env:     cloneStringMap(spec.Env),
		URL:     spec.URL,
		Port:    spec.Port,
		Health:  spec.Health,
		HealthCheckCfg: config.HealthCheck{
			URL:      spec.HealthCheck.URL,
			Command:  append([]string(nil), spec.HealthCheck.Command...),
			Interval: spec.HealthCheck.Interval,
			Timeout:  spec.HealthCheck.Timeout,
		},
		AutoStart:          spec.AutoStart,
		AutoRestart:        spec.AutoRestart,
		RestartDelay:       spec.RestartDelay,
		MaxRestartAttempts: spec.MaxRestartAttempts,
		RestartCooldown:    spec.RestartCooldown,
		LogFile:            spec.LogFile,
		Profiles:           append([]string(nil), spec.Profiles...),
		Protected:          spec.Protected,
	})
}

func findPIDByPort(port int) int {
	if port <= 0 {
		return 0
	}
	out, err := exec.Command("lsof", "-nP", "-iTCP:"+fmt.Sprintf("%d", port), "-sTCP:LISTEN", "-t").Output() //nolint:gosec
	if err != nil {
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return -1
		}
		return 0
	}
	lines := strings.TrimSpace(string(out))
	if lines == "" {
		return 0
	}
	first := strings.Split(lines, "\n")[0]
	pid, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil {
		return 0
	}
	return pid
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func ensurePath(env []string) []string {
	extra := []string{
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/usr/local/bin",
		"/usr/local/go/bin",
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		extra = append(extra, filepath.Join(home, "go", "bin"))
	}
	for i, entry := range env {
		if !strings.HasPrefix(entry, "PATH=") {
			continue
		}
		current := entry[5:]
		for _, dir := range extra {
			if strings.Contains(current, dir) {
				continue
			}
			if _, err := os.Stat(dir); err == nil {
				current += ":" + dir
			}
		}
		env[i] = "PATH=" + current
		return env
	}
	return env
}

func loadEnvFile(path string) []string {
	data, err := os.ReadFile(path) //nolint:gosec // env file path comes from local config
	if err != nil {
		return nil
	}
	var envs []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "=") {
			envs = append(envs, line)
		}
	}
	return envs
}
