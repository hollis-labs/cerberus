package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// accountHome is this account's home as the system records it, looked up
// by uid: never from $HOME or $USER, which the caller sets. Tests swap it.
var accountHome = func() (string, error) {
	uid := strconv.Itoa(os.Getuid())
	if runtime.GOOS == "darwin" {
		// Directory Services by absolute path: os/user falls back to $HOME
		// in a build without cgo, which would make this check agree with
		// whatever the caller set.
		out, err := exec.Command("/usr/bin/dscl", ".", "-search", "/Users", "UniqueID", uid).Output() //nolint:gosec // an absolute path and this process's own uid
		if err != nil {
			return "", fmt.Errorf("look up uid %s with dscl: %w", uid, err)
		}
		name, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")
		if name = strings.TrimSpace(name); name == "" {
			return "", fmt.Errorf("dscl knows no account with uid %s", uid)
		}
		out, err = exec.Command("/usr/bin/dscl", ".", "-read", "/Users/"+name, "NFSHomeDirectory").Output() //nolint:gosec // the record name dscl itself returned
		if err != nil {
			return "", fmt.Errorf("read %s's home with dscl: %w", name, err)
		}
		_, home, ok := strings.Cut(strings.TrimSpace(string(out)), ":")
		if home = strings.TrimSpace(home); !ok || home == "" {
			return "", fmt.Errorf("dscl records no home for %s", name)
		}
		return home, nil
	}
	u, err := user.LookupId(uid)
	if err != nil {
		return "", err
	}
	return u.HomeDir, nil
}

// realStateCheck reports whether this process reads Cerberus's real state:
// $HOME is the account's home. The brakes, policy, approvals and audit log
// live under it, and every state directory is resolved from $HOME, so a
// HOME pointed elsewhere (with --config sending the call in-process) would
// run a mutation under a scratch lockdown, a scratch policy and a scratch
// audit log (M11).
func realStateCheck() error {
	home := os.Getenv("HOME")
	want, err := accountHome()
	if err != nil {
		return fmt.Errorf("this account's home could not be looked up, so Cerberus cannot confirm it would read its own brakes, policy and audit log: %w", err)
	}
	if samePath(home, want) {
		return nil
	}
	return fmt.Errorf("HOME is %q, but this account's home is %q, and Cerberus keeps its brakes, policy and audit log under the account's home. Run it without overriding HOME, or through the daemon (without --config)", home, want)
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	return resolve(a) == resolve(b)
}
