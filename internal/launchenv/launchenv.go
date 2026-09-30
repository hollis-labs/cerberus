// Package launchenv composes the environment a supervisor-launched process
// starts with. A supervisor (launchd, the systemd user manager) hands its
// jobs a minimal PATH, and everything Cerberus runs under one shells out.
package launchenv

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LaunchdBasePath is the PATH launchd hands a user agent that declares no
// EnvironmentVariables of its own.
const LaunchdBasePath = "/usr/bin:/bin:/usr/sbin:/sbin"

// SystemdBasePath is the PATH the systemd user manager hands a unit that
// sets none (systemd's compiled-in default).
const SystemdBasePath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// Path composes the PATH baked into a supervised job: the entries of envPath
// (the installing or serving process's own PATH), then every entry of base,
// so the system tools stay reachable even if envPath is odd.
//
// Entries are filtered to absolute paths: a relative entry — "." or "" — in a
// long-lived background job's PATH is a way to get arbitrary code run as the
// operator, and it cannot mean anything useful to a job whose working
// directory is fixed. Duplicates keep their first position.
func Path(envPath, base string) string {
	seen := make(map[string]bool)
	var entries []string
	add := func(candidates string) {
		for _, entry := range filepath.SplitList(candidates) {
			if entry == "" || !filepath.IsAbs(entry) || seen[entry] {
				continue
			}
			seen[entry] = true
			entries = append(entries, entry)
		}
	}
	add(envPath)
	add(base)
	return strings.Join(entries, string(filepath.ListSeparator))
}

// UserBusEnv returns the variables a `systemctl --user` / `journalctl --user`
// call needs to reach the user manager, for those getenv reports unset: a
// daemon started outside a login session (from cron, a boot script, a
// manually exec'd shell) may have neither. The defaults are where
// systemd-logind puts them for uid.
func UserBusEnv(getenv func(string) string, uid int) []string {
	runtimeDir := getenv("XDG_RUNTIME_DIR")
	var out []string
	if runtimeDir == "" {
		runtimeDir = "/run/user/" + strconv.Itoa(uid)
		out = append(out, "XDG_RUNTIME_DIR="+runtimeDir)
	}
	if getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		out = append(out, "DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(runtimeDir, "bus"))
	}
	return out
}

// UserBusEnviron is os.Environ with UserBusEnv's defaults appended.
func UserBusEnviron() []string {
	return append(os.Environ(), UserBusEnv(os.Getenv, os.Getuid())...)
}
