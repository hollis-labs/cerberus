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
	Source          string          `json:"source"` // "posix"
	OS              string          `json:"os,omitempty"`
	OSVersion       string          `json:"os_version,omitempty"`
	Arch            string          `json:"arch,omitempty"`
	Kernel          string          `json:"kernel,omitempty"`
	CPU             int             `json:"cpu,omitempty"`
	CPUModel        string          `json:"cpu_model,omitempty" cerb:"untrusted"`
	GPUCount        *int            `json:"gpu_count,omitempty"` // nil: not determined; 0: none
	GPUModel        string          `json:"gpu_model,omitempty" cerb:"untrusted"`
	DiskMount       string          `json:"disk_mount,omitempty"`
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

// posixProbeScript is the primary fact-gathering script: plain POSIX
// utilities, read-only, one key=value line per fact. It sets its own PATH on
// the remote side; nothing about the controlling daemon's PATH is involved.
// Linux is covered fully; macOS through uname, sw_vers, sysctl, vm_stat and df.
// Any fact a host cannot supply is simply absent.
const posixProbeScript = `PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:$PATH; export PATH
kind=$(uname -s 2>/dev/null)
echo "os=$kind"
echo "arch=$(uname -m 2>/dev/null)"
echo "kernel=$(uname -r 2>/dev/null)"
mount=/
for d in /data /System/Volumes/Data; do [ -d "$d" ] && { mount=$d; break; }; done
echo "disk_mount=$mount"
echo "disk_kb=$(df -Pk "$mount" 2>/dev/null | awk 'NR==2{print $2, $4}')"
if [ "$kind" = Darwin ]; then
  echo "os_version=$(sw_vers -productVersion 2>/dev/null)"
  echo "cpu=$(sysctl -n hw.ncpu 2>/dev/null)"
  echo "cpu_model=$(sysctl -n machdep.cpu.brand_string 2>/dev/null)"
  echo "mem_total_bytes=$(sysctl -n hw.memsize 2>/dev/null)"
  echo "mem_available_bytes=$(vm_stat 2>/dev/null | awk '/page size of/{ps=$8} /^Pages free/{f=$3} /^Pages inactive/{i=$3} /^Pages speculative/{s=$3} END{if (ps) printf "%d", (f+i+s)*ps}')"
  echo "load=$(sysctl -n vm.loadavg 2>/dev/null | tr -d '{}')"
  echo "uptime_seconds=$(sysctl -n kern.boottime 2>/dev/null | awk -v now="$(date +%s)" '{gsub(/[^0-9]/," "); if ($1) print now-$1}')"
else
  echo "os_version=$(awk -F= '/^VERSION_ID=/{gsub(/"/,"",$2); print $2}' /etc/os-release 2>/dev/null)"
  echo "cpu=$(nproc 2>/dev/null || getconf _NPROCESSORS_ONLN 2>/dev/null)"
  echo "cpu_model=$(awk -F: '/^model name/{print $2; exit}' /proc/cpuinfo 2>/dev/null)"
  echo "mem_total_kb=$(awk '/^MemTotal:/{print $2}' /proc/meminfo 2>/dev/null)"
  echo "mem_available_kb=$(awk '/^MemAvailable:/{print $2}' /proc/meminfo 2>/dev/null)"
  echo "load=$(awk '{print $1, $2, $3}' /proc/loadavg 2>/dev/null)"
  echo "uptime_seconds=$(awk '{print int($1)}' /proc/uptime 2>/dev/null)"
  if command -v nvidia-smi >/dev/null 2>&1; then
    gpus=$(nvidia-smi --query-gpu=name --format=csv,noheader 2>/dev/null)
    echo "gpu_count=$(printf '%s\n' "$gpus" | grep -c .)"
    echo "gpu_model=$(printf '%s\n' "$gpus" | head -n 1)"
  else
    echo "gpu_count=0"
  fi
fi
for t in docker tmux tailscale caddy; do if command -v "$t" >/dev/null 2>&1; then echo "tool_$t=1"; else echo "tool_$t=0"; fi; done
`

// PosixProbeCommand is posixProbeScript as one ssh command. It runs under sh
// whatever the remote login shell is.
var PosixProbeCommand = "sh -c " + shellQuote(posixProbeScript)

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// factRE bounds the free-text facts (os, arch, kernel): a short token, so a
// credential-shaped or multi-line value is dropped rather than carried.
var factRE = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,48}$`)

// secretShapeRE drops a fact that looks like a credential even though it is
// token-shaped (a long unbroken run, or a known vendor prefix).
var secretShapeRE = regexp.MustCompile(`(?i)(^|[[:space:]._+-])(sk|pk|ghp|gho|xox[a-z]|akia|eyj)[A-Za-z0-9_-]{8,}`)

// textRE bounds descriptive text (a CPU or GPU model): printable, short.
var textRE = regexp.MustCompile(`^[A-Za-z0-9 ._+()@/,-]{1,64}$`)

func cleanText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if !textRE.MatchString(s) || secretShapeRE.MatchString(s) || longRun(s) {
		return ""
	}
	return s
}

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
		case "os_version":
			p.OSVersion = cleanFact(val)
		case "cpu":
			p.CPU = atoiBounded(val, 4096)
		case "cpu_model":
			p.CPUModel = cleanText(val)
		case "gpu_count":
			if n := atoiBounded(val, 64); val == "0" || n > 0 {
				p.GPUCount = &n
			}
		case "gpu_model":
			p.GPUModel = cleanText(val)
		case "disk_mount":
			if val == "/" || val == "/data" || val == "/System/Volumes/Data" {
				p.DiskMount = val
			}
		case "mem_available_bytes":
			p.MemoryAvailable = atoi64(val)
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
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 || n > limit {
		return 0
	}
	return n
}

// Empty reports a probe that learned nothing, so the caller can treat the
// host as not answering usefully.
func (p Probed) Empty() bool {
	return p.OS == "" && p.Arch == "" && p.CPU == 0 && p.MemoryTotal == 0 && p.DiskTotal == 0 && p.Load1 == nil
}
