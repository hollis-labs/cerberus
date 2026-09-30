package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/cerberus/internal/daemon"
	"github.com/hollis-labs/cerberus/internal/launchenv"
)

// systemdDaemonUnitTemplate is the daemon's systemd user unit, the Linux
// counterpart of launchdPlistTemplate: the invoking binary with
// `daemon --foreground`, restarted whenever it exits (KeepAlive), started
// with the user manager (RunAtLoad), logging beside the launchd logs, and a
// PATH composed from the installing shell. Only PATH is set: the manager's
// own environment, DBUS_SESSION_BUS_ADDRESS included, is left for the
// keyring and everything else to find.
const systemdDaemonUnitTemplate = `# Written by ` + "`cerberus install`" + `; rerun it rather than editing this file.
[Unit]
Description=Cerberus daemon
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart=%s daemon --foreground
WorkingDirectory=%s
Restart=always
RestartSec=5
Environment=%s
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`

// systemdInstaller installs the daemon as a systemd user unit. Its fields are
// what a test replaces.
type systemdInstaller struct {
	home       string
	binPath    string
	envPath    string
	systemctl  func(ctx context.Context, args ...string) ([]byte, error)
	lingerOn   func(ctx context.Context) (bool, error)
	out        io.Writer
	unitDir    string
	canonical  string
	handWrote  string
	stdoutPath string
	stderrPath string
}

func newSystemdInstaller(out io.Writer) (*systemdInstaller, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not determine home directory: %w", err)
	}
	binPath, err := resolveDaemonBinaryPath(os.Executable)
	if err != nil {
		return nil, err
	}
	return &systemdInstaller{
		home:      home,
		binPath:   binPath,
		envPath:   os.Getenv("PATH"),
		systemctl: daemon.SystemctlUser,
		lingerOn:  loginctlLinger,
		out:       out,
	}, nil
}

func (s *systemdInstaller) defaults() {
	if s.unitDir == "" {
		s.unitDir = filepath.Join(s.home, ".config", "systemd", "user")
	}
	if s.canonical == "" {
		s.canonical = daemon.CanonicalDaemonUnit
	}
	if s.handWrote == "" {
		s.handWrote = daemon.HandWrittenDaemonUnit
	}
	logs := filepath.Join(s.home, ".cerberus", "logs")
	s.stdoutPath = filepath.Join(logs, "systemd-stdout.log")
	s.stderrPath = filepath.Join(logs, "systemd-stderr.log")
}

func (s *systemdInstaller) renderUnit() string {
	quote := func(v string) string {
		v = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v)
		return `"` + strings.ReplaceAll(v, "%", "%%") + `"`
	}
	escape := func(v string) string { return strings.ReplaceAll(v, "%", "%%") }
	return fmt.Sprintf(systemdDaemonUnitTemplate,
		strings.ReplaceAll(quote(s.binPath), "$", "$$"),
		escape(s.home),
		quote("PATH="+launchenv.Path(s.envPath, launchenv.SystemdBasePath)),
		escape(s.stdoutPath),
		escape(s.stderrPath),
	)
}

func (s *systemdInstaller) run(ctx context.Context, args ...string) error {
	if out, err := s.systemctl(ctx, args...); err != nil {
		return fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// retireHandWritten moves the daemon off a unit written by hand before
// install supported Linux: it is disabled and stopped, so two daemons never
// contend for the socket, and its file is removed. A cerberus.service that
// does not run `cerberus daemon` is not ours, and install refuses rather
// than stop it. It reports whether there was one to retire.
func (s *systemdInstaller) retireHandWritten(ctx context.Context) (bool, error) {
	path := filepath.Join(s.unitDir, s.handWrote)
	data, err := os.ReadFile(path) //nolint:gosec // the user's own unit directory
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !handWrittenDaemonUnit(string(data)) {
		return false, fmt.Errorf("%s exists but does not run `cerberus daemon`; move it aside, then rerun `cerberus install`", path)
	}
	// Not loaded is fine: the file is what is left to clear.
	_, _ = s.systemctl(ctx, "disable", "--now", s.handWrote)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return true, fmt.Errorf("removing the hand-written unit (%s): %w", path, err)
	}
	return true, nil
}

// handWrittenDaemonUnit reports whether a unit's ExecStart runs the
// Cerberus daemon.
func handWrittenDaemonUnit(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if ok && strings.Contains(value, "cerberus") && strings.Contains(value, " daemon") {
			return true
		}
	}
	return false
}

func (s *systemdInstaller) install(ctx context.Context) error {
	s.defaults()
	if err := os.MkdirAll(s.unitDir, 0o755); err != nil { //nolint:gosec // the user manager's unit directory
		return fmt.Errorf("creating systemd user unit directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.stdoutPath), 0o700); err != nil {
		return fmt.Errorf("creating logs directory: %w", err)
	}
	retired, err := s.retireHandWritten(ctx)
	if err != nil {
		return err
	}
	if retired {
		fmt.Fprintf(s.out, "Retired the hand-written unit: %s\n", filepath.Join(s.unitDir, s.handWrote))
	}
	unitPath := filepath.Join(s.unitDir, s.canonical)
	if err := os.WriteFile(unitPath, []byte(s.renderUnit()), 0o644); err != nil { //nolint:gosec // a unit file carries no secret: only PATH
		return fmt.Errorf("writing unit: %w", err)
	}
	if err := s.run(ctx, "daemon-reload"); err != nil {
		return err
	}
	if err := s.run(ctx, "enable", s.canonical); err != nil {
		return err
	}
	// restart, not start: a reinstall must pick up a changed unit or binary,
	// as the launchd path's unload and load does.
	if err := s.run(ctx, "restart", s.canonical); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "Installed systemd user unit: %s\n", unitPath)
	fmt.Fprintf(s.out, "Binary: %s\n", s.binPath)
	switch on, lingerErr := s.lingerOn(ctx); {
	case lingerErr != nil:
		fmt.Fprintf(s.out, "Could not read lingering (%v). Without it the daemon stops at logout and does not start at boot; enable it with: loginctl enable-linger\n", lingerErr)
	case on:
		fmt.Fprintln(s.out, "Cerberus daemon will start automatically at boot (lingering is enabled).")
	default:
		fmt.Fprintln(s.out, "Cerberus daemon will start automatically at login.")
		fmt.Fprintln(s.out, "Lingering is off, so it stops at logout and does not start at boot. To keep it running, run: loginctl enable-linger")
	}
	return nil
}

func (s *systemdInstaller) uninstall(ctx context.Context) error {
	s.defaults()
	retired, err := s.retireHandWritten(ctx)
	if err != nil {
		return err
	}
	if retired {
		fmt.Fprintf(s.out, "Removed systemd user unit: %s\n", filepath.Join(s.unitDir, s.handWrote))
	}
	unitPath := filepath.Join(s.unitDir, s.canonical)
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		if !retired {
			fmt.Fprintln(s.out, "Systemd user unit not installed, nothing to do.")
		} else {
			_ = s.run(ctx, "daemon-reload")
		}
		return nil
	}
	// Not loaded is fine: the file is what is left to clear.
	_, _ = s.systemctl(ctx, "disable", "--now", s.canonical)
	if err := os.Remove(unitPath); err != nil {
		return fmt.Errorf("removing unit: %w", err)
	}
	if err := s.run(ctx, "daemon-reload"); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "Removed systemd user unit: %s\n", unitPath)
	fmt.Fprintln(s.out, "Cerberus daemon will no longer start automatically.")
	return nil
}

// loginctlLinger reads whether the current user lingers.
func loginctlLinger(ctx context.Context) (bool, error) {
	u, err := user.Current()
	if err != nil {
		return false, err
	}
	if _, statErr := os.Stat(filepath.Join("/var/lib/systemd/linger", u.Username)); statErr == nil {
		return true, nil
	}
	path, err := exec.LookPath("loginctl")
	if err != nil {
		path = "/usr/bin/loginctl"
	}
	out, err := exec.CommandContext(ctx, path, "show-user", u.Username, "--property", "Linger").CombinedOutput() //nolint:gosec // loginctl with the current user's name
	if err != nil {
		// loginctl answers only for a user with a session or lingering.
		return false, nil //nolint:nilerr // no session record and no linger file: not lingering
	}
	return strings.TrimSpace(string(out)) == "Linger=yes", nil
}
