package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chrispian/cerberus/internal/registry"
)

// OverviewSnapshot is one timestamped sample of the control-plane counters
// the overview page renders. Stored chronologically by the
// SnapshotRecorder so the overview API can bucket samples into a trend.
type OverviewSnapshot struct {
	Timestamp         time.Time `json:"timestamp"`
	Resources         int       `json:"resources"`
	Projects          int       `json:"projects"`
	Pipelines         int       `json:"pipelines"`
	Connectors        int       `json:"connectors"`
	Plugins           int       `json:"plugins"`
	Running           int       `json:"running"`
	Attention         int       `json:"attention"`
	Stopped           int       `json:"stopped"`
	ServicesFailed    int       `json:"services_failed"`
	RegistryEntries   int       `json:"registry_entries"`
	RegistryHealthy   int       `json:"registry_healthy"`
	RegistryUnhealthy int       `json:"registry_unhealthy"`
}

// SnapshotFunc captures one snapshot of current counters. The recorder
// invokes it on every tick. Implementations should be quick — they run on
// the daemon's snapshot-recorder goroutine and block the next tick if slow.
type SnapshotFunc func(ctx context.Context) (OverviewSnapshot, error)

// SnapshotRecorderConfig tunes the recorder. Use DefaultSnapshotRecorderConfig
// in production; tests use a faster interval and shorter retention.
type SnapshotRecorderConfig struct {
	// Interval is the sample cadence. Default 1 minute.
	Interval time.Duration
	// Retention is the on-disk retention window. Snapshots older than
	// now-Retention are evicted on read and write. Default 7 days.
	Retention time.Duration
	// Path is the on-disk JSON file. Empty disables persistence (in-memory only).
	Path string
}

// DefaultSnapshotRecorderConfig returns the production defaults: 1-minute
// sample interval and 7-day retention.
func DefaultSnapshotRecorderConfig() SnapshotRecorderConfig {
	return SnapshotRecorderConfig{
		Interval:  time.Minute,
		Retention: 7 * 24 * time.Hour,
	}
}

// SnapshotRecorder periodically samples control-plane counters and persists
// them to disk. The overview API reads the buffer via Recent() and buckets
// the samples into a 24-hourly trend (or any other window the UI asks for).
//
// Concurrency: Run() owns the buffer write side; Recent() is safe to call
// concurrently from any goroutine (the overview HTTP handler does).
type SnapshotRecorder struct {
	cfg    SnapshotRecorderConfig
	logger *slog.Logger
	take   SnapshotFunc

	mu       sync.RWMutex
	snapshots []OverviewSnapshot
}

// NewSnapshotRecorder constructs a recorder with the given config and
// snapshot func. Pass DefaultSnapshotRecorderConfig() unless you need a
// faster cadence (tests).
func NewSnapshotRecorder(cfg SnapshotRecorderConfig, take SnapshotFunc, logger *slog.Logger) *SnapshotRecorder {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.Retention <= 0 {
		cfg.Retention = 7 * 24 * time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SnapshotRecorder{cfg: cfg, take: take, logger: logger}
}

// Run blocks until ctx is cancelled, sampling on the configured interval.
// On startup it loads the persisted buffer from disk; on each tick it
// appends a new snapshot, evicts old entries, and writes the buffer back.
// Sample errors are logged and skipped — the recorder never fails the
// daemon.
func (r *SnapshotRecorder) Run(ctx context.Context) error {
	if r.take == nil {
		return errors.New("snapshot recorder: take func is nil")
	}

	if err := r.load(); err != nil {
		r.logger.Warn("snapshot_recorder.load_failed", "err", err)
	}

	r.tick(ctx)

	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

// Recent returns the snapshots within the last window, oldest → newest.
// Pass 24h to get the trailing day. Safe to call concurrently.
func (r *SnapshotRecorder) Recent(window time.Duration) []OverviewSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if window <= 0 {
		out := make([]OverviewSnapshot, len(r.snapshots))
		copy(out, r.snapshots)
		return out
	}

	cutoff := time.Now().Add(-window)
	idx := sort.Search(len(r.snapshots), func(i int) bool {
		return !r.snapshots[i].Timestamp.Before(cutoff)
	})
	out := make([]OverviewSnapshot, len(r.snapshots)-idx)
	copy(out, r.snapshots[idx:])
	return out
}

func (r *SnapshotRecorder) tick(ctx context.Context) {
	snap, err := r.take(ctx)
	if err != nil {
		r.logger.Warn("snapshot_recorder.take_failed", "err", err)
		return
	}
	if snap.Timestamp.IsZero() {
		snap.Timestamp = time.Now()
	}

	r.mu.Lock()
	r.snapshots = append(r.snapshots, snap)
	r.snapshots = evictOlderThan(r.snapshots, time.Now().Add(-r.cfg.Retention))
	r.mu.Unlock()

	if err := r.persist(); err != nil {
		r.logger.Warn("snapshot_recorder.persist_failed", "err", err)
	}
}

func (r *SnapshotRecorder) load() error {
	if r.cfg.Path == "" {
		return nil
	}
	data, err := os.ReadFile(r.cfg.Path) //nolint:gosec // path comes from trusted cerberus config
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read snapshot file: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	var stored []OverviewSnapshot
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("decode snapshot file: %w", err)
	}
	// Sort by timestamp (defensive — file should already be sorted) and
	// drop anything past the retention window.
	sort.Slice(stored, func(i, j int) bool {
		return stored[i].Timestamp.Before(stored[j].Timestamp)
	})
	stored = evictOlderThan(stored, time.Now().Add(-r.cfg.Retention))

	r.mu.Lock()
	r.snapshots = stored
	r.mu.Unlock()
	return nil
}

func (r *SnapshotRecorder) persist() error {
	if r.cfg.Path == "" {
		return nil
	}
	r.mu.RLock()
	snapshot := make([]OverviewSnapshot, len(r.snapshots))
	copy(snapshot, r.snapshots)
	r.mu.RUnlock()

	if err := os.MkdirAll(filepath.Dir(r.cfg.Path), 0o750); err != nil {
		return fmt.Errorf("create snapshot dir: %w", err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode snapshots: %w", err)
	}
	tmp := r.cfg.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write snapshot file: %w", err)
	}
	if err := os.Rename(tmp, r.cfg.Path); err != nil {
		return fmt.Errorf("rename snapshot file: %w", err)
	}
	return nil
}

func evictOlderThan(snapshots []OverviewSnapshot, cutoff time.Time) []OverviewSnapshot {
	idx := sort.Search(len(snapshots), func(i int) bool {
		return !snapshots[i].Timestamp.Before(cutoff)
	})
	if idx == 0 {
		return snapshots
	}
	// Reuse the backing array; copy keeps the slice header tight.
	out := make([]OverviewSnapshot, len(snapshots)-idx)
	copy(out, snapshots[idx:])
	return out
}

// NewClientSnapshotFunc builds a SnapshotFunc that samples the control-plane
// counters by calling the standard Client surface. Used by the daemon to
// wire the snapshot recorder against the in-process runtime. `configPath`
// is the unified v2 config path — used only to resolve the registry
// summary; pass "" to skip registry stats (they stay zero).
//
// The function tolerates partial failures: a List* call that errors leaves
// its counter at zero and logs the error. A snapshot with all zeros is
// still recorded — the rendered trend will show a gap, which is more
// informative than dropping the sample entirely.
func NewClientSnapshotFunc(client Client, configPath string, logger *slog.Logger) SnapshotFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context) (OverviewSnapshot, error) {
		snap := OverviewSnapshot{Timestamp: time.Now()}

		if health, err := client.Health(ctx, ""); err == nil {
			snap.ServicesFailed = health.ServicesFailed
			runtime := summarizeRuntimeHealth(health.Resources)
			snap.Running = runtime.running
			snap.Attention = runtime.attention
			snap.Stopped = runtime.stopped
		} else {
			logger.Debug("snapshot.health_failed", "err", err)
		}

		if projects, err := client.ListProjects(ctx); err == nil {
			snap.Projects = len(projects)
		} else {
			logger.Debug("snapshot.list_projects_failed", "err", err)
		}
		if resources, err := client.ListResources(ctx, ResourceListArgs{}); err == nil {
			snap.Resources = len(resources)
			// Fall back to per-resource breakdown when Health() didn't
			// populate runtime — keeps a fresh-daemon sample meaningful.
			if snap.Running == 0 && snap.Attention == 0 && snap.Stopped == 0 {
				runtime := summarizeRuntimeResources(resources)
				snap.Running = runtime.running
				snap.Attention = runtime.attention
				snap.Stopped = runtime.stopped
			}
		} else {
			logger.Debug("snapshot.list_resources_failed", "err", err)
		}
		if pipelines, err := client.ListPipelines(ctx); err == nil {
			snap.Pipelines = len(pipelines)
		} else {
			logger.Debug("snapshot.list_pipelines_failed", "err", err)
		}
		if connectors, err := client.ListConnectors(ctx); err == nil {
			snap.Connectors = len(connectors)
		} else {
			logger.Debug("snapshot.list_connectors_failed", "err", err)
		}
		if plugins, err := client.ListManagedPlugins(ctx); err == nil {
			snap.Plugins = len(plugins)
		} else {
			logger.Debug("snapshot.list_plugins_failed", "err", err)
		}

		if configPath != "" {
			if entries, healthy, unhealthy, err := registrySnapshot(configPath); err == nil {
				snap.RegistryEntries = entries
				snap.RegistryHealthy = healthy
				snap.RegistryUnhealthy = unhealthy
			} else {
				logger.Debug("snapshot.registry_failed", "err", err)
			}
		}

		return snap, nil
	}
}

type runtimeSummary struct {
	running   int
	attention int
	stopped   int
}

func summarizeRuntimeHealth(resources []ResourceHealth) runtimeSummary {
	out := runtimeSummary{}
	for _, r := range resources {
		status := strings.ToLower(r.Status)
		switch {
		case r.OperatorStopped || status == "stopped":
			out.stopped++
		case r.RecommendedAction != "" || !r.Healthy || status == "failed" || status == "error" || status == "degraded":
			out.attention++
		case status == "running" || status == "healthy":
			out.running++
		default:
			out.stopped++
		}
	}
	return out
}

func summarizeRuntimeResources(resources []ResourceInfo) runtimeSummary {
	out := runtimeSummary{}
	for _, r := range resources {
		status := strings.ToLower(r.Status)
		switch {
		case r.OperatorStopped || status == "stopped":
			out.stopped++
		case r.ArtifactStale || r.RecommendedAction != "" || status == "failed" || status == "error" || status == "degraded":
			out.attention++
		case status == "running" || status == "healthy":
			out.running++
		default:
			out.stopped++
		}
	}
	return out
}

func registrySnapshot(configPath string) (entries, healthy, unhealthy int, err error) {
	reg, err := registry.ForConfig(configPath)
	if err != nil {
		return 0, 0, 0, err
	}
	list, err := reg.List()
	if err != nil {
		return 0, 0, 0, err
	}
	reports, err := reg.Health()
	if err != nil {
		return 0, 0, 0, err
	}
	entries = len(list)
	for _, report := range reports {
		if report.Healthy() {
			healthy++
		} else {
			unhealthy++
		}
	}
	return entries, healthy, unhealthy, nil
}

// SnapshotStatePath returns the on-disk path for the overview snapshot
// buffer. Mirrors PluginConnectorStatePath's convention.
func SnapshotStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".cerberus", "state")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	return filepath.Join(dir, "overview_snapshots.json"), nil
}

// BucketGaugeTrend buckets gauge-style samples (e.g. resource counts) into
// `buckets` equal-width windows spanning the last `window`. For each
// bucket it returns the value of the most recent sample whose timestamp
// falls in the bucket. Empty buckets are forward-filled with the previous
// bucket's value (so a missing sample doesn't render as a spurious dip);
// buckets before the first sample remain at 0. Returned slice is oldest →
// newest, length == buckets.
//
// This is cerberus's gauge equivalent of Tether's bucketCounts (which
// distributes timestamped events). The semantics differ — gauge value vs.
// event count — but the output shape and bucket math match, so the same
// chart widgets render either kind.
func BucketGaugeTrend(samples []OverviewSnapshot, value func(OverviewSnapshot) int, window time.Duration, buckets int) []int {
	out := make([]int, buckets)
	if buckets <= 0 || window <= 0 || len(samples) == 0 || value == nil {
		return out
	}

	end := time.Now()
	start := end.Add(-window)
	bucketWidth := window / time.Duration(buckets)
	if bucketWidth <= 0 {
		return out
	}

	// Per-bucket latest sample (-1 means no sample seen).
	latestIdx := make([]int, buckets)
	for i := range latestIdx {
		latestIdx[i] = -1
	}
	for i, s := range samples {
		if s.Timestamp.Before(start) || !s.Timestamp.Before(end) {
			continue
		}
		idx := int(s.Timestamp.Sub(start) / bucketWidth)
		if idx >= buckets {
			idx = buckets - 1
		}
		if idx < 0 {
			idx = 0
		}
		// Keep the latest sample in this bucket (samples are
		// chronological so the last write wins).
		latestIdx[idx] = i
	}

	// Forward-fill: each empty bucket inherits the previous bucket's
	// value. A bucket with a sample takes that sample's value.
	carry := 0
	carrySet := false
	for i := 0; i < buckets; i++ {
		if latestIdx[i] >= 0 {
			carry = value(samples[latestIdx[i]])
			carrySet = true
		}
		if carrySet {
			out[i] = carry
		}
	}
	return out
}
