package cerbapi

import (
	"context"
	"log/slog"
	"sync"
	"time"

	localconn "github.com/chrispian/cerberus/internal/connector/local"
	"github.com/chrispian/cerberus/internal/domain"
)

// DriftScanInterval is how often the background scan recomputes artifact
// drift for every local process resource.
const DriftScanInterval = 60 * time.Second

// driftEntry is a cached artifact-drift result for one resource.
type driftEntry struct {
	installed   bool
	stale       bool
	staleReason string
	updatedAt   time.Time
}

// DriftCache holds artifact-drift results computed on a slow background
// cadence. The high-fanout list path (ListResources) reads from this cache
// instead of running a live git repo-drift probe per poll, so the resources
// table can surface repo-drift staleness without the per-poll cost that
// InspectArtifactInstallBasic was introduced to avoid.
type DriftCache struct {
	runtime *ResourceRuntimeService
	logger  *slog.Logger
	// ttl bounds how long a cached entry stays usable if the background
	// scan stalls; past it ListResources falls back to the basic probe.
	ttl time.Duration

	mu      sync.RWMutex
	entries map[string]driftEntry

	running bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewDriftCache builds a drift cache bound to a runtime service. Call Run to
// start the background scan and AttachDriftCache to wire it into the runtime.
func NewDriftCache(runtime *ResourceRuntimeService, logger *slog.Logger) *DriftCache {
	if logger == nil {
		logger = slog.Default()
	}
	return &DriftCache{
		runtime: runtime,
		logger:  logger,
		ttl:     3 * DriftScanInterval,
		entries: make(map[string]driftEntry),
		done:    make(chan struct{}),
	}
}

// Run scans drift immediately, then on every tick until ctx is cancelled.
func (d *DriftCache) Run(ctx context.Context) error {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return nil
	}
	d.running = true
	ctx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		d.running = false
		d.mu.Unlock()
		close(d.done)
	}()

	d.logger.Info("daemon.drift_cache.start", "interval", DriftScanInterval.String())

	ticker := time.NewTicker(DriftScanInterval)
	defer ticker.Stop()

	d.scanAll(ctx)
	for {
		select {
		case <-ctx.Done():
			d.logger.Info("daemon.drift_cache.stop")
			return ctx.Err()
		case <-ticker.C:
			d.scanAll(ctx)
		}
	}
}

// Stop cancels the background scan and waits for it to exit.
func (d *DriftCache) Stop() {
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return
	}
	cancel := d.cancel
	done := d.done
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	<-done
}

// scanAll recomputes drift for every local process resource with the full
// (repo-aware) artifact probe.
func (d *DriftCache) scanAll(ctx context.Context) {
	if d.runtime == nil {
		return
	}
	cfg := d.runtime.snapshotConfig()
	if cfg == nil {
		return
	}
	for i := range cfg.Resources {
		select {
		case <-ctx.Done():
			return
		default:
		}
		res := cfg.Resources[i]
		if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
			continue
		}
		spec, err := localconn.SpecFromResourceConfig(res.Config)
		if err != nil {
			continue
		}
		dr := resourceDefToDomain(&res)
		_, art, err := localconn.InspectArtifactInstall(dr, spec)
		if err != nil {
			d.logger.Debug("daemon.drift_cache.inspect_failed", "resource", res.ID, "error", err.Error())
			continue
		}
		d.mu.Lock()
		d.entries[res.ID] = driftEntry{
			installed:   art.Installed,
			stale:       art.Stale,
			staleReason: art.StaleReason,
			updatedAt:   time.Now(),
		}
		d.mu.Unlock()
	}
}

// Lookup returns the cached drift status for a resource. ok is false when
// there is no entry yet (scan not run) or the entry is older than the TTL,
// signalling the caller to fall back to the basic probe.
func (d *DriftCache) Lookup(id string) (localconn.ArtifactStatus, bool) {
	d.mu.RLock()
	entry, found := d.entries[id]
	d.mu.RUnlock()
	if !found {
		return localconn.ArtifactStatus{}, false
	}
	if d.ttl > 0 && time.Since(entry.updatedAt) > d.ttl {
		return localconn.ArtifactStatus{}, false
	}
	return localconn.ArtifactStatus{
		Installed:   entry.installed,
		Stale:       entry.stale,
		StaleReason: entry.staleReason,
	}, true
}
