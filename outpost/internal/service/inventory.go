package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/hollis-labs/libs/app/app-host/plugins"
	pluginhost "github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-host"
)

// Each plugin retains the shared driver's process identity and teardown rules.
// Optional admission is an Outpost policy, not a change to shared Inventory.
type managedPlugin interface {
	Start(context.Context) error
	Stop(context.Context) error
	Health(context.Context) error
	APIRoutes() http.Handler
}

type pluginRuntime struct {
	*plugins.Inventory
	id   string
	dirs []string
}

func newPluginRuntime(spec pluginhost.Spec) managedPlugin {
	return &pluginRuntime{Inventory: plugins.NewInventory([]pluginhost.Spec{spec}), id: spec.ID,
		dirs: []string{spec.Init.DataDir, spec.Init.CacheDir}}
}

func (p *pluginRuntime) Start(ctx context.Context) error {
	for _, dir := range p.dirs {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	return p.Inventory.Start(ctx)
}

func (p *pluginRuntime) Health(ctx context.Context) error {
	c, ok := p.Client(p.id)
	if !ok {
		return errors.New("plugin unavailable")
	}
	status, err := c.Health(ctx)
	if err != nil {
		return err
	}
	if !status.OK {
		return errors.New("plugin reported unhealthy")
	}
	return nil
}

type pluginEntry struct {
	id       string
	optional bool
	runtime  managedPlugin
	healthy  bool // guarded by inventory.mu
}

type inventory struct {
	mu      sync.RWMutex
	entries []*pluginEntry // immutable after construction
}

// Lifecycle calls are serialized by Run; HTTP readers share only health state.
func (i *inventory) setHealthy(p *pluginEntry, healthy bool) {
	i.mu.Lock()
	p.healthy = healthy
	i.mu.Unlock()
}

func (i *inventory) isHealthy(p *pluginEntry) bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return p.healthy
}

func checkPlugin(ctx context.Context, p *pluginEntry) error {
	check, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return p.runtime.Health(check)
}

func (i *inventory) Start(ctx context.Context) error {
	for _, p := range i.entries {
		err := p.runtime.Start(ctx)
		if err == nil {
			err = checkPlugin(ctx, p)
		}
		if ctx.Err() != nil {
			_ = i.Stop(context.Background())
			return ctx.Err()
		}
		if err != nil {
			// Never print plugin errors: they can include configuration secrets.
			slog.Error("plugin admission failed", "plugin", p.id, "optional", p.optional)
			stopErr := p.runtime.Stop(context.Background())
			if !p.optional || stopErr != nil {
				_ = i.Stop(context.Background())
				return fmt.Errorf("plugin %s admission or cleanup failed", p.id)
			}
			continue
		}
		i.setHealthy(p, true)
	}
	return nil
}

func (i *inventory) Check(ctx context.Context) error {
	for _, p := range i.entries {
		if !i.isHealthy(p) {
			continue
		}
		err := checkPlugin(ctx, p)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			continue
		}
		i.setHealthy(p, false) // refuse new requests before teardown
		slog.Error("plugin unhealthy", "plugin", p.id, "optional", p.optional)
		if !p.optional {
			return fmt.Errorf("required plugin %s unhealthy", p.id)
		}
		if err := p.runtime.Stop(ctx); err != nil {
			return fmt.Errorf("plugin %s cleanup failed", p.id)
		}
	}
	return nil
}

func (i *inventory) Stop(ctx context.Context) error {
	var result error
	for _, p := range i.entries {
		i.setHealthy(p, false)
		if err := p.runtime.Stop(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("plugin %s cleanup failed", p.id))
		}
	}
	return result
}

func (i *inventory) APIRoutes() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := pluginID(r.URL.Path)
		for _, p := range i.entries {
			if p.id != id {
				continue
			}
			if !i.isHealthy(p) {
				http.Error(w, "plugin unavailable; inspect host logs and restart after repair", http.StatusServiceUnavailable)
				return
			}
			p.runtime.APIRoutes().ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

// Readiness is deliberately stricter than liveness: even optional failures
// reject deployment acceptance. No plugin names, config or errors are public.
func (i *inventory) Readiness(w http.ResponseWriter, _ *http.Request) {
	i.mu.RLock()
	healthy := 0
	for _, p := range i.entries {
		if p.healthy {
			healthy++
		}
	}
	i.mu.RUnlock()
	status := "ok"
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if healthy != len(i.entries) || len(i.entries) == 0 {
		status = "degraded"
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(struct {
		Status  string `json:"status"`
		Plugins int    `json:"plugins"`
		Healthy int    `json:"healthy"`
		PID     int    `json:"pid"`
	}{status, len(i.entries), healthy, os.Getpid()})
}
