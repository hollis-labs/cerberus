package hosts

import (
	"bufio"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// inventoryScript collects the physical facts of a Linux host: DMI identity,
// CPU, memory layout, whole disks and network interfaces. It is read-only and
// takes no input. Root-only facts (serial numbers, dmidecode, disk serials)
// are read with `sudo -n` where the host allows it, and skipped where it does
// not (the host is then reported as partial). It replaces the collection half
// of sysadmin/fleet/fleet-inventory.py.
const inventoryScript = `
S=; sudo -n true 2>/dev/null && S="sudo -n"
echo "sudo=$([ -n "$S" ] && echo 1 || echo 0)"
d=/sys/class/dmi/id
for k in sys_vendor product_name product_version board_vendor board_name bios_version bios_date chassis_asset_tag; do
  echo "$k=$(cat $d/$k 2>/dev/null)"
done
echo "cpu_model=$(awk -F: '/^model name/{print $2; exit}' /proc/cpuinfo)"
if [ -n "$S" ]; then
  echo "serial=$($S cat $d/product_serial 2>/dev/null)"
  echo "board_serial=$($S cat $d/board_serial 2>/dev/null)"
  echo "@@dmi-processor"; $S dmidecode -t 4 2>/dev/null
  echo "@@dmi-memory"; $S dmidecode -t 16,17 2>/dev/null
fi
echo "@@lsblk"; $S lsblk -dbnP -e 1,7,11 -o NAME,SIZE,ROTA,TRAN,MODEL,SERIAL 2>/dev/null
echo "@@nics"; for n in /sys/class/net/*; do [ -e "$n/device" ] && echo "${n##*/} $(cat $n/address)"; done
echo "@@end"
`

// InventoryCommand is inventoryScript as one ssh command, run under sh
// whatever the remote login shell is.
var InventoryCommand = "sh -c " + shellQuote(inventoryScript)

var placeholders = map[string]bool{
	"to be filled by o.e.m.": true, "default string": true, "not specified": true, "not applicable": true, "none": true,
	"system product name": true, "system manufacturer": true, "system version": true, "system serial number": true,
	"o.e.m.": true, "0": true, "": true, "unknown": true, "other": true, "no asset tag": true, "no asset information": true,
	"type1productconfigid": true,
}

func invText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if placeholders[strings.ToLower(s)] {
		return ""
	}
	if len(s) > 128 {
		s = s[:128]
	}
	return s
}

// sizeStr renders bytes as vendors label a disk (decimal), matching the
// precision the inventory script recorded so a re-run does not churn.
func sizeStr(n int64) string {
	for _, u := range []struct {
		name string
		mult int64
	}{{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}} {
		if n >= u.mult {
			if n%u.mult == 0 || n >= 100*u.mult {
				return fmt.Sprintf("%.0f%s", float64(n)/float64(u.mult), u.name)
			}
			return fmt.Sprintf("%.1f%s", float64(n)/float64(u.mult), u.name)
		}
	}
	return strconv.FormatInt(n, 10)
}

func gibStr(n int64) string {
	return strconv.FormatFloat(float64(n)/float64(1<<30), 'g', -1, 64) + "GiB"
}

func asSize(s string) Size {
	z, err := ParseSize(s)
	if err != nil {
		return 0
	}
	return z
}

var dmiHandleRE = regexp.MustCompile(`DMI type (\d+)`)
var dmiSizeRE = regexp.MustCompile(`^(\d+)\s*(MB|GB|TB|kB)`)

// dmiBlocks returns the key/value blocks of one DMI type from dmidecode
// output: only top-level (single-tab) "Key: value" lines.
func dmiBlocks(text string, handleType int) []map[string]string {
	var blocks []map[string]string
	var cur map[string]string
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "Handle "):
			cur = nil
			if m := dmiHandleRE.FindStringSubmatch(line); m != nil {
				if n, _ := strconv.Atoi(m[1]); n == handleType {
					cur = map[string]string{}
					blocks = append(blocks, cur)
				}
			}
		case cur != nil && strings.Contains(line, ":") && strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "\t\t"):
			k, v, _ := strings.Cut(strings.TrimSpace(line), ":")
			cur[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return blocks
}

func dmiSize(v string) int64 {
	m := dmiSizeRE.FindStringSubmatch(v)
	if m == nil {
		return 0
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	return n * map[string]int64{"kB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40}[m[2]]
}

var lsblkRE = regexp.MustCompile(`([A-Z]+)="([^"]*)"`)
var digitsOnly = regexp.MustCompile(`\D`)

// Collected is what one inventory run found.
type Collected struct {
	Hardware Hardware
	// Root is false when the host would not give root-only facts (no
	// passwordless sudo): serials and the memory layout are then absent and an
	// existing record keeps its own.
	Root bool
	// DMI facts the ceilings may be filled from (source dmi).
	MemSlots, CoreCount, ThreadCount int
	MemMaxTotal                      Size
	MemTypes                         []string
	ECC                              *bool
	Socket                           string
}

// ParseInventory reads the output of InventoryCommand. A missing section
// leaves its facts out; only output with no end marker is an error.
func ParseInventory(out string) (Collected, error) {
	if !strings.Contains(out, "@@end") {
		return Collected{}, fmt.Errorf("the inventory script did not finish (no end marker); the host may have dropped the connection")
	}
	sections := map[string][]string{}
	kv := map[string]string{}
	name := ""
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "@@"):
			name = line[2:]
			sections[name] = nil
		case name != "":
			sections[name] = append(sections[name], line)
		default:
			if k, v, ok := strings.Cut(line, "="); ok {
				kv[k] = v
			}
		}
	}
	c := Collected{Root: kv["sudo"] == "1"}
	hw := &c.Hardware
	hw.Vendor = invText(kv["sys_vendor"])
	hw.Model = invText(kv["product_name"])
	hw.Version = invText(kv["product_version"])
	hw.Serial = invText(kv["serial"])
	hw.AssetTag = invText(kv["chassis_asset_tag"])
	hw.Board = invText(invText(kv["board_vendor"]) + " " + invText(kv["board_name"]))
	hw.BIOS = invText(invText(kv["bios_version"]) + " " + invText(kv["bios_date"]))
	hw.CPUModel = invText(kv["cpu_model"])

	if proc := dmiBlocks(strings.Join(sections["dmi-processor"], "\n"), 4); len(proc) > 0 {
		hw.CPUSocket = invText(strings.TrimPrefix(invText(proc[0]["Upgrade"]), "Socket "))
		c.Socket = hw.CPUSocket
		c.CoreCount, _ = strconv.Atoi(proc[0]["Core Count"])
		c.ThreadCount, _ = strconv.Atoi(proc[0]["Thread Count"])
	}

	memText := strings.Join(sections["dmi-memory"], "\n")
	arrays, devices := dmiBlocks(memText, 16), dmiBlocks(memText, 17)
	if len(devices) > 0 {
		var filled []map[string]string
		for _, d := range devices {
			if dmiSize(d["Size"]) > 0 {
				filled = append(filled, d)
			}
		}
		mem := &HardwareMemory{}
		var total int64
		for _, d := range filled {
			total += dmiSize(d["Size"])
			speed := d["Configured Memory Speed"]
			if speed == "" {
				speed = d["Speed"]
			}
			speed = digitsOnly.ReplaceAllString(speed, "")
			var parts []string
			for _, p := range []string{gibStr(dmiSize(d["Size"])), invText(d["Type"]), speedText(speed), invText(d["Manufacturer"]), invText(d["Part Number"])} {
				if p != "" {
					parts = append(parts, p)
				}
			}
			mem.Modules = append(mem.Modules, strings.Join(parts, " "))
		}
		if len(filled) > 0 {
			mem.Type = invText(filled[0]["Type"])
		}
		mem.Total = asSize(gibStr(total))
		for _, a := range arrays {
			n, _ := strconv.Atoi(a["Number Of Devices"])
			mem.Slots += n
		}
		if mem.Slots == 0 {
			mem.Slots = len(devices)
		}
		mem.SlotsUsed = len(filled)
		var mx int64
		for _, a := range arrays {
			mx += dmiSize(a["Maximum Capacity"])
		}
		if mx > 0 {
			mem.Max = asSize(gibStr(mx))
			c.MemMaxTotal = mem.Max
		}
		hw.Memory = mem
		c.MemSlots = mem.Slots
		if mem.Type != "" {
			c.MemTypes = []string{mem.Type}
		}
		for _, a := range arrays {
			switch e := strings.ToLower(a["Error Correction Type"]); {
			case e == "":
			case e == "none":
				f := false
				c.ECC = &f
			default:
				t := true
				c.ECC = &t
			}
		}
	}

	for _, line := range sections["lsblk"] {
		f := map[string]string{}
		for _, m := range lsblkRE.FindAllStringSubmatch(line, -1) {
			f[m[1]] = m[2]
		}
		size, _ := strconv.ParseInt(f["SIZE"], 10, 64)
		if size == 0 || strings.HasPrefix(f["NAME"], "zram") || f["NAME"] == "" {
			continue
		}
		tran := f["TRAN"]
		kind := "ssd"
		switch {
		case tran == "nvme":
			kind = "nvme"
		case tran == "usb":
			kind = "usb"
		case f["ROTA"] == "1":
			kind = "hdd"
		}
		hw.Disks = append(hw.Disks, HardwareDisk{Name: f["NAME"], Model: invText(f["MODEL"]), Serial: invText(f["SERIAL"]),
			Size: asSize(sizeStr(size)), Type: kind, Bus: tran})
	}
	for _, line := range sections["nics"] {
		if p := strings.Fields(line); len(p) == 2 {
			hw.NICs = append(hw.NICs, HardwareNIC{Name: p[0], MAC: strings.ToLower(p[1])})
		}
	}
	return c, nil
}

func speedText(speed string) string {
	if speed == "" {
		return ""
	}
	return speed + "MT/s"
}

// sourcedFill sets *dst to value with source dmi when it is absent or already
// dmi-sourced; a value an operator or the vendor spec recorded stays.
func fillSourced[T any](dst **Sourced[T], value T) {
	if *dst != nil && (*dst).Source != SourceDMI {
		return
	}
	*dst = &Sourced[T]{Value: value, Source: SourceDMI}
}

// MergeInventory applies a collection to the named host. It replaces the
// collected facts and keeps everything an operator set: notes, storage_slots,
// ceilings recorded from the vendor spec or by hand, and every component. It
// reports whether the host record changed; recorded_at moves only when it did.
func MergeInventory(f *File, name string, c Collected, now time.Time) (bool, []string, error) {
	i := f.hostIndex(name)
	if i < 0 {
		return false, nil, fmt.Errorf("host %q is not declared", name)
	}
	h := f.Hosts[i]
	var old Hardware
	if h.Hardware != nil {
		old = *h.Hardware
	}
	var warnings []string

	nw := c.Hardware
	nw.Notes, nw.StorageSlots = old.Notes, old.StorageSlots
	if !c.Root {
		warnings = append(warnings, "no passwordless sudo on the host, so serials and the memory layout were not collected; the recorded ones were kept")
		nw.Serial, nw.CPUSocket, nw.Memory = old.Serial, old.CPUSocket, old.Memory
		for i := range nw.Disks {
			for _, od := range old.Disks {
				if od.Name == nw.Disks[i].Name && nw.Disks[i].Serial == "" {
					nw.Disks[i].Serial = od.Serial
				}
			}
		}
	}

	// Ceilings: DMI-sourced values are refreshed, anything else is kept.
	var ceil *Ceilings
	if old.Ceilings != nil {
		cp := *old.Ceilings
		ceil = &cp
	}
	if c.Root {
		if ceil == nil {
			ceil = &Ceilings{}
		}
		if c.MemSlots > 0 || c.MemMaxTotal > 0 || len(c.MemTypes) > 0 || c.ECC != nil {
			var m MemCeiling
			if ceil.Memory != nil {
				m = *ceil.Memory
			}
			if c.MemSlots > 0 {
				fillSourced(&m.Slots, c.MemSlots)
			}
			if c.MemMaxTotal > 0 {
				fillSourced(&m.MaxTotal, c.MemMaxTotal)
			}
			if len(c.MemTypes) > 0 {
				fillSourced(&m.Types, c.MemTypes)
			}
			if c.ECC != nil {
				fillSourced(&m.ECC, *c.ECC)
			}
			ceil.Memory = &m
		}
		if c.Socket != "" || c.CoreCount > 0 {
			var cpu CPUCeiling
			if ceil.CPU != nil {
				cpu = *ceil.CPU
			}
			if c.Socket != "" {
				if cpu.Socket == nil || cpu.Socket.Source == SourceDMI {
					cpu.Socket = &SourcedText{Value: c.Socket, Source: SourceDMI}
				}
			}
			if c.CoreCount > 0 && (cpu.Installed == nil || cpu.Installed.Source == SourceDMI) {
				cpu.Installed = &CPUSpec{Cores: c.CoreCount, Threads: c.ThreadCount, Source: SourceDMI}
			}
			ceil.CPU = &cpu
		}
		if reflect.DeepEqual(ceil, &Ceilings{}) {
			ceil = nil
		}
	}
	nw.Ceilings = ceil

	// recorded_at moves only when a fact did.
	nw.RecordedAt = old.RecordedAt
	if h.Hardware != nil && reflect.DeepEqual(old, nw) {
		return false, warnings, nil
	}
	t := now.UTC().Truncate(time.Second)
	nw.RecordedAt = &t
	h.Hardware = &nw
	f.Hosts[i] = h
	return true, warnings, nil
}
