package local

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/service"
)

type devSession struct {
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
	if s.status == domain.StateRunning || s.status == domain.StateHealthy || s.status == domain.StateUnhealthy {
		return fmt.Errorf("already running (pid %d)", s.pid)
	}

	lock, err := service.AcquireLock(s.id)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

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
		})
	}

	s.exited = make(chan struct{})
	go func() {
		_ = cmd.Wait()
		if logFile != nil {
			_ = logFile.Close()
		}
		_ = service.RemovePIDFile(s.id)
		close(s.exited)
	}()

	return nil
}

func (s *devSession) Stop() error {
	lock, err := service.AcquireLock(s.id)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	var pid int
	useGroup := false

	if pidFromFile, alive := service.ValidatePIDFile(s.id); alive {
		pid = pidFromFile
		useGroup = true
	} else {
		portPID := findPIDByPort(s.spec.Port)
		if portPID <= 0 {
			s.status = domain.StateStopped
			s.pid = 0
			_ = service.RemovePIDFile(s.id)
			return nil
		}
		pid = portPID
	}

	if useGroup {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	} else if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Signal(syscall.SIGTERM)
	}

	go func(pid int, useGroup bool) {
		time.Sleep(10 * time.Second)
		if !processAlive(pid) {
			return
		}
		if useGroup {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			return
		}
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
	}(pid, useGroup)

	s.status = domain.StateStopped
	s.pid = 0
	_ = service.RemovePIDFile(s.id)
	return nil
}

func (s *devSession) Reload() error {
	_ = s.Stop()
	return s.Start()
}

func (s *devSession) Poll() domain.State {
	if s.status == domain.StateBuilding {
		return s.status
	}

	if pidFromFile, alive := service.ValidatePIDFile(s.id); alive {
		s.pid = pidFromFile
		if s.status != domain.StateRunning && s.status != domain.StateHealthy && s.status != domain.StateUnhealthy {
			s.status = domain.StateRunning
			s.uptime = time.Now()
		}
		s.errMsg = ""
		return s.status
	}

	if s.spec.Port <= 0 {
		if s.status == domain.StateStarting {
			if s.exited != nil {
				select {
				case <-s.exited:
					s.status = domain.StateStopped
					s.errMsg = s.tailLog()
					return s.status
				default:
				}
			}
			if time.Since(s.uptime) > 30*time.Second {
				s.status = domain.StateStopped
				s.errMsg = "start timeout"
			}
			return s.status
		}
		s.status = domain.StateStopped
		s.pid = 0
		_ = service.RemovePIDFile(s.id)
		return s.status
	}

	pid := findPIDByPort(s.spec.Port)
	if pid > 0 && s.status == domain.StateStarting {
		s.pid = pid
		_ = service.WritePIDFile(s.id, pid)
		s.status = domain.StateRunning
		s.uptime = time.Now()
		s.errMsg = ""
		return s.status
	}
	if pid > 0 {
		s.status = domain.StateStopped
		s.pid = 0
		_ = service.RemovePIDFile(s.id)
		return s.status
	}

	if s.status == domain.StateStarting {
		if s.exited != nil {
			select {
			case <-s.exited:
				s.status = domain.StateStopped
				s.errMsg = s.tailLog()
				return s.status
			default:
			}
		}
		if time.Since(s.uptime) > 30*time.Second {
			s.status = domain.StateStopped
			s.errMsg = "start timeout"
		}
		return s.status
	}

	s.status = domain.StateStopped
	s.pid = 0
	_ = service.RemovePIDFile(s.id)
	return s.status
}

func (s *devSession) BuildSync() (string, error) {
	return BuildProcess(s.spec)
}

func BuildProcess(spec ProcessSpec) (string, error) {
	if len(spec.Build) == 0 {
		return "", fmt.Errorf("no build command configured")
	}
	// Run the declared build contract from the resource spec.
	cmd := exec.Command(spec.Build[0], spec.Build[1:]...) //nolint:gosec // command comes from trusted local Cerberus config
	cmd.Dir = spec.Dir
	cmd.Env = sessionEnv(spec)
	out, err := cmd.CombinedOutput()
	return string(out), err
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
		Build:   append([]string(nil), spec.Build...),
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
