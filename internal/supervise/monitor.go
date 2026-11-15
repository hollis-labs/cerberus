package supervise

import (
	"context"
	"time"

	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/storage"
	"github.com/hollis-labs/cerberus/internal/target"
)

// MonitorConfig is the resource monitor's cadence and restart budget (the old build's
// ResourceMonitorConfig).
type MonitorConfig struct {
	CheckInterval             time.Duration
	DefaultMaxRestartAttempts int
	DefaultRestartCooldown    time.Duration
}

// DefaultMonitorConfig is the old build's default.
func DefaultMonitorConfig() MonitorConfig {
	return MonitorConfig{CheckInterval: 30 * time.Second, DefaultMaxRestartAttempts: 3, DefaultRestartCooldown: 10 * time.Second}
}

// monitor restarts dev sessions with auto_restart that went down, as the old build's resource
// monitor did: not while paused or frozen, not past the restart budget or inside the cooldown,
// and only the definition last applied. It never stops a workload, on exit or otherwise.
type monitor struct {
	s   *Service
	cfg MonitorConfig

	cancel context.CancelFunc
	done   chan struct{}

	failureCount map[string]int
	lastRestart  map[string]time.Time
	lastError    map[string]string
}

// StartMonitor starts the resource monitor in supervise mode; observe mode starts nothing. It
// returns at once; Shutdown stops it.
func (s *Service) StartMonitor(ctx context.Context, cfg MonitorConfig) {
	if s.o.Mode != settings.ModeSupervise {
		return
	}
	if cfg.CheckInterval <= 0 {
		cfg = DefaultMonitorConfig()
	}
	ctx, cancel := context.WithCancel(ctx)
	m := &monitor{s: s, cfg: cfg, cancel: cancel, done: make(chan struct{}),
		failureCount: map[string]int{}, lastRestart: map[string]time.Time{}, lastError: map[string]string{}}
	s.monMu.Lock()
	s.mon = m
	s.monMu.Unlock()
	go m.run(ctx)
}

// Shutdown stops the lane's background work and waits for it (plan section 4.4, step 4). It
// stops no workload: an os_service unit outlives this process by design, and so does a dev
// session.
func (s *Service) Shutdown() {
	s.monMu.Lock()
	m := s.mon
	s.mon = nil
	s.monMu.Unlock()
	if m == nil {
		return
	}
	m.cancel()
	<-m.done
	s.o.Logger.Info("daemon.resource_monitor.stop")
}

func (m *monitor) run(ctx context.Context) {
	defer close(m.done)
	m.s.o.Logger.Info("daemon.resource_monitor.start", "interval", m.cfg.CheckInterval.String())
	ticker := time.NewTicker(m.cfg.CheckInterval)
	defer ticker.Stop()
	m.checkAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkAll(ctx)
		}
	}
}

func shouldMonitor(res config.ResourceDef, spec localconn.ProcessSpec) bool {
	if res.Type != string(domain.ResourceProcess) || res.Connector != "local" {
		return false
	}
	return isDevSession(spec) && spec.AutoRestart
}

func (m *monitor) checkAll(ctx context.Context) {
	cfg, err := m.s.config()
	if err != nil {
		m.s.o.Logger.Warn("daemon.resource_monitor.config", "error", err.Error())
		return
	}
	monitored := map[string]bool{}
	for _, res := range cfg.Resources {
		if spec, err := localconn.SpecFromResourceConfig(res.Config); err == nil && shouldMonitor(res, spec) {
			monitored[res.ID] = true
		}
	}
	// A resource no longer monitored must not keep an exhausted budget if it comes back.
	for _, table := range []map[string]int{m.failureCount} {
		for id := range table {
			if !monitored[id] {
				delete(table, id)
			}
		}
	}
	for id := range m.lastRestart {
		if !monitored[id] {
			delete(m.lastRestart, id)
		}
	}
	for id := range m.lastError {
		if !monitored[id] {
			delete(m.lastError, id)
		}
	}
	ordered, warnings := startupOrder(cfg.Resources)
	for _, w := range warnings {
		m.s.o.Logger.Warn("daemon.resource_monitor.dependencies", "warning", w)
	}
	for i := range ordered {
		if ctx.Err() != nil {
			return
		}
		spec, err := localconn.SpecFromResourceConfig(ordered[i].Config)
		if err != nil || !shouldMonitor(ordered[i], spec) {
			continue
		}
		m.check(ctx, cfg, ordered[i], spec)
	}
}

func stateDown(st domain.State) bool {
	return st == domain.StateStopped || st == domain.StateFailed || st == domain.StateUnknown
}

func (m *monitor) check(ctx context.Context, cfg *config.ConfigV2, res config.ResourceDef, spec localconn.ProcessSpec) {
	log := m.s.o.Logger
	if p, err := storage.Paused(ctx, m.s.o.Store, res.ID); err != nil || p {
		if p {
			log.Info("daemon.resource_monitor.paused", "resource", res.ID)
		}
		return
	}
	dr := ResourceToDomain(&res)
	m.s.opMu.Lock()
	state, err := m.s.conn.Status(ctx, dr)
	m.s.opMu.Unlock()
	if err != nil {
		m.lastError[res.ID] = err.Error()
		return
	}
	if !stateDown(state) {
		if m.failureCount[res.ID] > 0 {
			log.Info("daemon.resource_monitor.resource_recovered", "resource", res.ID)
			m.failureCount[res.ID], m.lastError[res.ID] = 0, ""
		}
		return
	}
	if conflict := refusePortConflict(cfg, res.ID); conflict != nil {
		m.lastError[res.ID] = conflict.Error()
		log.Warn("daemon.resource_monitor.port_conflict", "resource", res.ID, "error", conflict.Error())
		return
	}
	maxAttempts := m.cfg.DefaultMaxRestartAttempts
	if spec.MaxRestartAttempts > 0 {
		maxAttempts = spec.MaxRestartAttempts
	}
	if m.failureCount[res.ID] >= maxAttempts {
		log.Warn("daemon.resource_monitor.max_restarts_exceeded", "resource", res.ID, "failure_count", m.failureCount[res.ID], "last_error", m.lastError[res.ID], "max", maxAttempts)
		return
	}
	cooldown := m.cfg.DefaultRestartCooldown
	if spec.RestartCooldown != "" {
		if d, parseErr := time.ParseDuration(spec.RestartCooldown); parseErr == nil && d > 0 {
			cooldown = d
		}
	}
	if last, ok := m.lastRestart[res.ID]; ok && time.Since(last) < cooldown {
		return
	}
	// A freeze pauses restarts; a lockdown does not, since they restore declared state.
	if f, frozen := m.frozen(res); frozen {
		log.Warn("daemon.resource_monitor.restart_skipped", "resource", res.ID, "freeze", f.ID, "reason", "the resource is frozen; lift it with `cerberus freeze --off "+f.ID+"`")
		return
	}
	// Only the definition last applied restarts; one edited since waits for an apply. A
	// resource with no record is taken as it is.
	digest := m.s.o.Digester.Digest(res)
	if prev, ok, readErr := storage.Applied(ctx, m.s.o.Store, res.ID); readErr == nil && ok && prev != digest {
		m.lastError[res.ID] = "its definition changed since it was last applied, so the monitor does not restart it; run `cerberus resource apply " + res.ID + "`"
		log.Warn("daemon.resource_monitor.restart_skipped", "resource", res.ID, "reason", m.lastError[res.ID])
		return
	} else if readErr == nil && !ok {
		if setErr := storage.SetApplied(ctx, m.s.o.Store, res.ID, digest, time.Now()); setErr != nil {
			log.Warn("daemon.resource_monitor.applied_record_failed", "resource", res.ID, "error", setErr.Error())
		}
	}
	for _, w := range m.s.dependencyWarnings(ctx, cfg, &res) {
		log.Warn("daemon.resource_monitor.dependency_unavailable", "resource", res.ID, "warning", w)
	}
	m.failureCount[res.ID]++
	m.lastRestart[res.ID] = time.Now()
	log.Info("daemon.resource_monitor.restart_attempt", "resource", res.ID, "attempt", m.failureCount[res.ID], "max", maxAttempts)
	m.s.opMu.Lock()
	_, err = m.s.conn.Apply(ctx, dr)
	m.s.opMu.Unlock()
	if err != nil {
		m.lastError[res.ID] = err.Error()
		log.Warn("daemon.resource_monitor.restart_failed", "resource", res.ID, "attempt", m.failureCount[res.ID], "error", err.Error())
		return
	}
	m.lastError[res.ID] = ""
	log.Info("daemon.resource_monitor.restart_success", "resource", res.ID, "attempt", m.failureCount[res.ID])
}

// frozen is the freeze covering a resource, from the lane's brake source.
func (m *monitor) frozen(res config.ResourceDef) (brake.Freeze, bool) {
	if m.s.o.Brakes == nil {
		return brake.Freeze{}, false
	}
	labels := res.TargetLabels()
	op, _ := operation(localconn.OpApply)
	return m.s.o.Brakes().FreezeCovering("local", target.Resolve("local", op.Target.Kind, res.ID, &labels, false))
}

// startupOrder orders every resource after its dependencies (the old build's
// resourceStartupOrder over the whole set), with any cycle named.
func startupOrder(resources []config.ResourceDef) ([]config.ResourceDef, []string) {
	byID := make(map[string]config.ResourceDef, len(resources))
	for _, r := range resources {
		byID[r.ID] = r
	}
	state := map[string]int{}
	var ordered []config.ResourceDef
	var warnings []string
	var visitAll func(id string, stack []string)
	visitAll = func(id string, stack []string) {
		if state[id] == 2 {
			return
		}
		if state[id] == 1 {
			warnings = append(warnings, "dependency cycle: "+joinPath(append(stack, id)))
			return
		}
		r, ok := byID[id]
		if !ok {
			return
		}
		state[id] = 1
		for _, dep := range r.DependsOn {
			visitAll(dep, append(stack, id))
		}
		state[id] = 2
		ordered = append(ordered, r)
	}
	for _, r := range resources {
		visitAll(r.ID, nil)
	}
	return ordered, warnings
}

func joinPath(ids []string) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += " -> "
		}
		out += id
	}
	return out
}
