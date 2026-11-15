package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/config"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// memKeyring is an in-memory credential store.
type memKeyring map[string]string

func (m memKeyring) Get(_ context.Context, service, key string) (string, error) {
	return m[service+"/"+key], nil
}
func (m memKeyring) Set(_ context.Context, service, key, value string) error {
	m[service+"/"+key] = value
	return nil
}
func (m memKeyring) Delete(_ context.Context, service, key string) error {
	delete(m, service+"/"+key)
	return nil
}

// probeConn is an ssh transport that records the credential scope it was reached under.
type probeConn struct{ scope *secrets.CredentialScope }

func (p *probeConn) Connect(ctx context.Context, _ string, _ int, _ string, _ string, _ sshconn.HostKeyConfig) error {
	if s, ok := secrets.CredentialScopeFrom(ctx); ok {
		*p.scope = s
	}
	return nil
}
func (p *probeConn) Exec(context.Context, string) (*sshconn.ExecResult, error) {
	return &sshconn.ExecResult{Stdout: "os=Linux\n"}, nil
}
func (p *probeConn) Ping(context.Context) error                         { return nil }
func (p *probeConn) Put(context.Context, string, string) (int64, error) { return 0, nil }
func (p *probeConn) Get(context.Context, string, string) (int64, error) { return 0, nil }
func (p *probeConn) PutDir(context.Context, string, string) (*sshconn.DirTransferResult, error) {
	return nil, nil
}
func (p *probeConn) GetDir(context.Context, string, string) (*sshconn.DirTransferResult, error) {
	return nil, nil
}
func (p *probeConn) Close() error { return nil }

// The host monitor reaches ssh hosts through the admin lane's ssh connector, each probe's
// credentials resolved as a read of its ssh resource (the old host service's scope).
func TestTheHostMonitorProbesSSHThroughTheAdminLane(t *testing.T) {
	root := filepath.Join(t.TempDir(), "adopted")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	box := config.ResourceDef{ID: "box", Name: "box", Type: "server", Connector: "ssh", Env: "prod", Config: map[string]any{"host": "box.invalid", "key_file": "/keys/box"}}
	lookup := func(id string) (*config.ResourceDef, bool) { return &box, id == "box" }
	var scope secrets.CredentialScope
	adm, err := admin.New(admin.Options{Gate: gatetest.New(t, gatetest.Options{}).Gate, Paths: admin.Paths{AdoptRoot: root}, Keyring: memKeyring{},
		Resources: lookup, SSHBackend: func() sshconn.Backend { return &probeConn{scope: &scope} }})
	if err != nil {
		t.Fatal(err)
	}
	if hostSSH(nil, lookup) != nil {
		t.Fatal("observe mode has no ssh connector, so ssh hosts are skipped")
	}
	ssh, err := hostSSH(adm, lookup)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out, err := ssh.Exec(context.Background(), &resource.Resource{ID: "box", Type: resource.Server, Connector: "ssh", Config: box.Config}, hosts.PosixProbeCommand)
	if err != nil || out.Stdout != "os=Linux\n" {
		t.Fatalf("probe = %+v, %v", out, err)
	}
	if scope.Access != secrets.AccessRead || scope.Target.ID != "box" || scope.Target.Env != "prod" {
		t.Fatalf("the probe's credential scope = %+v, want a read of box", scope)
	}
}
