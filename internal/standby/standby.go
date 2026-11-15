// Package standby holds the on-disk contract of the daemon's standby mode
// (docs/substrate-port-plan.md section 7.1): the flag file that turns it on and
// names the one resource this daemon still operates, and the standby directory
// that keeps what standby itself records.
//
//	~/.cerberus/standby                     flag: present means standby; its first line is the resource id
//	~/.cerberus/standby.d/sessions.json     the dev sessions standby stopped, written once
//	~/.cerberus/standby.d/audit/            the standby audit chain (its own genesis and digest key)
//
// The package only reads and writes those paths. What standby does (stopping
// loops, plugins and sessions, fencing the socket) is the daemon's.
package standby

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// FlagName is the flag file under the state directory.
	FlagName = "standby"
	// DirName is the standby directory under the state directory.
	DirName = "standby.d"
	// SessionsName is the session list in the standby directory.
	SessionsName = "sessions.json"
	// AuditName is the standby audit chain's directory in the standby directory.
	AuditName = "audit"
	// PollInterval is how often the flag is checked.
	PollInterval = time.Second
)

// State is what the flag says.
type State struct {
	// Active is true while the flag file exists.
	Active bool
	// Resource is the one resource the daemon still operates, from the flag's
	// first line. Empty when the flag names none or names something that is
	// not a resource id: standby is then a full fence.
	Resource string
	// Problem says why Resource is empty for a flag that has content.
	Problem string
}

// Paths are the flag and the standby directory.
type Paths struct {
	Flag string
	Dir  string
}

// PathsIn returns the paths under a state directory (~/.cerberus).
func PathsIn(stateDir string) Paths {
	return Paths{Flag: filepath.Join(stateDir, FlagName), Dir: filepath.Join(stateDir, DirName)}
}

// DefaultPaths returns the paths under ~/.cerberus.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home for the standby flag: %w", err)
	}
	return PathsIn(filepath.Join(home, ".cerberus")), nil
}

// AuditDir is the standby audit chain's directory.
func (p Paths) AuditDir() string { return filepath.Join(p.Dir, AuditName) }

// SessionsPath is the session list.
func (p Paths) SessionsPath() string { return filepath.Join(p.Dir, SessionsName) }

var resourceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Read reports what the flag says. A flag that exists but cannot be read is
// still standby (fail closed), with no resource.
func (p Paths) Read() State {
	f, err := os.Open(p.Flag)
	if errors.Is(err, os.ErrNotExist) {
		return State{}
	}
	if err != nil {
		return State{Active: true, Problem: "the flag file could not be read: " + err.Error()}
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	line := ""
	if sc.Scan() {
		line = strings.TrimSpace(sc.Text())
	}
	switch {
	case line == "":
		return State{Active: true, Problem: "the flag file names no resource"}
	case !resourceID.MatchString(line):
		return State{Active: true, Problem: fmt.Sprintf("the flag file's first line %q is not a resource id", line)}
	}
	return State{Active: true, Resource: line}
}

// Watch reports the flag's state to apply now and again on every change,
// checking every interval until ctx ends. apply runs on Watch's goroutine,
// one call at a time.
func Watch(ctx context.Context, p Paths, interval time.Duration, apply func(State)) {
	last := p.Read()
	apply(last)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if cur := p.Read(); cur != last {
				last = cur
				apply(cur)
			}
		}
	}
}

// Sessions is the session list standby wrote at its first entry.
type Sessions struct {
	WrittenAt time.Time `json:"written_at"`
	IDs       []string  `json:"sessions"`
}

// WriteSessionsOnce records the dev sessions standby stopped. The file is
// written only if it does not exist: a later entry (a restart of the daemon in
// standby, when the sessions are already gone) leaves the first list as it is.
// It reports whether it wrote.
func (p Paths) WriteSessionsOnce(ids []string, now time.Time) (bool, error) {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return false, err
	}
	if ids == nil {
		ids = []string{}
	}
	data, err := json.MarshalIndent(Sessions{WrittenAt: now.UTC(), IDs: ids}, "", "  ")
	if err != nil {
		return false, err
	}
	f, err := os.OpenFile(p.SessionsPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return false, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return false, err
	}
	return true, f.Close()
}

// ReadSessions returns the recorded session list, and whether there is one.
func (p Paths) ReadSessions() (Sessions, bool, error) {
	data, err := os.ReadFile(p.SessionsPath())
	if errors.Is(err, os.ErrNotExist) {
		return Sessions{}, false, nil
	}
	if err != nil {
		return Sessions{}, false, err
	}
	var s Sessions
	if err := json.Unmarshal(data, &s); err != nil {
		return Sessions{}, true, fmt.Errorf("%s: %w", p.SessionsPath(), err)
	}
	return s, true, nil
}

// ClearSessions removes the session list once its sessions are started again,
// so the next standby records a fresh one.
func (p Paths) ClearSessions() error {
	if err := os.Remove(p.SessionsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
