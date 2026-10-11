package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// probeBackend is the existing fake ssh backend with scripted output.
type probeBackend struct {
	fakeSSHBackend
	connectErr error
	stdout     string
	stderr     string
	execErr    error
}

func (b *probeBackend) Connect(ctx context.Context, host string, port int, user, keyFile string, hk sshconn.HostKeyConfig) error {
	if b.connectErr != nil {
		return b.connectErr
	}
	return b.fakeSSHBackend.Connect(ctx, host, port, user, keyFile, hk)
}

func (b *probeBackend) Exec(_ context.Context, command string) (*sshconn.ExecResult, error) {
	b.execs++
	b.command = command
	if b.execErr != nil {
		return nil, b.execErr
	}
	return &sshconn.ExecResult{Stdout: b.stdout, Stderr: b.stderr}, nil
}

const hostsYAML = `version: 1
hosts:
  - name: box-a
    hostname: box-a.example.test
    tailnet_ip: 100.64.0.10
    lan_ip: 192.0.2.10
    role: worker
    provider: homelab
    labels: {agents: "true"}
    reach: {ssh: box-a-ssh}
    declared: {os: linux, arch: amd64, cpu: 8, memory: 16GiB}
  - name: box-b
    role: service
    labels: {agents: "false"}
  - name: box-c
    reach: {ssh: not-an-ssh-resource}
`

const goodProbeOutput = "os=Linux\narch=x86_64\nkernel=6.1.0-test\ncpu=4\nmem_total_kb=8000000\nmem_available_kb=4000000\n" +
	"load=0.10 0.20 0.30\nuptime_seconds=60\ndisk_kb=1000000 500000\ntool_docker=1\ntool_tmux=0\ntool_tailscale=1\ntool_caddy=0\n"

type hostFixture struct {
	svc     *HostService
	backend *probeBackend
	sink    *audit.Memory
	clock   *time.Time
}

func newHostFixture(t *testing.T, backend *probeBackend) *hostFixture {
	t.Helper()
	dir := t.TempDir()
	hostsPath := filepath.Join(dir, "hosts.yaml")
	if err := os.WriteFile(hostsPath, []byte(hostsYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := audit.NewMemory()
	clock := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	lookup := func(id string) (*config.ResourceDef, bool) {
		switch id {
		case "box-a-ssh":
			return &config.ResourceDef{ID: id, Name: id, Type: "server", Connector: "ssh", Config: map[string]any{"host": "ssh://probe@192.0.2.10", "key_file": "/nonexistent/key"}}, true
		case "not-an-ssh-resource":
			return &config.ResourceDef{ID: id, Type: "container", Connector: "docker"}, true
		}
		return nil, false
	}
	conn := sshconn.NewWithBackendFactory(nil, func() sshconn.Backend { return backend })
	svc := NewHostService(sink, HostServiceOptions{
		HostsPath: hostsPath, StatePath: filepath.Join(dir, "state", "hosts.json"), Resources: lookup,
		SSH: func(context.Context) (HostSSH, error) { return conn, nil },
		Now: func() time.Time { return clock },
	})
	return &hostFixture{svc: svc, backend: backend, sink: sink, clock: &clock}
}

func TestHostListAndGetReportDeclaredWithUnknownState(t *testing.T) {
	f := newHostFixture(t, &probeBackend{})
	views, err := f.svc.List(context.Background(), HostFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 || views[0].Host.Name != "box-a" || views[2].Host.Name != "box-c" {
		t.Fatalf("views = %+v", views)
	}
	a := views[0]
	if a.State != hosts.StateUnknown || !a.Stale || a.Probed != nil || a.Host.Declared.CPU != 8 {
		t.Fatalf("box-a = %+v", a)
	}
	got, err := f.svc.List(context.Background(), HostFilter{Labels: map[string]string{"agents": "true"}})
	if err != nil || len(got) != 1 || got[0].Host.Name != "box-a" {
		t.Fatalf("label filter = %+v, %v", got, err)
	}
	if _, err := f.svc.Get(context.Background(), "nope"); err == nil {
		t.Fatal("unknown host accepted")
	} else {
		var nf *HostNotFoundError
		if !errors.As(err, &nf) {
			t.Fatalf("error type %T", err)
		}
	}
}

func TestHostProbeRecordsFactsApartFromDeclaredAndGoesStale(t *testing.T) {
	f := newHostFixture(t, &probeBackend{stdout: goodProbeOutput})
	v, err := f.svc.Probe(context.Background(), "box-a")
	if err != nil {
		t.Fatal(err)
	}
	if v.State != hosts.StateOnline || v.Stale || v.ProbedAt == nil || v.LastSeen == nil {
		t.Fatalf("view = %+v", v)
	}
	// Declared says 8 cores; the probe measured 4. They are not merged.
	if v.Host.Declared.CPU != 8 || v.Probed == nil || v.Probed.CPU != 4 || v.Probed.Source != "posix" || !v.Probed.Tools["docker"] {
		t.Fatalf("declared/probed = %+v / %+v", v.Host.Declared, v.Probed)
	}
	if f.backend.execs != 1 || f.backend.command != hosts.PosixProbeCommand {
		t.Fatalf("ran %d commands, last %q", f.backend.execs, f.backend.command)
	}
	// Time passes: the same data now reports stale, and persists across a restart.
	*f.clock = f.clock.Add(time.Hour)
	again, err := f.svc.Get(context.Background(), "box-a")
	if err != nil || !again.Stale || again.State != hosts.StateOnline || again.Probed == nil {
		t.Fatalf("after an hour = %+v, %v", again, err)
	}
	got := pairs(t, f.sink.Records())
	if len(got) != 1 {
		t.Fatalf("audit = %+v", got)
	}
	for _, p := range got {
		if p[0].Connector != "host" || p[0].Operation != "probe" || p[0].Effect != "read" {
			t.Fatalf("audit intent = %+v", p[0])
		}
	}
}

func TestHostProbeUnreachableIsAStateNotAnError(t *testing.T) {
	f := newHostFixture(t, &probeBackend{stdout: goodProbeOutput})
	if _, err := f.svc.Probe(context.Background(), "box-a"); err != nil {
		t.Fatal(err)
	}
	f.backend.connectErr = errors.New("dial tcp 192.0.2.10:22: i/o timeout")
	*f.clock = f.clock.Add(10 * time.Minute)
	v, err := f.svc.Probe(context.Background(), "box-a")
	if err != nil {
		t.Fatalf("an unreachable host must not be an error: %v", err)
	}
	if v.State != hosts.StateOffline || !strings.Contains(v.Reason, "did not answer") || !v.Stale {
		t.Fatalf("view = %+v", v)
	}
	// The last measurement is kept, with its own age, not refreshed.
	if v.Probed == nil || v.ProbedAt == nil || !v.ProbedAt.Before(*v.CheckedAt) {
		t.Fatalf("last facts lost: %+v", v)
	}
}

func TestHostProbeWithoutUsableReachIsAnError(t *testing.T) {
	f := newHostFixture(t, &probeBackend{})
	for _, name := range []string{"box-b", "box-c"} {
		if _, err := f.svc.Probe(context.Background(), name); err == nil {
			t.Fatalf("%s probed without a usable ssh reach", name)
		}
	}
	if f.backend.connects != 0 {
		t.Fatal("connected despite an unusable reach")
	}
}

// A credential-shaped value in a host's output or in a transport error does
// not leave in the view.
func TestHostProbeNeverReturnsACredentialShapedValue(t *testing.T) {
	const secret = "sk-live-abcdef0123456789abcdef"
	out := "os=Linux\narch=" + secret + "\nkernel=password=" + secret + "\ncpu=2\nnote=" + secret + "\n"
	f := newHostFixture(t, &probeBackend{stdout: out, stderr: "token=" + secret})
	v, err := f.svc.Probe(context.Background(), "box-a")
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, v, secret)

	// An error echoing a value the request resolved is removed by the scope.
	const resolved = "r3solved-cred-0123456789"
	scope := redact.NewScope()
	scope.Add("test", resolved)
	ctx := redact.WithScope(context.Background(), scope)
	f = newHostFixture(t, &probeBackend{connectErr: errors.New("handshake failed using " + resolved + " and token=" + secret)})
	v, err = f.svc.Probe(ctx, "box-a")
	if err != nil || v.State != hosts.StateOffline {
		t.Fatalf("view = %+v, %v", v, err)
	}
	assertNoSecret(t, v, secret)
	assertNoSecret(t, v, resolved)
}

func assertNoSecret(t *testing.T, v HostView, secret string) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("view carries a credential-shaped value: %s", data)
	}
}

func TestHostIsNotASupervisedResource(t *testing.T) {
	if SupervisedLocally("host", "host") || SupervisedLocally("server", "ssh") {
		t.Fatal("the supervision lane claimed a host")
	}
}

func TestHostsYAMLErrorsSurfaceAsValidationErrors(t *testing.T) {
	f := newHostFixture(t, &probeBackend{})
	bad := "version: 1\nhosts:\n  - name: a\n    tailnet_ip: 198.51.100.1\n  - name: a\n"
	if err := os.WriteFile(f.svc.hostsPath, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.List(context.Background(), HostFilter{})
	var ve *hosts.ValidationError
	if !errors.As(err, &ve) || len(ve.Problems) != 2 {
		t.Fatalf("err = %v", err)
	}
}
