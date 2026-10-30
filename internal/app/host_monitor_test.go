package app

import (
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/config"
)

func TestHostMonitorSettingsDefaultsToOff(t *testing.T) {
	s, err := HostMonitorSettings(nil)
	if err != nil || s.Enabled || s.OutpostInterval != time.Minute || s.SSHInterval != 5*time.Minute || s.Concurrency != 4 {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestHostMonitorSettingsOverridesAndRefusals(t *testing.T) {
	s, err := HostMonitorSettings(&config.HostMonitorConfig{Enabled: true, SSHInterval: "2m", JitterPercent: 20, Concurrency: 2,
		Thresholds: &config.HostMonitorThresholds{DiskFreeBytes: "10GiB", OfflineAfter: 5}})
	if err != nil || !s.Enabled || s.SSHInterval != 2*time.Minute || s.Jitter != 0.2 || s.Concurrency != 2 ||
		s.Thresholds.DiskFreeBytes != 10<<30 || s.Thresholds.OfflineAfter != 5 || s.OutpostInterval != time.Minute {
		t.Fatalf("%+v %v", s, err)
	}
	for name, cfg := range map[string]*config.HostMonitorConfig{
		"bad duration":      {SSHInterval: "soon"},
		"under floor":       {SSHInterval: "5s"},
		"jitter too big":    {JitterPercent: 90},
		"bad size":          {Thresholds: &config.HostMonitorThresholds{DiskFreeBytes: "lots"}},
		"retention shrinks": {SampleRetention: "720h", Rollup5mRetention: "24h"},
	} {
		if _, err := HostMonitorSettings(cfg); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if _, err := HostMonitorSettings(&config.HostMonitorConfig{SSHInterval: "soon"}); !strings.Contains(err.Error(), "host_monitor.ssh_interval") {
		t.Fatalf("the message names the setting: %v", err)
	}
}
