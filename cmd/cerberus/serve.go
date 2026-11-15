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

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/datadir"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/hollis-labs/cerberus/internal/service"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/storage"
	"github.com/hollis-labs/cerberus/internal/supervise"
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
// place and never written, with runtime state from the supervision lane (nil: unknown).
func newAPI(app *module.App, home string, lane *supervise.Service) (*api.API, error) {
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
		Home:    home,
		Mutator: lane,
	})
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
	lane, err := startSupervision(app)
	if err != nil {
		return err
	}
	c.lane = lane
	// The monitor outlives the signal that ends serve: it stops after HTTP, in the order of
	// plan section 4.4, never with it.
	lane.StartMonitor(context.WithoutCancel(ctx), supervise.DefaultMonitorConfig())
	defer lane.Shutdown()
	// The background loops stop after HTTP and before the resource monitor (deferred, so they
	// run before lane.Shutdown).
	bg, err := c.startBackground(app)
	if err != nil {
		return err
	}
	defer bg.Stop()
	id := identity.From(app)
	if id == nil {
		return errors.New("the identity module must be started")
	}
	a, err := newAPI(app, c.home, lane)
	if err != nil {
		return err
	}
	a.RegisterScopes(auth.ScopeTable(app))
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
	// export, each flushed), then the resource monitor, which stops no workload; the modules stop
	// after serve returns.
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

// startBackground starts the background loops for this app's settings (none in observe mode).
func (c *cerberus) startBackground(app *module.App) (*background, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	st, err := settings.Parse(app.Config[settings.Key], home)
	if err != nil {
		return nil, err
	}
	dirs, err := dataDirs()
	if err != nil {
		return nil, err
	}
	return startBackground(app, st, dirs, slog.Default())
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

// startSupervision builds the supervision lane over the descriptors the registry lists
// (<adopt_root>/registry.yaml) and checks the process's own identity: when
// cerberus.self_resource_id is set, the systemd unit the process runs in must be the unit
// that resource installs as. Observe mode writes nothing.
func startSupervision(app *module.App) (*supervise.Service, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cfg, err := settings.Parse(app.Config[settings.Key], home)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	service.SetStateRoot(dirs.Data)
	opts := supervise.Options{Mode: cfg.Mode, Resources: resources, Home: home, StateDir: dirs.Data, SelfID: cfg.SelfResourceID}
	if cfg.Mode == settings.ModeSupervise {
		if err = superviseOptions(app, &opts, reg, dirs.Data, home); err != nil {
			return nil, err
		}
	}
	lane, err := supervise.New(opts)
	if err != nil {
		return nil, err
	}
	list, err := resources()
	if err != nil {
		return nil, fmt.Errorf("read the registry at %s: %w", reg.IndexPath(), err)
	}
	var check supervise.SelfCheck
	if cfg.SelfResourceID != "" {
		cgroup, openErr := supervise.OpenCgroup()
		if openErr != nil {
			return nil, openErr
		}
		var r io.Reader
		if cgroup != nil {
			defer func() { _ = cgroup.Close() }()
			r = cgroup
		}
		if check, err = supervise.CheckSelf(r, cfg.SelfResourceID, list, home); err != nil {
			return nil, err
		}
		if err = check.OutsideUnit(cfg.Mode, cfg.SelfResourceID); err != nil {
			return nil, err
		}
		if check.Unit == "" {
			slog.Warn("cerberus is not running in a systemd unit, so its identity is not checked", "self_resource_id", cfg.SelfResourceID, "expected_unit", check.Expected)
		}
	}
	slog.Info("cerberus supervision", "mode", lane.Mode(), "resources", len(list), "registry", reg.IndexPath(), "self", cfg.SelfResourceID, "unit", check.Unit)
	return lane, nil
}

// superviseOptions adds what supervise mode needs: the registry's config, the class A store,
// the applied-digest key (the audit directory's, in the data dir) and the approver. Until
// slice 6 the approver is the interim one, which cannot enforce a policy, so a configured policy
// refuses the start.
func superviseOptions(app *module.App, opts *supervise.Options, reg *registry.Registry, data, home string) error {
	if dir := policyDir(home); dirHasEntries(dir) {
		return redact.Guidance("cerberus.mode supervise refuses to start: a policy is configured at %s, and this build has no policy gate or approvals yet (slice 6 of the port). "+
			"Run in observe mode, or move the policy aside until the build that enforces it", dir)
	}
	opts.Config = func() (*config.ConfigV2, error) {
		resolved, err := reg.Resolve()
		if err != nil {
			return nil, err
		}
		return resolved.Config, nil
	}
	opts.Store = storage.FromApp(app).Local
	digester, err := supervise.OpenAppliedKey(context.Background(), opts.Store, filepath.Join(data, "audit"), time.Now())
	if err != nil {
		return err
	}
	opts.Digester = digester
	brakes := brake.Store{Dir: filepath.Join(data, "brakes")}
	opts.Brakes = func() brake.State {
		st, _ := brakes.Load()
		return st
	}
	opts.Approver = supervise.InterimApprover{Brakes: opts.Brakes}
	return nil
}

// policyDir is where the operator's policy lives: the config dir, beside the override file
// (plan section 6).
func policyDir(home string) string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "cerberus", "policy")
}

func dirHasEntries(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}
