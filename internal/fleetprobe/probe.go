package fleetprobe

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ResourceRef is what the probe needs to know about a declared Cerberus resource
// to attribute a running service to it.
type ResourceRef struct {
	ID      string
	Project string
	// Units and Containers name the systemd units (without .service) and the
	// containers the resource runs as, when its config declares them.
	Units      []string
	Containers []string
	// HealthURL is probed with a GET when Options.Health is set.
	HealthURL string
}

// Options configure a probe.
type Options struct {
	// BinDirs are directories of installed executables to report.
	BinDirs []string
	// Resources attribute services to Cerberus resources (optional).
	Resources []ResourceRef
	// SystemUnits includes system-manager services whose executable is not part
	// of the operating system.
	SystemUnits bool
	// Health GETs each attributed resource's declared health URL.
	Health        bool
	HealthTimeout time.Duration
	// HashCachePath remembers digests between probes; empty disables the cache.
	HashCachePath string
	// UnitPrefix is the naming prefix of units Cerberus generates
	// (prefix.<project>.<resource>.service); the last segment is the resource ID.
	UnitPrefix string
}

// DefaultUnitPrefix is the prefix of systemd units Cerberus generates.
const DefaultUnitPrefix = "com.hollis-labs.cerberus."

// Probe reports the host's running services and installed binaries.
func Probe(ctx context.Context, opts Options) (*Report, error) {
	start := time.Now()
	host, _ := os.Hostname()
	if opts.UnitPrefix == "" {
		opts.UnitPrefix = DefaultUnitPrefix
	}
	if opts.HealthTimeout == 0 {
		opts.HealthTimeout = 3 * time.Second
	}
	r := &Report{Schema: Schema, Host: host, ProbedAt: start.UTC().Truncate(time.Second), Prober: SelfBuild(),
		Services: []Service{}, Binaries: []Artifact{}}
	id := newIdentifier(opts.HashCachePath)
	defer id.hashes.save()
	procs := readProcTable("/proc")

	var units []unitInfo
	if us, err := runningUnits(ctx, true); err != nil {
		r.Warnings = append(r.Warnings, "systemd user units: "+err.Error())
	} else {
		units = us
	}
	var sysUnits []unitInfo
	if opts.SystemUnits {
		if us, err := runningUnits(ctx, false); err != nil {
			r.Warnings = append(r.Warnings, "systemd system units: "+err.Error())
		} else {
			for _, u := range us {
				if u.ExecPath != "" && !systemPath(u.ExecPath) {
					sysUnits = append(sysUnits, u)
				}
			}
		}
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	add := func(s Service) {
		mu.Lock()
		r.Services = append(r.Services, s)
		mu.Unlock()
	}
	for _, u := range units {
		if desktopUnits[unitBase(u.ID)] || u.MainPID <= 0 {
			continue
		}
		wg.Add(1)
		go func(u unitInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			add(id.unitService(ctx, KindSystemdUser, u, procs))
		}(u)
	}
	for _, u := range sysUnits {
		if u.MainPID <= 0 {
			continue
		}
		wg.Add(1)
		go func(u unitInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			add(id.unitService(ctx, KindSystemdSystem, u, procs))
		}(u)
	}
	if cs, err := containers(ctx); err != nil {
		if err != errNoDocker {
			mu.Lock()
			r.Warnings = append(r.Warnings, "docker: "+err.Error())
			mu.Unlock()
		}
	} else {
		mu.Lock()
		r.Services = append(r.Services, cs...)
		mu.Unlock()
	}
	for _, dir := range opts.BinDirs {
		ents, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				r.Warnings = append(r.Warnings, fmt.Sprintf("bin dir %s: %v", dir, err))
			}
			continue
		}
		for _, e := range ents {
			name := e.Name()
			if skipBinaryName(name) {
				continue
			}
			p := filepath.Join(dir, name)
			fi, err := os.Stat(p)
			if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
				continue
			}
			wg.Add(1)
			go func(p string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				a := id.fromFile(ctx, p)
				mu.Lock()
				r.Binaries = append(r.Binaries, a)
				mu.Unlock()
			}(p)
		}
	}
	wg.Wait()

	attribute(r.Services, opts.Resources, opts.UnitPrefix)
	if opts.Health {
		probeHealth(ctx, r.Services, opts.Resources, opts.HealthTimeout)
	}
	sort.Slice(r.Services, func(i, j int) bool {
		if r.Services[i].Kind != r.Services[j].Kind {
			return r.Services[i].Kind < r.Services[j].Kind
		}
		return r.Services[i].Name < r.Services[j].Name
	})
	sort.Slice(r.Binaries, func(i, j int) bool { return r.Binaries[i].Path < r.Binaries[j].Path })
	r.Duration = time.Since(start).Round(time.Millisecond).String()
	return r, nil
}

func (id *identifier) unitService(ctx context.Context, kind string, u unitInfo, procs *procTable) Service {
	s := Service{Kind: kind, Name: unitBase(u.ID), PID: u.MainPID}
	if !u.StartedAt.IsZero() {
		s.StartedAt = u.StartedAt
	}
	if _, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", u.MainPID)); err != nil && u.ExecPath != "" {
		// Another user's process (a root system service): identify the unit's
		// declared executable on disk instead; children are not visible either.
		a := id.fromFile(ctx, u.ExecPath)
		s.Artifact = &a
		return s
	}
	a := id.fromProcess(ctx, u.MainPID, procCmdline(u.MainPID))
	s.Artifact = &a
	s.Children = id.children(ctx, u.MainPID, a, procs)
	return s
}

// children identifies distinct executables below a service's main process.
func (id *identifier) children(ctx context.Context, pid int, main Artifact, procs *procTable) []Artifact {
	byKey := map[string]*Artifact{}
	var order []string
	for _, c := range procs.descendants(pid) {
		target, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", c))
		if err != nil {
			continue
		}
		exe := strings.TrimSuffix(target, " (deleted)")
		cmd := procCmdline(c)
		key := exe
		if isInterpreter(filepath.Base(exe)) {
			script := scriptArg(cmd, procCwd(c))
			if script == "" {
				continue // a shell running a command line, not a service
			}
			key = exe + "|" + script
		} else if systemPath(exe) {
			continue // sandbox wrappers, coreutils, distro tools
		}
		if key == main.Path && main.Script == nil {
			continue // a fork or worker of the main executable
		}
		if a, ok := byKey[key]; ok {
			a.PIDs = append(a.PIDs, c)
			continue
		}
		a := id.fromProcess(ctx, c, cmd)
		a.PIDs = []int{c}
		byKey[key] = &a
		order = append(order, key)
	}
	out := make([]Artifact, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// attribute fills ResourceID/Project from the declared resources.
func attribute(services []Service, refs []ResourceRef, prefix string) {
	if len(refs) == 0 {
		return
	}
	byID := map[string]*ResourceRef{}
	byUnit := map[string]*ResourceRef{}
	byContainer := map[string]*ResourceRef{}
	for i := range refs {
		ref := &refs[i]
		byID[ref.ID] = ref
		for _, u := range ref.Units {
			byUnit[strings.TrimSuffix(u, ".service")] = ref
		}
		for _, c := range ref.Containers {
			byContainer[c] = ref
		}
	}
	for i := range services {
		s := &services[i]
		var ref *ResourceRef
		switch s.Kind {
		case KindContainer:
			if ref = byContainer[s.Name]; ref == nil {
				ref = byID[s.Name]
			}
		default:
			if ref = byUnit[s.Name]; ref == nil {
				ref = byID[unitResourceID(s.Name, prefix)]
			}
		}
		if ref != nil {
			s.ResourceID, s.Project = ref.ID, ref.Project
		}
	}
}

// backupMarkers name the copies an operator keeps beside an installed binary
// (tool.prev-20261030, tool.before-x, tool.bak); they are not installed tools.
var backupMarkers = []string{"prev", "pre", "before", "bak", "old", "orig", "tmp", "save"}

func skipBinaryName(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	for _, seg := range strings.Split(name, ".")[1:] {
		for _, m := range backupMarkers {
			if seg == m || strings.HasPrefix(seg, m+"-") || strings.HasPrefix(seg, m+"_") || (strings.HasPrefix(seg, m) && strings.Trim(strings.TrimPrefix(seg, m), "0123456789-") == "") {
				return true
			}
		}
	}
	return false
}

// unitResourceID maps a generated unit name (prefix.<project>.<id> or
// prefix.<id>) to its resource ID; other names map to themselves.
func unitResourceID(unit, prefix string) string {
	if !strings.HasPrefix(unit, prefix) {
		return unit
	}
	rest := strings.TrimPrefix(unit, prefix)
	if i := strings.LastIndex(rest, "."); i >= 0 {
		return rest[i+1:]
	}
	return rest
}

func probeHealth(ctx context.Context, services []Service, refs []ResourceRef, timeout time.Duration) {
	urls := map[string]string{}
	for _, ref := range refs {
		if ref.HealthURL != "" {
			urls[ref.ID] = ref.HealthURL
		}
	}
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var wg sync.WaitGroup
	for i := range services {
		u, ok := urls[services[i].ResourceID]
		if !ok {
			continue
		}
		wg.Add(1)
		go func(s *Service, u string) {
			defer wg.Done()
			h := &Health{URL: u}
			t0 := time.Now()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err == nil {
				var resp *http.Response
				if resp, err = client.Do(req); err == nil {
					h.Status = resp.StatusCode
					resp.Body.Close()
				}
			}
			if err != nil {
				h.Error = err.Error()
			}
			h.LatencyMS = time.Since(t0).Milliseconds()
			s.Health = h
		}(&services[i], u)
	}
	wg.Wait()
}
