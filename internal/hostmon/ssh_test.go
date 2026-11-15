package hostmon

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/config"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// fakeSSH answers the probe command as a healthy host would, and records what it ran on.
type fakeSSH struct {
	calls atomic.Int32
	res   atomic.Pointer[resource.Resource]
}

func (f *fakeSSH) Exec(_ context.Context, res *resource.Resource, command string) (*sshconn.ExecResult, error) {
	f.calls.Add(1)
	f.res.Store(res)
	if command != hosts.PosixProbeCommand {
		return nil, context.Canceled
	}
	return &sshconn.ExecResult{Stdout: goodProbeOutput}, nil
}

// With the ssh connector (slice 7), an ssh host is probed as the old build probed it: through
// its reach.ssh resource, the resource's connection settings and its id passed to the
// connector, and the result recorded like any other host's.
func TestMonitorProbesSSHHostsThroughTheConnector(t *testing.T) {
	ctx := context.Background()
	hostsPath, _ := outpostHosts(t, 1)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0).UTC() }
	ssh := &fakeSSH{}
	prober, err := NewProber(ProbeOptions{HostsPath: hostsPath, StatePath: filepath.Join(t.TempDir(), "hosts.json"), Now: now,
		Resources: func(id string) (*config.ResourceDef, bool) {
			if id != "box-ssh" {
				return nil, false
			}
			return &config.ResourceDef{ID: "box-ssh", Name: "box", Type: "server", Connector: "ssh", Config: map[string]any{"host": "box.invalid"}}, true
		},
		SSH: func(context.Context) (SSH, error) { return ssh, nil }})
	if err != nil {
		t.Fatal(err)
	}
	settings := hosts.DefaultMonitorSettings()
	settings.Enabled = true
	ss := sqliteStore(t)
	m := New(Options{Prober: prober, Store: ss, Settings: settings, Now: now, Rand: func() float64 { return 0 }})
	m.tick(ctx)
	clock.Add(int64(settings.SSHInterval / time.Second))
	m.tick(ctx)
	m.wg.Wait()
	if ssh.calls.Load() == 0 {
		t.Fatal("the ssh host was not probed")
	}
	if res := ssh.res.Load(); res == nil || res.ID != "box-ssh" || res.Config["host"] != "box.invalid" || res.Config["id"] != "box-ssh" {
		t.Fatalf("probed %+v, want the reach.ssh resource with its settings", res)
	}
	st, ok, err := ss.Status(ctx, "ssh-box")
	// The fixture's output is a reachable host (its small disk makes it degraded): measured,
	// never offline.
	if err != nil || !ok || st.Status == "offline" || st.LastOK.IsZero() || st.ConsecutiveFailures != 0 {
		t.Fatalf("ssh-box status = %+v %v %v, want it measured", st, ok, err)
	}
}
