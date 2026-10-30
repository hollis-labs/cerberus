package app

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/store/sqlite"
)

// HostMonitorSettings resolves the daemon's host_monitor config over the
// defaults and validates the result. A nil config is the defaults, which leave
// monitoring off.
func HostMonitorSettings(cfg *config.HostMonitorConfig) (hosts.MonitorSettings, error) {
	s := hosts.DefaultMonitorSettings()
	if cfg == nil {
		return s, nil
	}
	s.Enabled = cfg.Enabled
	dur := func(name, v string, dst *time.Duration) error {
		if v == "" {
			return nil
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return fmt.Errorf("host_monitor.%s %q is not a positive duration such as 60s", name, v)
		}
		*dst = d
		return nil
	}
	for _, d := range []struct {
		name, v string
		dst     *time.Duration
	}{
		{"outpost_interval", cfg.OutpostInterval, &s.OutpostInterval}, {"ssh_interval", cfg.SSHInterval, &s.SSHInterval},
		{"min_interval", cfg.MinInterval, &s.MinInterval}, {"max_backoff", cfg.MaxBackoff, &s.MaxBackoff},
		{"probe_timeout", cfg.ProbeTimeout, &s.ProbeTimeout}, {"sample_retention", cfg.SampleRetention, &s.SampleRetention},
		{"event_retention", cfg.EventRetention, &s.EventRetention}, {"rollup_5m_retention", cfg.Rollup5mRetention, &s.Rollup5mRetention}, {"rollup_1h_retention", cfg.Rollup1hRetention, &s.Rollup1hRetention},
	} {
		if err := dur(d.name, d.v, d.dst); err != nil {
			return s, err
		}
	}
	if cfg.JitterPercent > 0 {
		s.Jitter = cfg.JitterPercent / 100
	}
	if cfg.Concurrency > 0 {
		s.Concurrency = cfg.Concurrency
	}
	if cfg.SSHConcurrency > 0 {
		s.SSHConcurrency = cfg.SSHConcurrency
	}
	if t := cfg.Thresholds; t != nil {
		th := &s.Thresholds
		if t.DiskFreePercent > 0 {
			th.DiskFreePercent = t.DiskFreePercent
		}
		if t.DiskFreeBytes != "" {
			sz, err := hosts.ParseSize(t.DiskFreeBytes)
			if err != nil {
				return s, fmt.Errorf("host_monitor.thresholds.disk_free_bytes: %w", err)
			}
			th.DiskFreeBytes = int64(sz)
		}
		if t.MemoryAvailablePercent > 0 {
			th.MemoryAvailablePercent = t.MemoryAvailablePercent
		}
		if t.LoadFactor > 0 {
			th.LoadFactor = t.LoadFactor
		}
		if t.SlowProbeFraction > 0 {
			th.SlowProbeFraction = t.SlowProbeFraction
		}
		for _, n := range []struct {
			v   int
			dst *int
		}{{t.LoadSamples, &th.LoadSamples}, {t.SlowProbeSamples, &th.SlowProbeSamples}, {t.OfflineAfter, &th.OfflineAfter},
			{t.DegradeConfirm, &th.DegradeConfirm}, {t.StaleIntervals, &th.StaleIntervals}} {
			if n.v > 0 {
				*n.dst = n.v
			}
		}
	}
	return s, s.Validate()
}

// AttachHostMonitor builds the host monitor over the application database and
// attaches it to the host service, so status, samples and the monitor
// schedule can be read. The returned monitor runs only when the settings
// enable it. Any failure is returned for the daemon to log: monitoring never
// keeps the socket from coming up, and nothing here reads the OS keyring.
func (a *App) AttachHostMonitor(svc *cerbapi.HostService, logger *slog.Logger) (*cerbapi.HostMonitor, error) {
	settings, err := HostMonitorSettings(a.Config.HostMonitor)
	if err != nil {
		return nil, err
	}
	if err := a.OpenStore(); err != nil {
		return nil, fmt.Errorf("open monitoring database: %w", err)
	}
	store, ok := a.Store.(*sqlite.Store)
	if !ok {
		return nil, fmt.Errorf("host monitoring needs the application SQLite store")
	}
	m := cerbapi.NewHostMonitor(svc, hosts.NewSampleStore(store.Core()), settings, logger)
	svc.AttachMonitor(m)
	return m, nil
}
