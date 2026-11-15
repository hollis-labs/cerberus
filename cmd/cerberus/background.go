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
	"github.com/hollis-labs/cerberus/internal/hostmon"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/storage"
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
func startBackground(app *module.App, st settings.Settings, dirs datadir.Dirs, logger *slog.Logger) (*background, error) {
	b := &background{}
	if st.Mode != settings.ModeSupervise {
		return b, nil
	}
	hostLoops, err := hostMonitorLoops(app, st, dirs, logger)
	if err != nil {
		return nil, err
	}
	b.start(context.Background(), "hosts", hostLoops)
	return b, nil
}

// hostMonitorLoops builds the janitor (always, in supervise mode, so retention never depends on
// probing) and the host monitor (when cerberus.host_monitor.enabled), writing class B through
// the Telemetry store, and adds the telemetry check to /healthz.
func hostMonitorLoops(app *module.App, st settings.Settings, dirs datadir.Dirs, logger *slog.Logger) ([]loop, error) {
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
		}})
	if err != nil {
		return nil, err
	}
	tel := hostmon.NewTelemetry(0, logger)
	if h := health.From(app); h != nil {
		h.Add("telemetry", tel.Check)
	}
	m := hostmon.New(hostmon.Options{Prober: prober, Store: hosts.NewSampleStore(stores.Telemetry), Telemetry: tel, Settings: ms, Logger: logger})
	loops := []loop{{name: "janitor", run: m.RunJanitor, flush: tel.Flush}}
	if ms.Enabled {
		loops = append([]loop{{name: "host_monitor", run: m.Run}}, loops...)
	}
	return loops, nil
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
