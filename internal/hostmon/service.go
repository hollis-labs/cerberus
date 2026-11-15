package hostmon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/config"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/pkg/outpost"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// SSH is the part of the ssh connector a probe uses.
type SSH interface {
	Exec(ctx context.Context, res *resource.Resource, command string) (*sshconn.ExecResult, error)
}

// ProbeOptions build a Prober. HostsPath and StatePath are required.
type ProbeOptions struct {
	// HostsPath is hosts.yaml, read fresh on every load.
	HostsPath string
	// StatePath is the derived-state file the probes write (state/hosts.json in the data
	// directory).
	StatePath string
	// Resources resolves a host's ssh reach.
	Resources func(id string) (*config.ResourceDef, bool)
	// SSH returns the ssh connector, resolved per probe; nil when this build has none, and then
	// ssh hosts are not probed (the monitor skips them; they are never marked offline for it).
	SSH    func(ctx context.Context) (SSH, error)
	Logger *slog.Logger
	Now    func() time.Time
}

// Prober runs the fixed host probe over a host's reach and records the derived state. It is
// the old HostService's probe path, without the per-call audit record: the monitor records state
// changes, failure streaks and an hourly summary instead (ProbeQuiet on main).
type Prober struct {
	o     ProbeOptions
	store *hosts.Store
	limit time.Duration
	mu    sync.Mutex // serializes the read-modify-write of stored state
}

// ErrNoSSH is the reason an ssh host is not probed by a build without the ssh connector.
var ErrNoSSH = errors.New("the ssh connector is not available in this build")

// NewProber builds a Prober.
func NewProber(o ProbeOptions) (*Prober, error) {
	if o.HostsPath == "" || o.StatePath == "" {
		return nil, errors.New("hostmon: HostsPath and StatePath are required")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Prober{o: o, store: hosts.NewStore(o.StatePath), limit: 45 * time.Second}, nil
}

// Load reads hosts.yaml.
func (p *Prober) Load() (*hosts.File, error) { return hosts.Load(p.o.HostsPath) }

// CanReach reports whether this build can probe h: false for an ssh host without the ssh
// connector.
func (p *Prober) CanReach(h hosts.Host) bool {
	return h.Reach.EffectiveTransport() != "ssh" || p.o.SSH != nil
}

// Probe measures one host and records its derived state, returning that state.
func (p *Prober) Probe(ctx context.Context, name string) (hosts.State, error) {
	f, err := p.Load()
	if err != nil {
		return hosts.State{}, err
	}
	var h *hosts.Host
	for i := range f.Hosts {
		if f.Hosts[i].Name == name {
			h = &f.Hosts[i]
		}
	}
	if h == nil {
		return hosts.State{}, fmt.Errorf("host %q is not declared; see `cerberus host list` (hosts are declared in hosts.yaml)", name)
	}
	ctx, scope := redact.EnsureScope(ctx)
	pctx, cancel := context.WithTimeout(ctx, p.limit)
	defer cancel()
	gather, err := p.transport(pctx, *h)
	if err != nil {
		return hosts.State{}, err
	}
	// The measurement runs unlocked, so probes of different hosts overlap (the monitor bounds
	// them); only the read-modify-write of the stored state is serialized.
	probed, perr := gather()

	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.o.Now().UTC()
	next := p.store.Get(name)
	next.CheckedAt = &now
	switch {
	case perr != nil:
		next.State = hosts.StateOffline
		next.Reason = hostReason(scope, "the host did not answer over "+h.Reach.EffectiveTransport(), perr)
	case probed.Empty():
		next.State = hosts.StateUnknown
		next.Reason = "the host answered but reported no usable facts"
	default:
		next.State = hosts.StateOnline
		next.Reason = ""
		next.LastSeen = &now
		next.ProbedAt = &now
		next.Probed = probed
	}
	if err := p.store.Put(name, next); err != nil {
		p.o.Logger.Warn("host.state_write_failed", "host", name, "error", scope.Text(err.Error()))
	}
	return next, nil
}

func (p *Prober) transport(ctx context.Context, h hosts.Host) (func() (*hosts.Probed, error), error) {
	switch h.Reach.EffectiveTransport() {
	case "outpost":
		if h.Reach.Outpost == nil {
			return nil, redact.Guidance("host %q has no Outpost endpoint", h.Name)
		}
		// The token file is read in place (named by absolute path in hosts.yaml, under the
		// adopted root) on every call; the protocol is pkg/outpost's, unchanged.
		client := outpost.Client{URL: h.Reach.Outpost.URL, TokenFile: h.Reach.Outpost.TokenFile,
			Credential: func(token string) { redact.ScopeFrom(ctx).Add("outpost/token", token) }}
		return func() (*hosts.Probed, error) {
			result, err := client.Execute(ctx, outpost.Request{Verb: "probe"})
			if err != nil {
				return nil, err
			}
			if result.ExitCode != 0 || result.Truncated {
				return nil, fmt.Errorf("outpost probe did not complete")
			}
			pr := hosts.ParsePosixProbe(result.Output)
			return &pr, nil
		}, nil
	case "ssh":
		def, err := p.reach(h)
		if err != nil {
			return nil, err
		}
		if p.o.SSH == nil {
			return nil, fmt.Errorf("host %q cannot be probed: %w", h.Name, ErrNoSSH)
		}
		return func() (*hosts.Probed, error) { return p.gather(ctx, def) }, nil
	default:
		return nil, redact.Guidance("host %q has no usable reach transport; configure reach.ssh or reach.outpost", h.Name)
	}
}

// reach resolves a host's ssh resource. A host with no usable reach cannot be probed; that is
// a configuration problem, not a host state.
func (p *Prober) reach(h hosts.Host) (*config.ResourceDef, error) {
	if h.Reach.SSH == "" {
		return nil, redact.Guidance("host %q declares no reach.ssh, so it cannot be probed; set reach.ssh to a server/ssh resource id in hosts.yaml", h.Name)
	}
	if p.o.Resources == nil {
		return nil, redact.Guidance("host %q cannot be probed: no resource configuration is loaded", h.Name)
	}
	def, ok := p.o.Resources(h.Reach.SSH)
	if !ok || def == nil {
		return nil, redact.Guidance("host %q reaches ssh resource %q, which is not in the loaded config; run `cerberus resource list`", h.Name, h.Reach.SSH)
	}
	if def.Connector != "ssh" {
		return nil, redact.Guidance("host %q reaches resource %q, which uses connector %q, not ssh", h.Name, def.ID, def.Connector)
	}
	return def, nil
}

// gather measures the host with plain POSIX commands over ssh. An error means the host could
// not be reached; a host that answers badly yields an empty Probed, not an error.
func (p *Prober) gather(ctx context.Context, def *config.ResourceDef) (*hosts.Probed, error) {
	sshc, err := p.o.SSH(ctx)
	if err != nil {
		return nil, err
	}
	cfg := make(map[string]any, len(def.Config)+2)
	for k, v := range def.Config {
		cfg[k] = v
	}
	cfg["id"], cfg["name"] = def.ID, def.Name
	res := resource.Resource{ID: def.ID, Name: def.Name, Type: resource.Server, Connector: "ssh", Config: cfg}
	out, err := sshc.Exec(ctx, &res, hosts.PosixProbeCommand)
	if err != nil {
		return nil, err
	}
	pr := hosts.ParsePosixProbe(out.Stdout)
	return &pr, nil
}

// hostReason is the reason text stored with an offline host: Cerberus's words, then the cause
// through the request's scope and the rules, bounded.
func hostReason(scope *redact.Scope, lead string, cause error) string {
	text := strings.Join(strings.Fields(scope.Text(cause.Error())), " ")
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return lead + ": " + text
}
