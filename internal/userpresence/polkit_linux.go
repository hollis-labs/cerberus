//go:build linux

package userpresence

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hollis-labs/cerberus/internal/redact"
)

const pkcheckPath = "/usr/bin/pkcheck"
const pkttyagentPath = "/usr/bin/pkttyagent"

// Polkit asks the system authority about the CLI process, never about the
// daemon or a caller-supplied PID. Passwords stay between polkit and its
// terminal agent. There is no reusable enrollment grant returned to the CLI.
type Polkit struct {
	run       func(context.Context, ...string) int
	setup     func() error
	startTime func(int) (uint64, error)
}

func NewPolkit() Verifier { return &Polkit{} }

func (p *Polkit) prerequisites() error {
	if p.setup != nil {
		return p.setup()
	}
	for _, path := range []string{pkcheckPath, pkttyagentPath, PolkitPolicyPath} {
		if err := trustedSystemFile(path); err != nil {
			return err
		}
	}
	f, err := os.Open(PolkitPolicyPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return validatePolkitPolicy(io.LimitReader(f, 64*1024))
}

func (p *Polkit) EnrollmentSetup() Setup {
	if p.prerequisites() != nil {
		return Setup{Method: "polkit", NextStep: LinuxRecovery}
	}
	return Setup{Method: "polkit", Available: true, NextStep: "run `cerberus approvals enroll --browser=false` in your SSH terminal, authenticate with your Linux account password, then open the printed link in your browser"}
}

func (p *Polkit) Verify(ctx context.Context, _ string) error {
	s, ok := ctx.Value(subjectKey{}).(peerSubject)
	if !ok || s.PID <= 0 || s.UID <= 0 || s.StartTime == 0 {
		return redact.GuidanceFor(ErrUnavailable, "Linux enrollment needs the non-root CLI's kernel-verified socket identity; %s", LinuxRecovery)
	}
	if err := p.prerequisites(); err != nil {
		return redact.GuidanceFor(ErrUnavailable, "Linux enrollment prerequisites are unavailable; %s", LinuxRecovery)
	}
	startTime := p.startTime
	if startTime == nil {
		startTime = ProcessStartTime
	}
	unchanged := func() bool { start, err := startTime(s.PID); return err == nil && start == s.StartTime }
	if !unchanged() {
		return redact.GuidanceFor(ErrUnavailable, "the enrollment CLI exited or changed")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	run := p.run
	if run == nil {
		run = runPolkitCheck
	}
	args := []string{"--action-id", PolkitAction, "--process", fmt.Sprintf("%d,%d,%d", s.PID, s.StartTime, s.UID)}
	// A pre-existing or automatic authorization is not fresh authentication.
	// pkcheck returns 2 only when authentication is required. Also check after
	// success so an administrator's *_keep override cannot create a grant.
	if code := run(ctx, args...); code != 2 {
		if code == 0 {
			return redact.GuidanceFor(ErrUnavailable, "polkit granted enrollment without fresh authentication; remove automatic or cached grants for %s", PolkitAction)
		}
		return redact.GuidanceFor(ErrUnavailable, "polkit cannot request enrollment authentication; %s", LinuxRecovery)
	}
	code := run(ctx, append(append([]string{}, args...), "--allow-user-interaction")...)
	if ctx.Err() != nil {
		return redact.GuidanceFor(ErrRefused, "enrollment authentication timed out or was canceled")
	}
	switch code {
	case 0:
		if !unchanged() {
			return redact.GuidanceFor(ErrRefused, "the enrollment CLI exited or changed")
		}
		if run(ctx, args...) != 2 {
			return redact.GuidanceFor(ErrUnavailable, "polkit retained enrollment authorization; configure %s with auth_self, without cached or automatic grants", PolkitAction)
		}
		return ctx.Err()
	case 1, 3:
		return redact.GuidanceFor(ErrRefused, "polkit authentication was refused or canceled")
	default:
		return redact.GuidanceFor(ErrUnavailable, "polkit could not authenticate through the CLI terminal; %s", LinuxRecovery)
	}
}

func runPolkitCheck(ctx context.Context, args ...string) int {
	cmd := exec.CommandContext(ctx, pkcheckPath, args...) //nolint:gosec // fixed system binary, typed peer identity
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	cmd.WaitDelay = time.Second
	// Neither diagnostics nor authentication details are returned or logged.
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func validatePolkitPolicy(r io.Reader) error {
	var policy struct {
		XMLName xml.Name `xml:"policyconfig"`
		Actions []struct {
			ID       string `xml:"id,attr"`
			Defaults struct {
				Any      string `xml:"allow_any"`
				Inactive string `xml:"allow_inactive"`
				Active   string `xml:"allow_active"`
			} `xml:"defaults"`
		} `xml:"action"`
	}
	if err := xml.NewDecoder(r).Decode(&policy); err != nil {
		return err
	}
	if len(policy.Actions) != 1 {
		return errors.New("expected one enrollment action")
	}
	a := policy.Actions[0]
	if a.ID != PolkitAction || a.Defaults.Any != "auth_self" || a.Defaults.Inactive != "auth_self" || a.Defaults.Active != "auth_self" {
		return errors.New("enrollment action must require fresh auth_self in every session")
	}
	return nil
}

// Resolve system symlinks (e.g. merged /usr) but require every resolved
// ancestor and the file to be root-owned and not group/world writable.
func trustedSystemFile(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	for current := resolved; ; current = filepath.Dir(current) {
		st, err := os.Stat(current)
		if err != nil {
			return err
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok || sys.Uid != 0 || st.Mode().Perm()&0022 != 0 {
			return errors.New("untrusted system file ownership or permissions")
		}
		if current == resolved && !st.Mode().IsRegular() {
			return errors.New("system file is not regular")
		}
		if current == "/" {
			return nil
		}
	}
}

// ProcessStartTime reads field 22 of /proc/PID/stat. The command field can
// contain spaces and parentheses, so split only after its final ')'.
func ProcessStartTime(pid int) (uint64, error) {
	if pid <= 0 {
		return 0, errors.New("invalid process id")
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	return parseStartTime(string(b))
}

func parseStartTime(stat string) (uint64, error) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, errors.New("invalid process stat")
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) <= 19 {
		return 0, errors.New("short process stat")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return 0, errors.New("invalid process start time")
	}
	return start, nil
}
