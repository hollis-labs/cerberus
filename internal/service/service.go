package service

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chrispian/cerberus/internal/config"
)

type Status int

const (
	StatusStopped Status = iota
	StatusRunning
	StatusStarting
	StatusBuilding
)

func (s Status) String() string {
	switch s {
	case StatusRunning:
		return "running"
	case StatusStarting:
		return "starting"
	case StatusBuilding:
		return "building"
	default:
		return "stopped"
	}
}

type Service struct {
	Def     config.ServiceDef
	Status  Status
	PID     int
	Uptime  time.Time
	Error   string
	logPath    string
	exited     chan struct{} // closed when the process exits
	BuildErr   string
	BuildDone  chan struct{} // closed when build finishes
}

func NewFromConfig(cfg *config.Config) []*Service {
	services := make([]*Service, len(cfg.Services))
	for i, def := range cfg.Services {
		services[i] = &Service{Def: def}
	}
	return services
}

// Poll checks if the service port is in use and updates status/PID.
func (s *Service) Poll() {
	if s.Status == StatusBuilding {
		return
	}
	pid := findPIDByPort(s.Def.Port)
	if pid > 0 {
		s.PID = pid
		if s.Status != StatusRunning {
			s.Status = StatusRunning
			s.Uptime = time.Now()
		}
		s.Error = ""
	} else {
		if s.Status == StatusStarting {
			// Check if process already exited (crashed on start)
			if s.exited != nil {
				select {
				case <-s.exited:
					s.Status = StatusStopped
					s.Error = s.tailLog()
					return
				default:
				}
			}
			// give it a grace period
			if time.Since(s.Uptime) > 30*time.Second {
				s.Status = StatusStopped
				s.Error = "start timeout"
			}
			return
		}
		s.Status = StatusStopped
		s.PID = 0
	}
}

// Start launches the service process in the background.
func (s *Service) Start() error {
	if s.Status == StatusRunning {
		return fmt.Errorf("already running (pid %d)", s.PID)
	}

	if len(s.Def.Command) == 0 {
		return fmt.Errorf("no command configured")
	}

	cmd := exec.Command(s.Def.Command[0], s.Def.Command[1:]...)
	cmd.Dir = s.Def.Dir

	// Build environment: inherit current env, then layer on config
	env := os.Environ()

	// Load env file if specified
	if s.Def.EnvFile != "" {
		envPath := s.Def.EnvFile
		if !strings.HasPrefix(envPath, "/") {
			envPath = s.Def.Dir + "/" + envPath
		}
		env = append(env, loadEnvFile(envPath)...)
	}

	// Apply explicit env vars from config (expand ~ in values)
	home, _ := os.UserHomeDir()
	for k, v := range s.Def.Env {
		if strings.HasPrefix(v, "~/") {
			v = home + v[1:]
		}
		env = append(env, k+"="+v)
	}

	cmd.Env = env

	// Detach process
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Redirect output to log file
	logDir := os.TempDir()
	s.logPath = fmt.Sprintf("%s/cerberus-%s.log", logDir, s.Def.ID)
	logFile, err := os.Create(s.logPath)
	if err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	if err := cmd.Start(); err != nil {
		s.Error = err.Error()
		return err
	}

	s.Status = StatusStarting
	s.Uptime = time.Now()
	s.Error = ""

	// Track process exit so we can detect early crashes
	s.exited = make(chan struct{})
	go func() {
		cmd.Wait()
		if logFile != nil {
			logFile.Close()
		}
		close(s.exited)
	}()

	return nil
}

// Build runs the service's build command synchronously in a goroutine.
func (s *Service) Build() error {
	if len(s.Def.Build) == 0 {
		return fmt.Errorf("no build command configured")
	}
	if s.Status == StatusBuilding {
		return fmt.Errorf("build already in progress")
	}

	prevStatus := s.Status
	s.Status = StatusBuilding
	s.BuildErr = ""
	s.BuildDone = make(chan struct{})

	go func() {
		defer close(s.BuildDone)

		cmd := exec.Command(s.Def.Build[0], s.Def.Build[1:]...)
		cmd.Dir = s.Def.Dir

		// Inherit environment
		env := os.Environ()
		if s.Def.EnvFile != "" {
			envPath := s.Def.EnvFile
			if !strings.HasPrefix(envPath, "/") {
				envPath = s.Def.Dir + "/" + envPath
			}
			env = append(env, loadEnvFile(envPath)...)
		}
		home, _ := os.UserHomeDir()
		for k, v := range s.Def.Env {
			if strings.HasPrefix(v, "~/") {
				v = home + v[1:]
			}
			env = append(env, k+"="+v)
		}
		cmd.Env = env

		out, err := cmd.CombinedOutput()
		if err != nil {
			errMsg := strings.TrimSpace(string(out))
			if errMsg == "" {
				errMsg = err.Error()
			}
			if len(errMsg) > 80 {
				errMsg = errMsg[len(errMsg)-77:] + "..."
			}
			s.BuildErr = errMsg
		}

		// Restore previous status
		if s.Status == StatusBuilding {
			s.Status = prevStatus
		}
	}()

	return nil
}

// Stop sends SIGTERM to the process owning this port.
func (s *Service) Stop() error {
	pid := findPIDByPort(s.Def.Port)
	if pid <= 0 {
		s.Status = StatusStopped
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return err
	}

	// Wait briefly, then force kill if still alive
	go func() {
		time.Sleep(5 * time.Second)
		if p := findPIDByPort(s.Def.Port); p == pid {
			proc.Signal(syscall.SIGKILL)
		}
	}()

	s.Status = StatusStopped
	s.PID = 0
	return nil
}

func findPIDByPort(port int) int {
	// Use lsof to find process listening on the port
	out, err := exec.Command("lsof", "-ti", fmt.Sprintf(":%d", port)).Output()
	if err != nil {
		// Also try a quick TCP connect as fallback
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return -1 // port is open but can't determine PID
		}
		return 0
	}

	lines := strings.TrimSpace(string(out))
	if lines == "" {
		return 0
	}

	// Take the first PID (there may be multiple lines)
	first := strings.Split(lines, "\n")[0]
	pid, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil {
		return 0
	}
	return pid
}

// tailLog reads the last meaningful line from the service log to show as error context.
func (s *Service) tailLog() string {
	if s.logPath == "" {
		return "process exited"
	}
	data, err := os.ReadFile(s.logPath)
	if err != nil || len(data) == 0 {
		return "process exited (no log)"
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	// Walk backwards to find a non-empty line
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			// Truncate for display
			if len(line) > 80 {
				line = line[:77] + "..."
			}
			return line
		}
	}
	return "process exited"
}

func loadEnvFile(path string) []string {
	data, err := os.ReadFile(path)
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
