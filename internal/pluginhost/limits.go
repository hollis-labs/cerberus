package pluginhost

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Limits bound what one plugin may take from the host (P5-d): how long each
// protocol call may run, how much it may return, and the resources its
// process may use. Defaults apply where connector-config.yaml says nothing;
// what it says is clamped to the host maximums (ClampLimits).
type Limits struct {
	Init   time.Duration
	Load   time.Duration
	Health time.Duration
	Unload time.Duration
	// Call is every operation's deadline; Operations overrides it by name.
	Call       time.Duration
	Operations map[string]time.Duration

	// MaxResultBytes caps an operation's result content (OutputCap).
	MaxResultBytes int
	// MemoryBytes is the memory watchdog's limit on the process group's
	// resident size; 0 turns the watchdog off.
	MemoryBytes int64

	// Process are the rlimits the shim sets before the plugin starts.
	Process ProcessLimits
}

// ProcessLimits are rlimits for a plugin process, where macOS enforces them.
// Memory is not among them: RLIMIT_RSS is ignored, and RLIMIT_AS and
// RLIMIT_DATA are unreliable and break the Go runtime's address reservation.
// The watchdog stands in for that, best effort.
type ProcessLimits struct {
	// OpenFiles is RLIMIT_NOFILE.
	OpenFiles uint64
	// FileBytes is RLIMIT_FSIZE, the largest file the plugin may write.
	FileBytes uint64
	// CPUSeconds is RLIMIT_CPU, a lifetime budget for a long-lived process,
	// not a per-call one; 0, the default, leaves it off. The wall-clock
	// deadline is what bounds one call.
	CPUSeconds uint64
}

// Defaults and host maximums.
var (
	DefaultLimits = Limits{
		Init: 20 * time.Second, Load: 20 * time.Second, Health: 5 * time.Second, Unload: 5 * time.Second,
		Call:           2 * time.Minute,
		MaxResultBytes: 1 << 20,
		MemoryBytes:    2 << 30,
		Process:        ProcessLimits{OpenFiles: 1024, FileBytes: 1 << 30},
	}
	MaxLimits = Limits{
		Init: 2 * time.Minute, Load: 2 * time.Minute, Health: 30 * time.Second, Unload: 30 * time.Second,
		Call:           30 * time.Minute,
		MaxResultBytes: MaxMessageBytes,
		MemoryBytes:    16 << 30,
		Process:        ProcessLimits{OpenFiles: 8192, FileBytes: 16 << 30, CPUSeconds: 24 * 3600},
	}
)

// MaxMessageBytes is the largest protocol message the host reads from a
// plugin. A longer one is unrecoverable: the stream is abandoned and the
// plugin stopped.
const MaxMessageBytes = 16 << 20

// LimitSettings is a plugin's `limits:` in connector-config.yaml.
type LimitSettings struct {
	InitTimeout    time.Duration            `yaml:"init_timeout"`
	LoadTimeout    time.Duration            `yaml:"load_timeout"`
	HealthTimeout  time.Duration            `yaml:"health_timeout"`
	UnloadTimeout  time.Duration            `yaml:"unload_timeout"`
	CallTimeout    time.Duration            `yaml:"call_timeout"`
	Operations     map[string]time.Duration `yaml:"operations"`
	MaxResultBytes int                      `yaml:"max_result_bytes"`
	MemoryMiB      int64                    `yaml:"memory_mib"`
	OpenFiles      uint64                   `yaml:"open_files"`
	FileMiB        uint64                   `yaml:"file_mib"`
	CPUSeconds     uint64                   `yaml:"cpu_seconds"`
}

// ClampLimits is DefaultLimits with s applied, each value clamped to its
// host maximum, and a warning for each clamp. A negative or zero value is
// the default.
func ClampLimits(s *LimitSettings) (Limits, []string) {
	l := DefaultLimits
	l.Operations = map[string]time.Duration{}
	if s == nil {
		return l, nil
	}
	var warnings []string
	dur := func(name string, set time.Duration, into *time.Duration, ceiling time.Duration) {
		switch {
		case set <= 0:
		case set > ceiling:
			warnings = append(warnings, fmt.Sprintf("limits.%s %s is over the host maximum %s; %s applies", name, set, ceiling, ceiling))
			*into = ceiling
		default:
			*into = set
		}
	}
	dur("init_timeout", s.InitTimeout, &l.Init, MaxLimits.Init)
	dur("load_timeout", s.LoadTimeout, &l.Load, MaxLimits.Load)
	dur("health_timeout", s.HealthTimeout, &l.Health, MaxLimits.Health)
	dur("unload_timeout", s.UnloadTimeout, &l.Unload, MaxLimits.Unload)
	dur("call_timeout", s.CallTimeout, &l.Call, MaxLimits.Call)
	for op, d := range s.Operations {
		v := l.Call
		dur("operations."+op, d, &v, MaxLimits.Call)
		l.Operations[op] = v
	}
	num := func(name string, set, ceiling uint64, into *uint64) {
		switch {
		case set == 0:
		case set > ceiling:
			warnings = append(warnings, fmt.Sprintf("limits.%s %d is over the host maximum %d; %d applies", name, set, ceiling, ceiling))
			*into = ceiling
		default:
			*into = set
		}
	}
	switch {
	case s.MaxResultBytes <= 0:
	case s.MaxResultBytes > MaxLimits.MaxResultBytes:
		warnings = append(warnings, fmt.Sprintf("limits.max_result_bytes %d is over the host maximum %d; %d applies", s.MaxResultBytes, MaxLimits.MaxResultBytes, MaxLimits.MaxResultBytes))
		l.MaxResultBytes = MaxLimits.MaxResultBytes
	default:
		l.MaxResultBytes = s.MaxResultBytes
	}
	switch mem := s.MemoryMiB << 20; {
	case s.MemoryMiB <= 0:
	case s.MemoryMiB > MaxLimits.MemoryBytes>>20:
		warnings = append(warnings, fmt.Sprintf("limits.memory_mib %d is over the host maximum %d; %d applies", s.MemoryMiB, MaxLimits.MemoryBytes>>20, MaxLimits.MemoryBytes>>20))
		l.MemoryBytes = MaxLimits.MemoryBytes
	default:
		l.MemoryBytes = mem
	}
	num("open_files", s.OpenFiles, MaxLimits.Process.OpenFiles, &l.Process.OpenFiles)
	if s.FileMiB > 0 {
		num("file_mib", s.FileMiB<<20, MaxLimits.Process.FileBytes, &l.Process.FileBytes)
	}
	num("cpu_seconds", s.CPUSeconds, MaxLimits.Process.CPUSeconds, &l.Process.CPUSeconds)
	return l, warnings
}

// CallTimeout is the deadline for operation op.
func (l Limits) CallTimeout(op string) time.Duration {
	if d, ok := l.Operations[op]; ok && d > 0 {
		return d
	}
	if l.Call > 0 {
		return l.Call
	}
	return DefaultLimits.Call
}

// PluginExecCommand is the hidden subcommand of the cerberus binary that
// sets a plugin's rlimits and then execs it (RunPluginExec).
const PluginExecCommand = "__plugin-exec"

// shimArgs are the arguments that run entrypoint under the shim with p.
func (p ProcessLimits) shimArgs(entrypoint string, args []string) []string {
	out := []string{PluginExecCommand,
		"--nofile=" + strconv.FormatUint(p.OpenFiles, 10),
		"--fsize=" + strconv.FormatUint(p.FileBytes, 10),
		"--cpu=" + strconv.FormatUint(p.CPUSeconds, 10),
		"--", entrypoint}
	return append(out, args...)
}

// RunPluginExec is the shim: it sets the rlimits its flags name, no core
// dumps (a core would carry the plugin's credentials), and replaces itself
// with the plugin, which keeps this process's pid and process group. It
// returns only on failure.
func RunPluginExec(args []string, env []string) error {
	var p ProcessLimits
	i := 0
	for ; i < len(args) && args[i] != "--"; i++ {
		name, value, ok := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		n, err := strconv.ParseUint(value, 10, 64)
		if !ok || err != nil {
			return fmt.Errorf("%s: bad flag %q", PluginExecCommand, args[i])
		}
		switch name {
		case "nofile":
			p.OpenFiles = n
		case "fsize":
			p.FileBytes = n
		case "cpu":
			p.CPUSeconds = n
		default:
			return fmt.Errorf("%s: unknown flag %q", PluginExecCommand, args[i])
		}
	}
	if i+1 >= len(args) {
		return errors.New(PluginExecCommand + ": no entrypoint after --")
	}
	entry := args[i+1:]
	if err := setRlimits(p); err != nil {
		return err
	}
	return syscall.Exec(entry[0], entry, env) //nolint:gosec // the entrypoint the launcher resolved and checked
}

func setRlimits(p ProcessLimits) error {
	set := func(what int, name string, v uint64) error {
		if err := syscall.Setrlimit(what, &syscall.Rlimit{Cur: v, Max: v}); err != nil {
			return fmt.Errorf("%s: setrlimit %s=%d: %w", PluginExecCommand, name, v, err)
		}
		return nil
	}
	if err := set(syscall.RLIMIT_CORE, "core", 0); err != nil {
		return err
	}
	if p.OpenFiles > 0 {
		// NOFILE may not go above the current hard limit; lowering is what
		// we want, so take the smaller.
		var cur syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &cur); err == nil && cur.Max < p.OpenFiles {
			p.OpenFiles = cur.Max
		}
		if err := set(syscall.RLIMIT_NOFILE, "nofile", p.OpenFiles); err != nil {
			return err
		}
	}
	if p.FileBytes > 0 {
		if err := set(syscall.RLIMIT_FSIZE, "fsize", p.FileBytes); err != nil {
			return err
		}
	}
	if p.CPUSeconds > 0 {
		if err := set(syscall.RLIMIT_CPU, "cpu", p.CPUSeconds); err != nil {
			return err
		}
	}
	return nil
}

// ExecutablePath is the running cerberus binary, resolved from the process
// itself and never from PATH: the daemon runs with launchd's minimal PATH,
// where `cerberus` need not resolve.
func ExecutablePath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return p, nil
}
