package cerbapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/config"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/fleetprobe"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// Fleet operations, recorded under the host definition.
const (
	OpFleetInventory  = "fleet_inventory"
	OpFleetStageProbe = "fleet_stage_probe"
)

// FleetInventorySchema identifies the inventory format.
const FleetInventorySchema = "cerberus.fleet.inventory.v1"

// fleetProbeRel is where a staged probe lives on a host, relative to $HOME.
// Cerberus owns the path; staging never touches the operator's bin dirs.
const fleetProbeRel = ".local/share/cerberus/fleet-probe/cerberus"

// fleetBinDirsLabel is the host label naming installed-binary directories to
// report (colon-separated); unset means the probe's default (~/.local/bin).
const fleetBinDirsLabel = "fleet.bin_dirs"

const noProbeMarker = "CERBERUS_FLEET_NO_PROBE"

// Fleet host statuses.
const (
	FleetHostOK          = "ok"
	FleetHostNoProbe     = "no_probe"
	FleetHostNoReach     = "no_reach"
	FleetHostUnreachable = "unreachable"
	FleetHostError       = "error"
)

// FleetInventoryQuery selects hosts for an inventory.
type FleetInventoryQuery struct {
	// Hosts limits the inventory to these host names; empty is every host.
	Hosts []string `json:"hosts,omitempty"`
	Role  string   `json:"role,omitempty"`
	// Refresh bypasses the short-lived cache.
	Refresh bool `json:"refresh,omitempty"`
}

// FleetInventory is what runs where across the fleet.
type FleetInventory struct {
	Schema      string      `json:"schema"`
	GeneratedAt time.Time   `json:"generated_at"`
	Cached      bool        `json:"cached"`
	Hosts       []FleetHost `json:"hosts"`
	// Drift lists Go executables present on two or more hosts at different builds.
	Drift []fleetprobe.Drift `json:"drift"`
	// RepoModules is how many local checkouts upstream data was read from.
	RepoModules int    `json:"repo_modules"`
	Duration    string `json:"duration"`
}

// FleetHost is one host's part of the inventory.
type FleetHost struct {
	Name   string             `json:"name"`
	Role   string             `json:"role,omitempty"`
	Status string             `json:"status"`
	Reason string             `json:"reason,omitempty" cerb:"untrusted"`
	Report *fleetprobe.Report `json:"report,omitempty"`
}

// FleetOptions configure the fleet inventory on a HostService.
type FleetOptions struct {
	// Resources lists the declared resources, to attribute the local host's
	// services. Optional.
	Resources func() []config.ResourceDef
	// RepoRoots are directories searched for local checkouts (go.mod), to
	// compute how far each build is behind its default branch.
	RepoRoots []string
	// LocalBinDirs are the local host's installed-binary directories when the
	// host declares no fleet.bin_dirs label.
	LocalBinDirs []string
	CacheTTL     time.Duration
	// HostTimeout bounds one host's probe.
	HostTimeout time.Duration
}

type fleetState struct {
	mu    sync.Mutex
	opts  FleetOptions
	cache map[string]fleetCached
}

type fleetCached struct {
	at  time.Time
	inv *FleetInventory
}

// SetFleetOptions configures the fleet inventory.
func (s *HostService) SetFleetOptions(o FleetOptions) {
	s.fleetMu.Lock()
	defer s.fleetMu.Unlock()
	if o.CacheTTL <= 0 {
		o.CacheTTL = time.Minute
	}
	if o.HostTimeout <= 0 {
		o.HostTimeout = 90 * time.Second
	}
	s.fleet = &fleetState{opts: o, cache: map[string]fleetCached{}}
}

func (s *HostService) fleetConfig() *fleetState {
	s.fleetMu.Lock()
	defer s.fleetMu.Unlock()
	if s.fleet == nil {
		s.fleet = &fleetState{opts: FleetOptions{CacheTTL: time.Minute, HostTimeout: 90 * time.Second}, cache: map[string]fleetCached{}}
	}
	return s.fleet
}

// FleetInventory probes the selected hosts and reports what runs where. It is
// a read: the remote command is fixed by Cerberus and runs a probe binary
// already staged on the host (see StageFleetProbe).
func (s *HostService) FleetInventory(ctx context.Context, q FleetInventoryQuery) (_ *FleetInventory, retErr error) {
	if s == nil {
		return nil, errors.New("host service is not configured")
	}
	call, err := s.beginRead(ctx, OpFleetInventory, map[string]any{"hosts": strings.Join(q.Hosts, ","), "role": q.Role})
	if err != nil {
		return nil, err
	}
	defer func() { call.finish(retErr) }()

	st := s.fleetConfig()
	key := strings.Join(append(append([]string(nil), q.Hosts...), "|"+q.Role), ",")
	if !q.Refresh {
		st.mu.Lock()
		c, ok := st.cache[key]
		st.mu.Unlock()
		if ok && time.Since(c.at) < st.opts.CacheTTL {
			inv := *c.inv
			inv.Cached = true
			return &inv, nil
		}
	}

	start := time.Now()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	selected, err := selectFleetHosts(f.Hosts, q)
	if err != nil {
		return nil, err
	}
	var repos *fleetprobe.RepoIndex
	var repoWG sync.WaitGroup
	if len(st.opts.RepoRoots) > 0 {
		repoWG.Add(1)
		go func() { defer repoWG.Done(); repos = fleetprobe.NewRepoIndex(st.opts.RepoRoots) }()
	}

	results := make([]FleetHost, len(selected))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i, h := range selected {
		wg.Add(1)
		go func(i int, h hosts.Host) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			hctx, cancel := context.WithTimeout(ctx, st.opts.HostTimeout)
			defer cancel()
			results[i] = s.fleetHost(hctx, h, st.opts)
		}(i, h)
	}
	wg.Wait()
	repoWG.Wait()

	reports := map[string]*fleetprobe.Report{}
	for i := range results {
		if r := results[i].Report; r != nil {
			repos.Annotate(ctx, r)
			reports[results[i].Name] = r
		}
	}
	inv := &FleetInventory{Schema: FleetInventorySchema, GeneratedAt: start.UTC().Truncate(time.Second), Hosts: results,
		Drift: fleetprobe.FindDrift(reports)}
	if inv.Drift == nil {
		inv.Drift = []fleetprobe.Drift{}
	}
	if repos != nil {
		inv.RepoModules = repos.Len()
	}
	inv.Duration = time.Since(start).Round(time.Millisecond).String()
	st.mu.Lock()
	st.cache[key] = fleetCached{at: time.Now(), inv: inv}
	st.mu.Unlock()
	return inv, nil
}

func selectFleetHosts(all []hosts.Host, q FleetInventoryQuery) ([]hosts.Host, error) {
	want := map[string]bool{}
	for _, n := range q.Hosts {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	found := map[string]bool{}
	var out []hosts.Host
	for _, h := range all {
		if len(want) > 0 && !want[h.Name] {
			continue
		}
		found[h.Name] = true
		if q.Role != "" && h.Role != q.Role {
			continue
		}
		out = append(out, h)
	}
	var missing []string
	for n := range want {
		if !found[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, redact.Guidance("unknown host(s) %s; run `cerberus host list`", strings.Join(missing, ", "))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// isLocalHost reports whether h names the machine this process runs on.
func isLocalHost(h hosts.Host) bool {
	name, err := os.Hostname()
	if err != nil {
		return false
	}
	short := strings.SplitN(name, ".", 2)[0]
	for _, n := range []string{h.Name, h.Hostname, h.TailnetName} {
		if n != "" && (n == name || strings.SplitN(n, ".", 2)[0] == short) {
			return true
		}
	}
	return false
}

func hostBinDirs(h hosts.Host) []string {
	v := strings.TrimSpace(h.Labels[fleetBinDirsLabel])
	if v == "" {
		return nil
	}
	var dirs []string
	for _, d := range strings.Split(v, ":") {
		if d = strings.TrimSpace(d); d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

func (s *HostService) fleetHost(ctx context.Context, h hosts.Host, opts FleetOptions) FleetHost {
	out := FleetHost{Name: h.Name, Role: h.Role}
	if isLocalHost(h) {
		bins := hostBinDirs(h)
		if len(bins) == 0 {
			bins = opts.LocalBinDirs
		}
		po := fleetprobe.Options{BinDirs: expandHome(bins), SystemUnits: true, Health: true}
		if opts.Resources != nil {
			po.Resources = FleetResourceRefs(opts.Resources())
		}
		if cache, err := os.UserCacheDir(); err == nil {
			po.HashCachePath = filepath.Join(cache, "cerberus", "fleet-probe-hashes.json")
		}
		r, err := fleetprobe.Probe(ctx, po)
		if err != nil {
			out.Status, out.Reason = FleetHostError, err.Error()
			return out
		}
		out.Status, out.Report = FleetHostOK, r
		return out
	}
	if h.Reach.SSH == "" {
		out.Status, out.Reason = FleetHostNoReach, "no reach.ssh: the fleet probe runs over ssh"
		return out
	}
	def, err := s.reach(h)
	if err != nil {
		out.Status, out.Reason = FleetHostNoReach, err.Error()
		return out
	}
	stdout, code, err := s.execFleet(ctx, def, fleetProbeCommand(hostBinDirs(h)))
	switch {
	case err != nil:
		out.Status, out.Reason = FleetHostUnreachable, err.Error()
	case strings.Contains(stdout, noProbeMarker):
		out.Status, out.Reason = FleetHostNoProbe, "no probe staged on the host; run `cerberus fleet stage-probe --host "+h.Name+" --ack`"
	case code != 0:
		out.Status, out.Reason = FleetHostError, fmt.Sprintf("probe exited %d", code)
	default:
		var r fleetprobe.Report
		if err := json.Unmarshal([]byte(stdout), &r); err != nil {
			out.Status, out.Reason = FleetHostError, "probe output is not a report: "+err.Error()
		} else if r.Schema != fleetprobe.Schema {
			out.Status, out.Reason = FleetHostError, "probe reported schema "+r.Schema+"; restage the probe"
		} else {
			out.Status, out.Report = FleetHostOK, &r
		}
	}
	return out
}

// fleetProbeCommand is the fixed remote command: run the staged probe, or say
// it is missing. Bin dirs come from the operator's hosts.yaml label.
func fleetProbeCommand(binDirs []string) string {
	var flags strings.Builder
	for _, d := range binDirs {
		flags.WriteString(" --bin-dir ")
		flags.WriteString(shellQuote(d))
	}
	return fmt.Sprintf(`p="$HOME/%s"; if [ -x "$p" ]; then exec "$p" fleet probe --json --no-registry%s; fi; echo %s; exit 3`,
		fleetProbeRel, flags.String(), noProbeMarker)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func expandHome(dirs []string) []string {
	home, _ := os.UserHomeDir()
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if home != "" && (d == "~" || strings.HasPrefix(d, "~/")) {
			d = filepath.Join(home, strings.TrimPrefix(d, "~"))
		}
		out = append(out, d)
	}
	return out
}

func (s *HostService) execFleet(ctx context.Context, def *config.ResourceDef, command string) (string, int, error) {
	if s.ssh == nil {
		return "", 0, redact.Guidance("the ssh connector is not available")
	}
	sshc, err := s.ssh(ctx)
	if err != nil {
		return "", 0, err
	}
	res := sshResource(def)
	out, err := sshc.Exec(ctx, &res, command)
	if err != nil {
		return "", 0, err
	}
	return out.Stdout, out.ExitCode, nil
}

func sshResource(def *config.ResourceDef) resource.Resource {
	cfg := make(map[string]any, len(def.Config)+2)
	for k, v := range def.Config {
		cfg[k] = v
	}
	cfg["id"], cfg["name"] = def.ID, def.Name
	return resource.Resource{ID: def.ID, Name: def.Name, Type: resource.Server, Connector: "ssh", Config: cfg}
}

// FleetResourceRefs maps declared resources to the probe's attribution refs.
func FleetResourceRefs(defs []config.ResourceDef) []fleetprobe.ResourceRef {
	refs := make([]fleetprobe.ResourceRef, 0, len(defs))
	for _, r := range defs {
		ref := fleetprobe.ResourceRef{ID: r.ID, Project: r.Project}
		if v, ok := r.Config["service_name"].(string); ok && v != "" {
			ref.Units = append(ref.Units, v)
		}
		if v, ok := r.Config["container"].(string); ok && v != "" {
			ref.Containers = append(ref.Containers, v)
		}
		if hc, ok := r.Config["health_check"].(map[string]any); ok {
			if u, ok := hc["url"].(string); ok {
				ref.HealthURL = u
			}
		}
		if ref.HealthURL == "" {
			if u, ok := r.Config["url"].(string); ok && strings.HasPrefix(u, "http") {
				ref.HealthURL = u
			}
		}
		refs = append(refs, ref)
	}
	return refs
}

// ---- staging (a write) ----

// FleetStageArgs selects hosts to stage the probe on.
type FleetStageArgs struct {
	Hosts        []string `json:"hosts,omitempty"`
	Role         string   `json:"role,omitempty"`
	Acknowledged bool     `json:"acknowledged,omitempty"`
}

// FleetStageResult is one host's staging outcome.
type FleetStageResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // staged, current, skipped, error
	Reason string `json:"reason,omitempty" cerb:"untrusted"`
}

// hostSSHPut is the upload half of the ssh connector.
type hostSSHPut interface {
	Put(ctx context.Context, res *resource.Resource, localPath, remotePath string) (*sshconn.TransferResult, error)
}

var selfDigest = sync.OnceValues(func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	f, err := os.Open(exe)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
})

// StageFleetProbe copies this cerberus binary to each selected ssh host as its
// fleet probe (~/.local/share/cerberus/fleet-probe/cerberus). Hosts of another
// OS or architecture, and hosts already at this build, are skipped.
func (s *HostService) StageFleetProbe(ctx context.Context, a FleetStageArgs) (_ []FleetStageResult, retErr error) {
	if s == nil {
		return nil, errors.New("host service is not configured")
	}
	op, _ := HostDefinition().Operation(OpFleetStageProbe)
	spec := auditSpec{connector: "host", operation: OpFleetStageProbe, op: op, known: true, acknowledged: a.Acknowledged,
		config: map[string]any{"hosts": strings.Join(a.Hosts, ","), "role": a.Role}, resources: s.resources}
	call, err := beginGated(ctx, s.audit, s.logger, spec)
	if err != nil {
		return nil, err
	}
	defer func() { call.finish(retErr) }()

	f, err := s.load()
	if err != nil {
		return nil, err
	}
	selected, err := selectFleetHosts(f.Hosts, FleetInventoryQuery{Hosts: a.Hosts, Role: a.Role})
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	digest, err := selfDigest()
	if err != nil {
		return nil, err
	}
	out := make([]FleetStageResult, len(selected))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, h := range selected {
		wg.Add(1)
		go func(i int, h hosts.Host) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			hctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			out[i] = s.stageOne(hctx, h, exe, digest)
		}(i, h)
	}
	wg.Wait()
	return out, nil
}

func (s *HostService) stageOne(ctx context.Context, h hosts.Host, exe, digest string) FleetStageResult {
	r := FleetStageResult{Name: h.Name}
	if isLocalHost(h) {
		r.Status, r.Reason = "skipped", "local host: the inventory probes it in process"
		return r
	}
	if h.Reach.SSH == "" {
		r.Status, r.Reason = "skipped", "no reach.ssh"
		return r
	}
	def, err := s.reach(h)
	if err != nil {
		r.Status, r.Reason = "skipped", err.Error()
		return r
	}
	info, _, err := s.execFleet(ctx, def, fmt.Sprintf(`printf '%%s\n' "$HOME"; uname -sm; p="$HOME/%s"; if [ -f "$p" ]; then sha256sum "$p" 2>/dev/null | cut -d' ' -f1; fi`, fleetProbeRel))
	if err != nil {
		r.Status, r.Reason = "error", err.Error()
		return r
	}
	lines := strings.Split(strings.TrimSpace(info), "\n")
	if len(lines) < 2 {
		r.Status, r.Reason = "error", "unexpected answer from host"
		return r
	}
	home, osArch := strings.TrimSpace(lines[0]), strings.Fields(lines[1])
	if len(osArch) != 2 || !matchesPlatform(osArch[0], osArch[1]) {
		r.Status, r.Reason = "skipped", fmt.Sprintf("host is %s; this build is %s/%s", strings.Join(osArch, " "), runtime.GOOS, runtime.GOARCH)
		return r
	}
	if len(lines) >= 3 && strings.TrimSpace(lines[2]) == digest {
		r.Status = "current"
		return r
	}
	sshc, err := s.ssh(ctx)
	if err != nil {
		r.Status, r.Reason = "error", err.Error()
		return r
	}
	put, ok := sshc.(hostSSHPut)
	if !ok {
		r.Status, r.Reason = "error", "the ssh connector cannot upload files"
		return r
	}
	final := home + "/" + fleetProbeRel
	tmp := final + ".staging"
	if _, _, err := s.execFleet(ctx, def, fmt.Sprintf(`mkdir -p %s`, shellQuote(filepath.Dir(final)))); err != nil {
		r.Status, r.Reason = "error", err.Error()
		return r
	}
	res := sshResource(def)
	if _, err := put.Put(ctx, &res, exe, tmp); err != nil {
		r.Status, r.Reason = "error", err.Error()
		return r
	}
	check, code, err := s.execFleet(ctx, def, fmt.Sprintf(`chmod 755 %[1]s && sha256sum %[1]s | cut -d' ' -f1 && mv -f %[1]s %[2]s`, shellQuote(tmp), shellQuote(final)))
	switch {
	case err != nil:
		r.Status, r.Reason = "error", err.Error()
	case code != 0 || strings.TrimSpace(check) != digest:
		r.Status, r.Reason = "error", "uploaded file does not match this build"
	default:
		r.Status = "staged"
	}
	return r
}

func matchesPlatform(osName, arch string) bool {
	goos := map[string]string{"Linux": "linux", "Darwin": "darwin"}[osName]
	goarch := map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[arch]
	return goos == runtime.GOOS && goarch == runtime.GOARCH
}

// ---- clients and HTTP ----

// FleetClient is the optional client interface for the fleet inventory.
type FleetClient interface {
	FleetInventory(ctx context.Context, q FleetInventoryQuery) (*FleetInventory, error)
}

// FleetClientOf returns client as a FleetClient, or one that refuses every call.
func FleetClientOf(client any) FleetClient {
	if f, ok := client.(FleetClient); ok {
		return f
	}
	return unavailableFleetClient{}
}

type unavailableFleetClient struct{}

func (unavailableFleetClient) FleetInventory(context.Context, FleetInventoryQuery) (*FleetInventory, error) {
	return nil, redact.Guidance("the fleet inventory is not available from this serving host; run the daemon from this build")
}

// FleetInventory implements FleetClient on InProcessClient.
func (c *InProcessClient) FleetInventory(ctx context.Context, q FleetInventoryQuery) (*FleetInventory, error) {
	if c.hosts == nil {
		return nil, redact.Guidance("the fleet inventory needs the host service")
	}
	return c.hosts.FleetInventory(ctx, q)
}

// FleetInventory implements FleetClient on SocketClient.
func (c *SocketClient) FleetInventory(ctx context.Context, q FleetInventoryQuery) (*FleetInventory, error) {
	vals := url.Values{}
	if len(q.Hosts) > 0 {
		vals.Set("hosts", strings.Join(q.Hosts, ","))
	}
	if q.Role != "" {
		vals.Set("role", q.Role)
	}
	if q.Refresh {
		vals.Set("refresh", "true")
	}
	path := "/v1/fleet/inventory"
	if enc := vals.Encode(); enc != "" {
		path += "?" + enc
	}
	var out FleetInventory
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FleetInventoryHTTP serves the inventory on the daemon socket at /v1/fleet/inventory.
func FleetInventoryHTTP(client FleetClient) http.Handler {
	if client == nil {
		client = unavailableFleetClient{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		v := r.URL.Query()
		q := FleetInventoryQuery{Role: v.Get("role"), Refresh: v.Get("refresh") == "true" || v.Get("refresh") == "1"}
		if hs := strings.TrimSpace(v.Get("hosts")); hs != "" {
			q.Hosts = strings.Split(hs, ",")
		}
		inv, err := client.FleetInventory(r.Context(), q)
		if err != nil {
			err = scopeError(redact.ScopeFrom(r.Context()), err)
			writeServiceError(w, ExternalConnectorHTTPStatus(err, http.StatusInternalServerError), err)
			return
		}
		writeJSON(w, http.StatusOK, inv)
	})
}
