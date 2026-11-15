package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/hollis-labs/cerberus/internal/datadir"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/overview"
	"github.com/hollis-labs/cerberus/internal/registry"
	"github.com/hollis-labs/cerberus/internal/supervise"
)

// snapshotSource is what one overview snapshot counts: the supervision lane's view of the local
// workloads and the registry's descriptors.
type snapshotSource struct {
	lane *supervise.Service
	reg  *registry.Registry
}

// take counts as main's NewClientSnapshotFunc did, from the lane and the registry instead of the
// in-process client. A failing part leaves its counters at zero and is logged; the snapshot is
// still recorded. Connectors and plugins stay zero until the admin lane (slice 7) serves them,
// and services_failed is zero as on the new health route.
func (s snapshotSource) take(ctx context.Context) (overview.OverviewSnapshot, error) {
	snap := overview.OverviewSnapshot{Timestamp: time.Now()}
	if statuses, err := s.lane.List(ctx); err == nil {
		for _, st := range statuses {
			if !st.Observed {
				continue
			}
			// main's summarizeRuntimeHealth: healthy means running; anything else not stopped
			// needs attention.
			switch st.State {
			case domain.StateRunning:
				snap.Running++
			case domain.StateStopped:
				snap.Stopped++
			default:
				snap.Attention++
			}
		}
	} else {
		slog.Debug("snapshot.health_failed", "err", err)
	}
	if resolved, err := s.reg.Resolve(); err == nil {
		snap.Projects = len(resolved.Config.Projects)
		snap.Resources = len(resolved.Config.Resources)
		snap.Pipelines = len(resolved.Config.Pipelines)
	} else {
		slog.Debug("snapshot.registry_resolve_failed", "err", err)
	}
	if list, err := s.reg.List(); err == nil {
		snap.RegistryEntries = len(list)
		if reports, herr := s.reg.Health(); herr == nil {
			for _, r := range reports {
				if r.Healthy() {
					snap.RegistryHealthy++
				} else {
					snap.RegistryUnhealthy++
				}
			}
		}
	} else {
		slog.Debug("snapshot.registry_failed", "err", err)
	}
	return snap, nil
}

// snapshotLoop is the overview snapshot recorder over new's own buffer in the data directory
// (<data>/state/overview_snapshots.json; slice 10 copies the old one). It persists on every
// tick, so, as on main, there is nothing to flush at stop.
func snapshotLoop(lane *supervise.Service, reg *registry.Registry, dirs datadir.Dirs, logger *slog.Logger) loop {
	cfg := overview.DefaultSnapshotRecorderConfig()
	cfg.Path = filepath.Join(dirs.Data, "state", "overview_snapshots.json")
	r := overview.NewSnapshotRecorder(cfg, snapshotSource{lane: lane, reg: reg}.take, logger)
	return loop{name: "snapshot_recorder", run: func(ctx context.Context) { _ = r.Run(ctx) }}
}
