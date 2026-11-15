package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hollis-labs/substrate/module"
	"github.com/hollis-labs/substrate/modules/health"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/datadir"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/hostmon"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/storage"
	"github.com/hollis-labs/cerberus/internal/supervise"
)

// flushTimeout bounds each loop's final flush on stop.
const flushTimeout = 5 * time.Second

// loop is one piece of background work: run until ctx ends, then flush what it holds.
type loop struct {
	name  string
	run   func(ctx context.Context)
	flush func(ctx context.Context)
}

// stage is the loops stopped together, in one step of the stop order.
type stage struct {
	name   string
	loops  []loop
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// background is the work besides HTTP and the resource monitor (plan section 4.4): the
// scheduler (off by default), then the host monitor and janitor, the snapshot recorder and
// event export (off by default). Observe mode starts none of it.
type background struct {
	stages []*stage
	once   sync.Once
}

// hostsPaths are hosts.yaml (the operator's, kept in the adopted root) and the derived-state
// file: in supervise mode new's own, in the data directory; in observe mode the old build's,
// read in place and never written.
func hostsPaths(st settings.Settings, dirs datadir.Dirs) (hostsYAML, state string) {
	hostsYAML = filepath.Join(st.AdoptRoot, "hosts.yaml")
	if st.Mode == settings.ModeSupervise {
		return hostsYAML, filepath.Join(dirs.Data, "state", "hosts.json")
	}
	return hostsYAML, filepath.Join(st.AdoptRoot, "state", "hosts.json")
}

// startBackground starts the background loops in supervise mode and returns them for Stop.
// In observe mode it starts nothing and returns an empty set.
// ssh is the admin lane's ssh connector for the host monitor's ssh hosts (nil: they are skipped).
func startBackground(app *module.App, lane *supervise.Service, st settings.Settings, dirs datadir.Dirs, parts *hostParts,
	logger *slog.Logger) *background {
	b := &background{}
	if st.Mode != settings.ModeSupervise {
		return b
	}
	// The scheduler stops first (plan section 4.4), so it starts first. A scheduler that refuses
	// to start never takes Cerberus down: the refusal is logged and fails its /healthz check.
	sched, err := schedulerLoop(context.Background(), storage.FromApp(app).Local, st)
	if err != nil {
		logger.Error("daemon.scheduler.refused", "error", err.Error())
		if h := health.From(app); h != nil {
			refused := err
			h.Add("scheduler", func(context.Context) error { return refused })
		}
	} else if sched != nil {
		b.start(context.Background(), "scheduler", []loop{*sched})
	}
	loops := append(hostMonitorLoops(app, parts), snapshotLoop(lane, registry.ForRoot(st.AdoptRoot), dirs, logger))
	b.start(context.Background(), "hosts", append(loops, exportLoops(st, dirs, logger)...))
	return b
}

// hostMonitorLoops builds the janitor (always, in supervise mode, so retention never depends on
// probing) and the host monitor (when cerberus.host_monitor.enabled), writing class B through
// the Telemetry store, and adds the telemetry check to /healthz. ssh hosts are probed through the
// admin lane's ssh connector, resolved per probe as the old host service did.
// hostParts are the host monitor's pieces, built once in serve and shared: the background loops
// run them in supervise mode, and the API reads through them (status, samples, events, the
// schedule, probe) in both modes. In observe mode the monitor never runs and nothing is written.
type hostParts struct {
	prober   *hostmon.Prober
	store    *hosts.SampleStore
	tel      *hostmon.Telemetry
	monitor  *hostmon.Monitor
	settings hosts.MonitorSettings
}

// buildHostParts builds the prober over hosts.yaml and the state file (hostsPaths), the sample
// store on the Telemetry store, the bounded telemetry writer and the monitor.
func buildHostParts(app *module.App, g *gate.Gate, st settings.Settings, dirs datadir.Dirs, ssh func(context.Context) (hostmon.SSH, error), logger *slog.Logger) (*hostParts, error) {
	ms, err := hostmon.Settings(st.HostMonitor)
	if err != nil {
		return nil, fmt.Errorf("%s.%w", settings.Key, err)
	}
	stores := storage.FromApp(app)
	hostsYAML, state := hostsPaths(st, dirs)
	reg := registry.ForRoot(st.AdoptRoot)
	prober, err := hostmon.NewProber(hostmon.ProbeOptions{HostsPath: hostsYAML, StatePath: state, Logger: logger,
		Resources: func(id string) (*config.ResourceDef, bool) {
			resolved, rerr := reg.Resolve()
			if rerr != nil {
				return nil, false
			}
			for i := range resolved.Config.Resources {
				if resolved.Config.Resources[i].ID == id {
					return &resolved.Config.Resources[i], true
				}
			}
			return nil, false
		}, SSH: ssh})
	if err != nil {
		return nil, err
	}
	p := &hostParts{prober: prober, store: hosts.NewSampleStore(stores.Telemetry), tel: hostmon.NewTelemetry(0, logger), settings: ms}
	p.monitor = hostmon.New(hostmon.Options{Prober: prober, Store: p.store, Telemetry: p.tel, Settings: ms, Record: hostMonitorRecorder(g, logger), Logger: logger})
	return p, nil
}

// reads is the API's view of the host monitor.
func (p *hostParts) reads() *hostmon.Reads {
	return &hostmon.Reads{Prober: p.prober, Store: p.store, Monitor: p.monitor, Settings: p.settings}
}

// hostMonitorLoops are the janitor (always, in supervise mode, so retention never depends on
// probing) and the host monitor (when cerberus.host_monitor.enabled), writing class B through
// the Telemetry store; the telemetry check joins /healthz.
func hostMonitorLoops(app *module.App, p *hostParts) []loop {
	if h := health.From(app); h != nil {
		h.Add("telemetry", p.tel.Check)
	}
	loops := []loop{{name: "janitor", run: p.monitor.RunJanitor, flush: p.tel.Flush}}
	if p.settings.Enabled {
		loops = append([]loop{{name: "host_monitor", run: p.monitor.Run}}, loops...)
	}
	return loops
}

func (b *background) start(ctx context.Context, name string, loops []loop) {
	ctx, cancel := context.WithCancel(ctx) //nolint:gosec // kept on the stage; Stop calls it
	s := &stage{name: name, loops: loops, cancel: cancel}
	for _, l := range loops {
		s.wg.Add(1)
		go func(l loop) {
			defer s.wg.Done()
			slog.Info("cerberus background start", "loop", l.name)
			l.run(ctx)
		}(l)
	}
	b.stages = append(b.stages, s)
}

// Stop stops each stage in the order it was started, waiting for its loops and flushing them
// before the next stage. It is safe to call more than once.
func (b *background) Stop() {
	b.once.Do(func() {
		for _, s := range b.stages {
			s.cancel()
			s.wg.Wait()
			for _, l := range s.loops {
				if l.flush != nil {
					ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
					l.flush(ctx)
					cancel()
				}
				slog.Info("cerberus background stopped", "loop", l.name)
			}
		}
		if len(b.stages) > 0 {
			slog.Info("cerberus background stopped all")
		}
	})
}

// dataDirs resolves the data directory from the process environment.
func dataDirs() (datadir.Dirs, error) { return datadir.Resolve(os.Getenv) }
