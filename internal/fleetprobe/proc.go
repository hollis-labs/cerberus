package fleetprobe

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procTable is a snapshot of the host's process tree.
type procTable struct {
	children map[int][]int
}

func readProcTable(root string) *procTable {
	t := &procTable{children: map[int][]int{}}
	ents, err := os.ReadDir(root)
	if err != nil {
		return t
	}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if ppid := readPPID(filepath.Join(root, e.Name(), "stat")); ppid > 0 {
			t.children[ppid] = append(t.children[ppid], pid)
		}
	}
	return t
}

// readPPID parses field 4 of /proc/<pid>/stat. The comm field (2) may contain
// spaces and parentheses, so parsing starts after the last ')'.
func readPPID(statPath string) int {
	b, err := os.ReadFile(statPath)
	if err != nil {
		return 0
	}
	return parsePPID(b)
}

func parsePPID(stat []byte) int {
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(string(stat[i+1:]))
	if len(f) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(f[1])
	return ppid
}

// descendants lists every process below pid, breadth first.
func (t *procTable) descendants(pid int) []int {
	var out []int
	queue := append([]int(nil), t.children[pid]...)
	seen := map[int]bool{}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		queue = append(queue, t.children[p]...)
	}
	return out
}

func procCmdline(pid int) []string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
}

func procCwd(pid int) string {
	d, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		return ""
	}
	return d
}

// systemPath reports whether an executable is part of the operating system
// (distribution packages), which the probe does not report as a child.
func systemPath(p string) bool {
	for _, pre := range []string{"/usr/bin/", "/usr/sbin/", "/usr/lib/", "/usr/libexec/", "/bin/", "/sbin/", "/lib/"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}
