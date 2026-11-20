package fleetprobe

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// unitInfo is the subset of `systemctl show` the probe uses.
type unitInfo struct {
	ID        string
	MainPID   int
	StartedAt time.Time
	ExecPath  string
}

// desktopUnits are session plumbing on a user manager, never fleet services.
var desktopUnits = map[string]bool{
	"dbus": true, "at-spi-dbus-bus": true, "gcr-ssh-agent": true, "ssh-agent": true, "gpg-agent": true,
	"xdg-document-portal": true, "xdg-permission-store": true, "xdg-desktop-portal": true,
	"pipewire": true, "pipewire-pulse": true, "wireplumber": true, "pulseaudio": true,
	"gnome-keyring-daemon": true, "dconf": true, "evolution-source-registry": true,
}

// runningUnits lists running service units on the user (user=true) or system manager.
func runningUnits(ctx context.Context, user bool) ([]unitInfo, error) {
	scope := []string{}
	if user {
		scope = append(scope, "--user")
	}
	list, err := systemctl(ctx, append(scope, "list-units", "--type=service", "--state=running", "--no-legend", "--plain")...)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(list, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || !strings.HasSuffix(f[0], ".service") {
			continue
		}
		names = append(names, f[0])
	}
	if len(names) == 0 {
		return nil, nil
	}
	out, err := systemctl(ctx, append(append(scope, "show", "-p", "Id,MainPID,ExecMainStartTimestamp,ExecStart"), names...)...)
	if err != nil {
		return nil, err
	}
	return parseShow(out), nil
}

func systemctl(ctx context.Context, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	b, err := exec.CommandContext(cctx, "systemctl", args...).Output()
	return string(b), err
}

// parseShow parses `systemctl show` output for several units (blank-line separated).
func parseShow(out string) []unitInfo {
	var units []unitInfo
	cur := unitInfo{}
	flush := func() {
		if cur.ID != "" {
			units = append(units, cur)
		}
		cur = unitInfo{}
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "Id":
			cur.ID = v
		case "MainPID":
			cur.MainPID, _ = strconv.Atoi(v)
		case "ExecMainStartTimestamp":
			cur.StartedAt = parseSystemdTime(v)
		case "ExecStart":
			// { path=/x/y ; argv[]=/x/y a b ; ... }
			if i := strings.Index(v, "path="); i >= 0 {
				rest := v[i+5:]
				if j := strings.IndexAny(rest, " ;"); j >= 0 {
					rest = rest[:j]
				}
				cur.ExecPath = rest
			}
		}
	}
	flush()
	return units
}

// parseSystemdTime parses "Thu 2026-11-19 21:55:43 UTC" (zone abbreviations vary).
func parseSystemdTime(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" || v == "n/a" {
		return time.Time{}
	}
	f := strings.Fields(v)
	if len(f) >= 3 {
		if t, err := time.Parse("2006-01-02 15:04:05", f[1]+" "+f[2]); err == nil {
			if len(f) >= 4 {
				if loc, err := time.LoadLocation(f[3]); err == nil {
					if t2, err := time.ParseInLocation("2006-01-02 15:04:05", f[1]+" "+f[2], loc); err == nil {
						return t2.UTC()
					}
				}
				if t2, err := time.Parse("2006-01-02 15:04:05 MST", f[1]+" "+f[2]+" "+f[3]); err == nil {
					return t2.UTC()
				}
			}
			return t.UTC()
		}
	}
	return time.Time{}
}

func unitBase(id string) string { return strings.TrimSuffix(id, ".service") }
