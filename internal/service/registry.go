package service

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/chrispian/cerberus/internal/config"
)

// ServiceRegistry owns the live set of ManagedServices inside a long-running
// process (the daemon, or a standalone `cerberus mcp` session).
//
// The registry is the single place that:
//   - Re-reads the config via its Source on every Reload().
//   - Diffs the new config against the previous snapshot.
//   - Applies the diff (register new, Stop+unregister removed, mark changed
//     as stale) so existing *ManagedService pointers held by MCP tools, the
//     daemon monitor, etc. keep working across reloads.
//
// All three reload entry points — file-watcher, SIGHUP, and `cerberus daemon
// reload` — converge on Reload(). Lifecycle handlers (start/stop/restart/
// rebuild/status) are expected to call Reload() at the top of each operation
// so they always act against fresh-from-disk service definitions.
type ServiceRegistry struct {
	src    config.Source
	logger *slog.Logger

	mu sync.RWMutex
	// services is the authoritative ordered list, mirroring config order.
	services []*ManagedService
	// byID lets us preserve pointer identity across reloads.
	byID map[string]*ManagedService
	// lastCfg is the last-good parsed config, used to compute Diffs.
	lastCfg *config.Config
}

// NewServiceRegistry constructs a registry backed by src. The initial
// snapshot is loaded eagerly; if it fails the registry is returned with the
// load error so the caller can decide whether to proceed with an empty
// state or abort startup.
func NewServiceRegistry(src config.Source, logger *slog.Logger) (*ServiceRegistry, error) {
	if src == nil {
		return nil, fmt.Errorf("registry: source is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	r := &ServiceRegistry{
		src:    src,
		logger: logger,
		byID:   make(map[string]*ManagedService),
	}
	if err := r.Reload(); err != nil {
		return r, err
	}
	return r, nil
}

// Current returns a pointer-slice snapshot of the current services. The
// returned slice is safe to iterate (copied) but the pointers it containsSlug
// are the live ManagedServices — callers should treat them as shared state.
func (r *ServiceRegistry) Current() []*ManagedService {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*ManagedService, len(r.services))
	copy(out, r.services)
	return out
}

// Find returns the ManagedService with the given ID, or nil if not present
// in the current snapshot.
func (r *ServiceRegistry) Find(id string) *ManagedService {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byID[id]
}

// Source returns the underlying config.Source.
func (r *ServiceRegistry) Source() config.Source { return r.src }

// Reload re-reads the config from the source, diffs it against the current
// snapshot, and applies the changes.
//
// Error behavior: on parse/validate failure the prior snapshot is retained
// and the error is logged under config.reload.failed — the daemon keeps
// running with the last-good state, matching the spec's "DO NOT crash the
// daemon" requirement.
func (r *ServiceRegistry) Reload() error {
	newCfg, err := r.src.Snapshot()
	if err != nil {
		r.logger.Error("config.reload.failed",
			"path", r.src.Path(),
			"error", err.Error(),
			"message", "keeping previous in-memory config")
		return fmt.Errorf("reload: %w", err)
	}

	r.mu.Lock()
	diff := config.DiffConfigs(r.lastCfg, newCfg)
	firstLoad := r.lastCfg == nil
	// Capture the pointers for services being removed so we can stop
	// them outside the lock (Stop can block up to 10s for SIGKILL
	// escalation; we don't want to serialize reads behind that).
	toStop := make([]*ManagedService, 0, len(diff.Removed))
	for _, id := range diff.Removed {
		if svc, ok := r.byID[id]; ok {
			toStop = append(toStop, svc)
		}
	}
	// applyDiffLocked returns pointers for services that were newly
	// added AND opted in via auto_start:true. We Start them outside
	// the lock (same reasoning as Stop — Start does I/O and can block).
	//
	// On the first (initial) load everything looks "added" from the
	// diff's perspective, but that code path is owned by the daemon's
	// boot sequence, not by the reload flow this spec covers. Skip
	// auto-start on first load to keep the change scoped to the
	// reload-added behavior called out by the spec review.
	toStart := r.applyDiffLocked(newCfg, diff)
	if firstLoad {
		toStart = nil
	}
	r.lastCfg = newCfg
	r.mu.Unlock()

	// Stop removed services outside the lock.
	for _, svc := range toStop {
		if err := svc.Stop(); err != nil {
			r.logger.Warn("config.reload.stop_removed_failed",
				"service", svc.Def.ID,
				"error", err.Error())
		} else {
			r.logger.Info("config.reload.stopped_removed",
				"service", svc.Def.ID)
		}
	}

	// Auto-start newly-added services that opted in.
	for _, svc := range toStart {
		if err := startService(svc); err != nil {
			r.logger.Warn("config.reload.auto_start_failed",
				"service", svc.Def.ID,
				"error", err.Error())
		} else {
			r.logger.Info("config.reload.auto_started",
				"service", svc.Def.ID)
		}
	}

	// Emit a structured reload event. On first-load we still log so
	// operators can confirm the daemon started with what they expected.
	if firstLoad {
		r.logger.Info("config.reloaded",
			"path", r.src.Path(),
			"initial", true,
			"service_count", len(newCfg.Services))
		return nil
	}
	if diff.IsEmpty() {
		r.logger.Info("config.reloaded",
			"path", r.src.Path(),
			"changed", false)
		return nil
	}
	r.logger.Info("config.reloaded",
		"path", r.src.Path(),
		"added", diff.Added,
		"removed", diff.Removed,
		"changed", diff.Changed)
	return nil
}

// applyDiffLocked must be called with r.mu held.
//
// It rebuilds r.services from newCfg while preserving *ManagedService pointer
// identity for services that still exist. Services whose definition changed
// have their Def updated and are flagged Stale=true so CLI/MCP status output
// can surface the divergence between the running process and the on-disk
// config. Stopping of removed services is performed by the caller of
// Reload() *after* this method returns (outside the lock), to avoid holding
// the registry mutex across a blocking syscall.
//
// Returns the set of freshly-registered services whose ServiceDef.AutoStart
// is true. The caller is responsible for actually calling Start on them,
// outside the lock, for the same reason Stop is deferred.
func (r *ServiceRegistry) applyDiffLocked(newCfg *config.Config, diff config.Diff) []*ManagedService {
	newByID := make(map[string]*ManagedService, len(newCfg.Services))
	newOrder := make([]*ManagedService, 0, len(newCfg.Services))
	var toStart []*ManagedService

	for i := range newCfg.Services {
		def := newCfg.Services[i]
		if def.ID == "" {
			continue
		}
		if existing, ok := r.byID[def.ID]; ok {
			// Preserve pointer identity. If this ID is in the Changed
			// set, update the Def in-place and mark stale so operators
			// see that the running process no longer matches config.
			if containsSlug(diff.Changed, def.ID) {
				existing.Def = def
				existing.Stale = true
			} else {
				existing.Def = def
			}
			newByID[def.ID] = existing
			newOrder = append(newOrder, existing)
			continue
		}
		// Newly registered service. Per spec: auto-register always,
		// auto-start only if def.AutoStart is true. The actual Start()
		// call happens outside the registry mutex (see Reload()).
		fresh := &ManagedService{Def: def}
		newByID[def.ID] = fresh
		newOrder = append(newOrder, fresh)
		if def.AutoStart {
			toStart = append(toStart, fresh)
		}
	}

	r.services = newOrder
	r.byID = newByID
	return toStart
}

// startService is an indirection point so tests can substitute a fake
// starter without spawning real processes. Production code always calls
// ManagedService.Start via this variable.
var startService = func(svc *ManagedService) error { return svc.Start() }

func containsSlug(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
