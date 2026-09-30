package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// otherHumanAccounts lists the local accounts, other than the operator's,
// that a person could log in to. A no-auth mcp-http on loopback is reachable
// by every one of them, and macOS gives a TCP listener no way to tell who is
// calling, so it is allowed only where this list is empty. Tests swap it.
var otherHumanAccounts = func() ([]string, error) {
	me, err := user.Current()
	if err != nil {
		return nil, err
	}
	switch runtime.GOOS {
	case "darwin":
		return darwinHumanAccounts(me.Username)
	case "linux":
		data, rerr := os.ReadFile("/etc/passwd")
		if rerr != nil {
			return nil, rerr
		}
		return passwdHumanAccounts(data, me.Username), nil
	default:
		return nil, fmt.Errorf("cannot list the local accounts on %s", runtime.GOOS)
	}
}

// nonLoginShells are shells no one logs in to.
var nonLoginShells = map[string]bool{"/usr/bin/false": true, "/bin/false": true, "/sbin/nologin": true, "/usr/sbin/nologin": true, "": true}

// darwinHumanAccounts reads Directory Services, by absolute path: the
// daemon's PATH is launchd's, and a lookup that silently found nothing would
// allow what it exists to refuse.
func darwinHumanAccounts(me string) ([]string, error) {
	uids, err := exec.Command("/usr/bin/dscl", ".", "-list", "/Users", "UniqueID").Output()
	if err != nil {
		return nil, fmt.Errorf("list local accounts with dscl: %w", err)
	}
	shells, err := exec.Command("/usr/bin/dscl", ".", "-list", "/Users", "UserShell").Output()
	if err != nil {
		return nil, fmt.Errorf("list local account shells with dscl: %w", err)
	}
	return dsclHumanAccounts(uids, shells, me), nil
}

// dsclHumanAccounts is the accounts, other than me, with a real uid (501
// and up on macOS), a login shell and no leading underscore (a service
// account).
func dsclHumanAccounts(uids, shells []byte, me string) []string {
	shellOf := dsclColumns(shells)
	var out []string
	for name, uid := range dsclColumns(uids) {
		n, err := strconv.Atoi(uid)
		if err != nil || n < 501 || name == me || strings.HasPrefix(name, "_") || nonLoginShells[shellOf[name]] {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func dsclColumns(data []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 {
			out[fields[0]] = fields[len(fields)-1]
		}
	}
	return out
}

// passwdHumanAccounts is /etc/passwd's accounts, other than me, with a uid
// of 1000 or more (the usual UID_MIN) and a login shell.
func passwdHumanAccounts(data []byte, me string) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		fields := strings.Split(sc.Text(), ":")
		if len(fields) < 7 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		uid, err := strconv.Atoi(fields[2])
		if err != nil || uid < 1000 || uid == 65534 || fields[0] == me || nonLoginShells[fields[6]] {
			continue
		}
		out = append(out, fields[0])
	}
	sort.Strings(out)
	return out
}
