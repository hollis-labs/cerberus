package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/substrate/module"
	"github.com/hollis-labs/substrate/modules/auth"
	"github.com/hollis-labs/substrate/modules/database"
	"github.com/hollis-labs/substrate/modules/httphost"
	"github.com/hollis-labs/substrate/modules/identity"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/auditchain"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/datadir"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/hostmon"
	cmcp "github.com/hollis-labs/cerberus/internal/mcp"
	"github.com/hollis-labs/cerberus/internal/pipeline"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/hollis-labs/cerberus/internal/service"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/storage"
	"github.com/hollis-labs/cerberus/internal/supervise"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// cerberus is what the commands share: the started framework app. ready, when set, learns
// the bound address (tests listen on port 0).
type cerberus struct {
	app   *module.App
	ready func(net.Addr)
	// lane is the supervision lane. The read API's runtime fields come from it
	// (laneObserver), and L7 and L8 run through it.
	lane *supervise.Service
	// home is the user's home directory (HOME from the environment the app was given).
	home string
}

// newAPI builds the API over the registry and the hosts files in the adopted root, read in
// place and never written, with runtime state from the supervision lane (nil: unknown). g is the
// operation gate and adm the admin lane in supervise mode; both are nil in observe mode.
func newAPI(app *module.App, home string, lane *supervise.Service, g *gate.Gate, adm *admin.Service, reads *hostmon.Reads) (*api.API, error) {
	st, err := settings.Parse(app.Config[settings.Key], home)
	if err != nil {
		return nil, err
	}
	reg := registry.ForRoot(st.AdoptRoot)
	var observer api.Observer
	if lane != nil {
		observer = laneObserver{lane: lane}
	}
	dirs, err := dataDirs()
	if err != nil {
		return nil, err
	}
	hostsYAML, hostsState := hostsPaths(st, dirs)
	return api.New(api.Options{
		Observer: observer,
		Resources: func() ([]config.ResourceDef, error) {
			resolved, err := reg.Resolve()
			if err != nil {
				return nil, err
			}
			return resolved.Config.Resources, nil
		},
		Hosts: api.FileHosts{
			HostsPath: hostsYAML,
			StatePath: hostsState,
		},
		HostReads: reads,
		Projects: func() ([]config.ProjectDef, error) {
			resolved, err := reg.Resolve()
			if err != nil {
				return nil, err
			}
			return resolved.Config.Projects, nil
		},
		Home:    home,
		Mutator: lane,
		Gate:    g,
		Admin:   adm,
		Policy: &api.PolicyOptions{
			Store:       policy.Store{Dir: policyDir(home), AuditDir: filepath.Join(dirs.Data, "audit")},
			Definitions: policyDefinitions(adm),
		},
	})
}

// policyDefinitions are every operation Cerberus can be asked to run, as old's
// defaultPolicyDefinitions listed them: the admin lane's connectors (built-ins and installed
// plugins; none in observe mode), the local runtime and pipelines.
func policyDefinitions(adm *admin.Service) func(context.Context) []contract.Definition {
	return func(context.Context) []contract.Definition {
		var defs []contract.Definition
		if adm != nil {
			defs = adm.Definitions()
		}
		return append(defs, localconn.Definition(), pipeline.Definition())
	}
}

func (c *cerberus) serveCmd(ctx context.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Serve the Cerberus HTTP host",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return c.serve(ctx) },
	}
}

// serve runs the HTTP host until ctx ends (SIGINT or SIGTERM), then drains: Serve stops
// accepting, waits for in-flight handlers and returns; the deferred boot Stop in run then
// stops the modules in reverse order. Background work (supervision, probes, scheduler,
// event export) joins this drain in its own slice, in the stop order of
// docs/substrate-port-plan.md section 4.4.
func (c *cerberus) serve(ctx context.Context) error {
	app := c.app
	if err := requireLoopback(app); err != nil {
		return err
	}
	if database.From(app) == nil {
		return errors.New("the database module must be started")
	}
	sup, err := startSupervision(app)
	if err != nil {
		return err
	}
	lane := sup.lane
	c.lane = lane
	// The admin lane (supervise mode only): its plugins stop at step 5 of plan section 4.4,
	// after the resource monitor, so their stop is deferred before the lane's shutdown.
	adm, err := startAdmin(app, sup.gate, c.home)
	if err != nil {
		return err
	}
	if adm != nil {
		defer adm.Plugins().Stop(context.WithoutCancel(ctx))
	}
	// The monitor and the approvals sweeper outlive the signal that ends serve: they stop after
	// HTTP, in the order of plan section 4.4, never with it. Deferred calls run last first: the
	// sweeper stops (step 3), then the resource monitor (step 4), then the plugins (step 5).
	lane.StartMonitor(context.WithoutCancel(ctx), supervise.DefaultMonitorConfig())
	defer lane.Shutdown()
	defer sup.startSweeper(context.WithoutCancel(ctx))()
	if adm != nil {
		// Once the lane runs, the plugins the state file marks loaded are restored, each recorded
		// as automation via daemon_start. They outlive the signal, like the monitor.
		if err = adm.Plugins().RestorePlugins(context.WithoutCancel(ctx)); err != nil {
			return err
		}
	}
	// The background loops stop after HTTP and before the sweeper and the resource monitor
	// (deferred last, so they run first).
	bg, parts, err := c.startBackground(app, lane, sup.gate, adm)
	if err != nil {
		return err
	}
	defer bg.Stop()
	id := identity.From(app)
	if id == nil {
		return errors.New("the identity module must be started")
	}
	a, err := newAPI(app, c.home, lane, sup.gate, adm, parts.reads())
	if err != nil {
		return err
	}
	a.RegisterScopes(auth.ScopeTable(app))
	// MCP over HTTP (/mcp): the tools call the API's own handlers in-process, under the caller's
	// principal, so the gate, approvals and the person check are the API's.
	if err = cmcp.Register(app, cmcp.MuxBackend{Handler: a.Mux()}); err != nil {
		return err
	}
	handler, err := httphost.Handler(app, httphost.Options{Routes: a.Mux()})
	if err != nil {
		return err
	}
	// Outermost first: the key guard (no machine credential may be minted with approve), then
	// the identity middleware (the session cookie becomes the bearer the guard verifies, and
	// cookie writes need X-Requested-With and a same-origin Origin), then the host (guard,
	// routes).
	handler = api.KeyGuard(id.Middleware(handler))
	// Stop order (plan section 4.4): HTTP stops accepting and drains first; then, deferred, the
	// background loops (scheduler, then host monitor and janitor, snapshot recorder and event
	// export, each flushed), the approvals sweeper, then the resource monitor, which stops no
	// workload, then the plugin processes; the modules stop after serve returns.
	err = httphost.Serve(ctx, app, handler, httphost.ServeOptions{
		Ready: func(addr net.Addr) {
			slog.Info("cerberus listening", "addr", addr.String())
			if c.ready != nil {
				c.ready(addr)
			}
		},
	})
	slog.Info("cerberus http stopped")
	return err
}

// startBackground builds the host monitor's pieces and starts the background loops for this
// app's settings (none in observe mode). adm, in supervise mode, gives the host monitor the ssh
// connector.
func (c *cerberus) startBackground(app *module.App, lane *supervise.Service, g *gate.Gate, adm *admin.Service) (*background, *hostParts, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}
	st, err := settings.Parse(app.Config[settings.Key], home)
	if err != nil {
		return nil, nil, err
	}
	dirs, err := dataDirs()
	if err != nil {
		return nil, nil, err
	}
	reg := registry.ForRoot(st.AdoptRoot)
	lookup := func(id string) (*config.ResourceDef, bool) {
		resolved, rerr := reg.Resolve()
		if rerr != nil {
			return nil, false
		}
		return gate.ConfigResourceLookup(resolved.Config)(id)
	}
	parts, err := buildHostParts(app, g, st, dirs, hostSSH(adm, lookup), slog.Default())
	if err != nil {
		return nil, nil, err
	}
	return startBackground(app, lane, st, dirs, parts, slog.Default()), parts, nil
}

// requireLoopback keeps the host on the loopback interface until identity and the scope
// table land (slice 4); a remote client reaches it through a local TLS proxy.
func requireLoopback(app *module.App) error {
	cfg, _ := app.Config["httphost"].(map[string]any)
	addr, _ := cfg["addr"].(string)
	if addr == "" {
		return errors.New("httphost.addr is empty; set a loopback address such as 127.0.0.1:4786")
	}
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("cerberus requires a loopback httphost.addr (for example 127.0.0.1:4786), got %q", addr)
}

// supervision is what startSupervision built: the lane, and in supervise mode the operation
// gate it and the API pass.
type supervision struct {
	lane *supervise.Service
	gate *gate.Gate
}

// startSweeper starts the approvals sweeper in supervise mode (never in observe mode, which
// writes nothing) and returns what stops it and waits for it.
func (s supervision) startSweeper(ctx context.Context) (stop func()) {
	if s.gate == nil || s.gate.Broker() == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.gate.Broker().RunSweeper(ctx, time.Minute)
	}()
	slog.Info("approvals.sweeper.start")
	return func() {
		cancel()
		<-done
		slog.Info("approvals.sweeper.stop")
	}
}

// startSupervision builds the supervision lane over the descriptors the registry lists
// (<adopt_root>/registry.yaml) and checks the process's own identity: when
// cerberus.self_resource_id is set, the systemd unit the process runs in must be the unit
// that resource installs as. Observe mode writes nothing: it opens no audit chain, approvals
// store or brakes.
func startSupervision(app *module.App) (supervision, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return supervision{}, err
	}
	cfg, err := settings.Parse(app.Config[settings.Key], home)
	if err != nil {
		return supervision{}, err
	}
	reg := registry.ForRoot(cfg.AdoptRoot)
	resources := func() ([]config.ResourceDef, error) {
		resolved, resolveErr := reg.Resolve()
		if resolveErr != nil {
			return nil, resolveErr
		}
		return resolved.Config.Resources, nil
	}
	// Dev-session pid and lock files are new's own, in the data dir: new never reads or writes
	// the old tree's pids/ or locks/ (plan section 3).
	dirs, err := datadir.Resolve(os.Getenv)
	if err != nil {
		return supervision{}, err
	}
	service.SetStateRoot(dirs.Data)
	opts := supervise.Options{Mode: cfg.Mode, Resources: resources, Home: home, StateDir: dirs.Data, SelfID: cfg.SelfResourceID,
		InstallAfterBuild: cfg.Build.InstallAfterBuild}
	if cfg.Mode == settings.ModeSupervise {
		if err = superviseOptions(app, &opts, reg, dirs.Data, home); err != nil {
			return supervision{}, err
		}
	}
	// supervise.New refuses supervise mode without the gate: nothing is wired to mutate ungated.
	lane, err := supervise.New(opts)
	if err != nil {
		return supervision{}, err
	}
	list, err := resources()
	if err != nil {
		return supervision{}, fmt.Errorf("read the registry at %s: %w", reg.IndexPath(), err)
	}
	var check supervise.SelfCheck
	if cfg.SelfResourceID != "" {
		cgroup, openErr := supervise.OpenCgroup()
		if openErr != nil {
			return supervision{}, openErr
		}
		var r io.Reader
		if cgroup != nil {
			defer func() { _ = cgroup.Close() }()
			r = cgroup
		}
		if check, err = supervise.CheckSelf(r, cfg.SelfResourceID, list, home); err != nil {
			return supervision{}, err
		}
		if err = check.OutsideUnit(cfg.Mode, cfg.SelfResourceID); err != nil {
			return supervision{}, err
		}
		if check.Unit == "" {
			slog.Warn("cerberus is not running in a systemd unit, so its identity is not checked", "self_resource_id", cfg.SelfResourceID, "expected_unit", check.Expected)
		}
	}
	slog.Info("cerberus supervision", "mode", lane.Mode(), "resources", len(list), "registry", reg.IndexPath(), "self", cfg.SelfResourceID, "unit", check.Unit)
	return supervision{lane: lane, gate: opts.Gate}, nil
}

// superviseOptions adds what supervise mode needs: the registry's config, the class A store,
// the applied-digest key, and the operation gate the old build's daemon installed
// (internal/app installPolicy and cmd_daemon on main): the audit chain in <data>/audit,
// continued and anchored in the keychain, which shares the digest key with the applied digests;
// the reloading policy from the config dir with the applied snapshot's enforcement; the
// approvals broker in <data>/approvals with the owner-session verifier; the brakes in
// <data>/brakes; and the rate limits.
func superviseOptions(app *module.App, opts *supervise.Options, reg *registry.Registry, data, home string) error {
	opts.Config = func() (*config.ConfigV2, error) {
		resolved, err := reg.Resolve()
		if err != nil {
			return nil, err
		}
		return resolved.Config, nil
	}
	opts.Store = storage.FromApp(app).Local
	auditDir := filepath.Join(data, "audit")
	// The applied-digest key first: its refusals (a missing or different key under migrated
	// digests) name the migration step, before the audit chain would create a key of its own.
	digester, err := supervise.OpenAppliedKey(context.Background(), opts.Store, auditDir, time.Now())
	if err != nil {
		return err
	}
	opts.Digester = digester
	sink, err := auditchain.Open(auditDir)
	if err != nil {
		return fmt.Errorf("open the audit chain in %s: %w", auditDir, err)
	}
	pdp := policy.NewReloading(policy.Store{Dir: policyDir(home), AuditDir: auditDir}, func(status policy.LoadStatus) {
		if recErr := gate.RecordPolicyLoad(sink, status); recErr != nil {
			slog.Error("policy.load_record_failed", "error", redact.Text(recErr.Error()))
		}
		if status.Mismatch() {
			slog.Warn("policy.snapshot_changed", "problem", status.Problem, "enforcement", status.Enforcement,
				"consequence", "the baseline decides until `cerberus policy apply` runs again")
		}
	})
	broker, err := gate.NewBroker(sink, filepath.Join(data, "approvals"))
	if err != nil {
		return fmt.Errorf("open the approvals store: %w", err)
	}
	// The default verifier seals and checks owner-session decisions with the audit chain's digest key.
	broker.SetVerifier(gate.OwnerSessionVerifier{Key: sink})
	opts.Gate = gate.New(gate.Options{
		Sink:        sink,
		Policy:      pdp,
		Enforcement: gate.SnapshotEnforcement{},
		Broker:      broker,
		Brakes:      &gate.Brakes{Store: brake.Store{Dir: filepath.Join(data, "brakes")}, AuditDir: auditDir, Sink: sink},
		Rates:       &gate.RateLimiter{AuditDir: auditDir},
		Logger:      slog.Default(),
	})
	return nil
}

// policyDir is where the operator's policy lives: the config dir, beside the override file
// (plan section 6).
func policyDir(home string) string { return filepath.Join(configDir(home), "policy") }
