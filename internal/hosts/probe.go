package hosts

import (
	"bufio"
	"regexp"
	"strconv"
	"strings"
)

// Probed is what a probe measured. It is a DTO (ADR 0003) built only from an
// allow-list of keys and shapes: nothing a remote host prints reaches it
// except a number, a bool or a short token that matches factRE.
type Probed struct {
	Source          string          `json:"source"` // "sysmon" or "posix"
	OS              string          `json:"os,omitempty"`
	Arch            string          `json:"arch,omitempty"`
	Kernel          string          `json:"kernel,omitempty"`
	CPU             int             `json:"cpu,omitempty"`
	MemoryTotal     int64           `json:"memory_total,omitempty"`
	MemoryAvailable int64           `json:"memory_available,omitempty"`
	DiskTotal       int64           `json:"disk_total,omitempty"`
	DiskFree        int64           `json:"disk_free,omitempty"`
	Load1           *float64        `json:"load1,omitempty"`
	Load5           *float64        `json:"load5,omitempty"`
	Load15          *float64        `json:"load15,omitempty"`
	UptimeSeconds   int64           `json:"uptime_seconds,omitempty"`
	Tools           map[string]bool `json:"tools,omitempty"`
}

// ProbedTools are the tools a probe reports the presence of.
var ProbedTools = []string{"docker", "tmux", "tailscale", "caddy"}

// PosixProbeCommand is the fallback fact-gathering command: plain POSIX
// utilities, read-only, output as key=value lines. It sets its own PATH on
// the remote side; nothing about the controlling daemon's PATH is involved.
const PosixProbeCommand = `PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:$PATH; export PATH; ` +
	`echo "os=$(uname -s 2>/dev/null)"; echo "arch=$(uname -m 2>/dev/null)"; echo "kernel=$(uname -r 2>/dev/null)"; ` +
	`echo "cpu=$(nproc 2>/dev/null || getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null)"; ` +
	`if [ -r /proc/meminfo ]; then ` +
	`echo "mem_total_kb=$(awk '/^MemTotal:/{print $2}' /proc/meminfo)"; echo "mem_available_kb=$(awk '/^MemAvailable:/{print $2}' /proc/meminfo)"; ` +
	`else echo "mem_total_bytes=$(sysctl -n hw.memsize 2>/dev/null)"; fi; ` +
	`if [ -r /proc/loadavg ]; then echo "load=$(awk '{print $1, $2, $3}' /proc/loadavg)"; ` +
	`else echo "load=$(sysctl -n vm.loadavg 2>/dev/null | tr -d '{}')"; fi; ` +
	`[ -r /proc/uptime ] && echo "uptime_seconds=$(awk '{print int($1)}' /proc/uptime)"; ` +
	`echo "disk_kb=$(df -Pk / 2>/dev/null | awk 'NR==2{print $2, $4}')"; ` +
	`for t in docker tmux tailscale caddy; do if command -v "$t" >/dev/null 2>&1; then echo "tool_$t=1"; else echo "tool_$t=0"; fi; done`

// factRE bounds the free-text facts (os, arch, kernel): a short token, so a
// credential-shaped or multi-line value is dropped rather than carried.
var factRE = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,48}$`)

// secretShapeRE drops a fact that looks like a credential even though it is
// token-shaped (a long unbroken run, or a known vendor prefix).
var secretShapeRE = regexp.MustCompile(`(?i)(^|[._+-])(sk|pk|ghp|gho|xox[a-z]|akia|eyj)[A-Za-z0-9_-]{8,}`)

func cleanFact(s string) string {
	s = strings.TrimSpace(s)
	if !factRE.MatchString(s) || secretShapeRE.MatchString(s) || longRun(s) {
		return ""
	}
	return s
}

// longRun reports an unbroken alphanumeric run of 24 or more: no OS, arch or
// kernel name is that long, and a token is.
func longRun(s string) bool {
	run := 0
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			run++
			if run >= 24 {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}

// ParsePosixProbe builds a Probed from the key=value output of
// PosixProbeCommand. Unknown keys are ignored and malformed values dropped; it
// never fails, so a half-working host still reports what it can.
func ParsePosixProbe(out string) Probed {
	p := Probed{Source: "posix", Tools: map[string]bool{}}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "os":
			p.OS = cleanFact(val)
		case "arch":
			p.Arch = cleanFact(val)
		case "kernel":
			p.Kernel = cleanFact(val)
		case "cpu":
			p.CPU = atoiBounded(val, 4096)
		case "mem_total_kb":
			p.MemoryTotal = atoi64(val) * 1024
		case "mem_available_kb":
			p.MemoryAvailable = atoi64(val) * 1024
		case "mem_total_bytes":
			p.MemoryTotal = atoi64(val)
		case "load":
			f := strings.Fields(val)
			loads := [3]**float64{&p.Load1, &p.Load5, &p.Load15}
			for i := 0; i < len(f) && i < 3; i++ {
				if v, err := strconv.ParseFloat(f[i], 64); err == nil && v >= 0 {
					*loads[i] = &v
				}
			}
		case "uptime_seconds":
			p.UptimeSeconds = atoi64(val)
		case "disk_kb":
			f := strings.Fields(val)
			if len(f) == 2 {
				p.DiskTotal = atoi64(f[0]) * 1024
				p.DiskFree = atoi64(f[1]) * 1024
			}
		default:
			if name, found := strings.CutPrefix(key, "tool_"); found {
				for _, t := range ProbedTools {
					if t == name && (val == "0" || val == "1") {
						p.Tools[name] = val == "1"
					}
				}
			}
		}
	}
	if len(p.Tools) == 0 {
		p.Tools = nil
	}
	return p
}

func atoi64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func atoiBounded(s string, limit int) int {
	n := atoi64(s)
	if n > int64(limit) {
		return 0
	}
	return int(n)
}

// Empty reports a probe that learned nothing, so the caller can treat the
// host as not answering usefully.
func (p Probed) Empty() bool {
	return p.OS == "" && p.Arch == "" && p.CPU == 0 && p.MemoryTotal == 0 && p.DiskTotal == 0 && p.Load1 == nil
}
