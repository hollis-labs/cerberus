package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
	"github.com/zalando/go-keyring"

	"github.com/hollis-labs/cerberus/internal/admin"
	"github.com/hollis-labs/cerberus/internal/admin/admintest"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/brake"
	"github.com/hollis-labs/cerberus/internal/config"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/storage"
	"github.com/hollis-labs/cerberus/internal/target"
	"github.com/hollis-labs/cerberus/internal/testhome"
)

// echoBinary is the synthetic third-party plugin, built once.
var echoBinary string

// TestMain replaces the OS credential store with an in-memory mock (these tests write the
// keyring), builds the synthetic plugin with the toolchain's caches, then runs under a scratch
// home.
func TestMain(m *testing.M) {
	secrets.MockStoreForTests()
	dir, err := os.MkdirTemp("", "admin-plugin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if echoBinary, err = admintest.BuildEcho(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	cleanup, err := testhome.Isolate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// fakeSSH is the ssh connector's transport: it records what it was asked and reaches nothing.
type fakeSSH struct {
	mu    sync.Mutex
	hosts []string
	cmds  []string
	keys  []string
}

func (f *fakeSSH) factory() sshconn.Backend { return &fakeConn{f: f} }

type fakeConn struct{ f *fakeSSH }

func (c *fakeConn) Connect(_ context.Context, host string, _ int, _ string, keyFile string, _ sshconn.HostKeyConfig) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.hosts, c.f.keys = append(c.f.hosts, host), append(c.f.keys, keyFile)
	return nil
}

func (c *fakeConn) Exec(_ context.Context, command string) (*sshconn.ExecResult, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.cmds = append(c.f.cmds, command)
	return &sshconn.ExecResult{Stdout: "ran: " + command}, nil
}
func (c *fakeConn) Ping(context.Context) error { return nil }
func (c *fakeConn) Put(context.Context, string, string) (int64, error) {
	return 0, nil
}
func (c *fakeConn) Get(_ context.Context, _, localPath string) (int64, error) {
	return 2, os.WriteFile(localPath, []byte("ok"), 0o600)
}
func (c *fakeConn) PutDir(context.Context, string, string) (*sshconn.DirTransferResult, error) {
	return &sshconn.DirTransferResult{}, nil
}
func (c *fakeConn) GetDir(context.Context, string, string) (*sshconn.DirTransferResult, error) {
	return &sshconn.DirTransferResult{}, nil
}
func (c *fakeConn) Close() error { return nil }

// lane is an admin lane over a scratch adopted root and config dir, a real gate, a migrated
// class A store for the receipts, and a fake ssh transport.
type lane struct {
	svc   *admin.Service
	world *gatetest.World
	root  string
	cfg   string
	st    *store.Store
	ssh   *fakeSSH
	res   map[string]config.ResourceDef
}

// resources are the registry the lane resolves: a production ssh host and the echo plugin's
// production target.
func resources() map[string]config.ResourceDef {
	return map[string]config.ResourceDef{
		"box": {ID: "box", Name: "box", Type: "server", Connector: "ssh", Env: target.EnvProd, Owner: target.OwnerSelf,
			Admin: target.Admin{Default: target.AdminSelf}, Config: map[string]any{"host": "box.invalid", "user": "ops", "key_file": "/keys/box"}},
		"db": {ID: "db", Name: "db", Type: "service", Connector: "echo", Env: target.EnvProd, Owner: target.OwnerSelf,
			Admin: target.Admin{Default: target.AdminSelf}},
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	cfg := store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: filepath.Join(t.TempDir(), "cerberus.db"), CreateParentDir: true}}
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fsys, root := storage.Migrations()
	if err := migrate.Ensure(ctx, cfg, st, migrate.Source{FS: fsys, Root: root}); err != nil {
		t.Fatal(err)
	}
	return st
}

// newLane builds the lane over root (a fresh one when empty) and world (a fresh gate when nil).
func newLane(t *testing.T, root string, world *gatetest.World) *lane {
	t.Helper()
	if root == "" {
		root = filepath.Join(t.TempDir(), "adopted")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if world == nil {
		world = gatetest.New(t, gatetest.Options{})
	}
	l := &lane{world: world, root: root, cfg: filepath.Join(t.TempDir(), "config", "cerberus"), st: openStore(t), ssh: &fakeSSH{}, res: resources()}
	svc, err := admin.New(admin.Options{
		Gate:  world.Gate,
		Paths: admin.Paths{AdoptRoot: root, ConfigDir: l.cfg},
		Resources: func(id string) (*config.ResourceDef, bool) {
			def, ok := l.res[id]
			if !ok {
				return nil, false
			}
			return &def, true
		},
		Receipts:    admin.StoreRecorder{Store: l.st},
		HostVersion: "test",
		Stderr:      &bytes.Buffer{},
		SSHBackend:  l.ssh.factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Plugins().Stop(context.Background()) })
	l.svc = svc
	return l
}

// install puts the echo plugin in a fresh adopted root and returns it.
func install(t *testing.T, p admintest.Plugin) (string, admintest.Installed) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "adopted")
	in, err := admintest.Install(root, echoBinary, p)
	if err != nil {
		t.Fatal(err)
	}
	return root, in
}

// owner is the owner's console session, which the gate classes a person.
func owner() context.Context {
	return gate.WithPrincipal(gate.BeginRequest(context.Background(), gate.SurfaceWeb), gate.WebSessionPrincipal("s1"))
}

// An existing third-party-style plugin, installed in an adopted root as the old build left it,
// loads unchanged: restored at start as automation via daemon_start with its bundle digest
// checked, it answers an operation, and the shared state file is not rewritten by the restore
// or by stopping at exit.
func TestAThirdPartyPluginLoadsFromTheAdoptedRoot(t *testing.T) {
	root, in := install(t, admintest.Plugin{Loaded: true})
	statePath := filepath.Join(root, "plugin-connectors.json")
	before, err := os.ReadFile(statePath) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatal(err)
	}
	l := newLane(t, root, nil)
	if l.svc.Plugins().Loaded("echo") {
		t.Fatal("a plugin loaded at construction; only RestorePlugins loads")
	}
	if err = l.svc.Plugins().RestorePlugins(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !l.svc.Plugins().Loaded("echo") {
		t.Fatal("the plugin did not load from the adopted root")
	}
	list := l.svc.Plugins().List(context.Background())
	if len(list) != 1 || list[0].BundleDigest != in.Digest || list[0].ReviewPending || list[0].Path != in.Path {
		t.Fatalf("list = %+v, want the reviewed bundle %s at %s", list, in.Digest, in.Path)
	}
	var restored bool
	for _, r := range l.world.Records(audit.KindIntent) {
		if r.Connector == "plugin" && r.Operation == "load" && r.Principal.Kind == audit.PrincipalAutomation &&
			strings.Contains(r.Reason, "bundle "+in.Digest+" checked against its accepted review") {
			restored = true
		}
	}
	if !restored {
		t.Fatalf("no automation record of the restore with its digest checked: %+v", l.world.Records(audit.KindIntent))
	}
	out, err := l.svc.Execute(owner(), admin.OperationArgs{Connector: "echo", Operation: "echo", Config: map[string]any{"message": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(out.Data)
	if !strings.Contains(string(data), `"tool":"cerberus_echo_echo"`) || !strings.Contains(string(data), `"message":"hi"`) {
		t.Fatalf("the plugin's answer = %s", data)
	}
	if len(admintest.Marks(root, "loaded")) != 1 {
		t.Fatal("the plugin's data directory is not under the adopted root's plugin-data")
	}
	l.svc.Plugins().Stop(context.Background())
	if len(admintest.Marks(root, "unloaded")) != 1 {
		t.Fatal("Stop did not unload the plugin")
	}
	if l.svc.Plugins().Loaded("echo") {
		t.Fatal("Stop left the plugin running")
	}
	after, _ := os.ReadFile(statePath) //nolint:gosec // the test's own file
	if !bytes.Equal(before, after) {
		t.Fatalf("restoring and stopping rewrote plugin-connectors.json:\n%s\nwas\n%s", after, before)
	}
}

// A bundle that no longer matches its accepted digest is refused at restore (plugin_changed),
// and the start goes on without it.
func TestAChangedBundleIsRefusedAtRestore(t *testing.T) {
	root, in := install(t, admintest.Plugin{Loaded: true})
	if err := os.WriteFile(filepath.Join(in.Path, "extra.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := newLane(t, root, nil)
	if err := l.svc.Plugins().RestorePlugins(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l.svc.Plugins().Loaded("echo") {
		t.Fatal("a changed bundle loaded")
	}
	var refused bool
	for _, r := range l.world.Records(audit.KindOutcome) {
		if r.Connector == "plugin" && r.Operation == "load" && r.OutcomeCode == string(gate.CodePluginChanged) {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("the restore's outcome is not plugin_changed: %+v", l.world.Records(audit.KindOutcome))
	}
}

// The restore at start is Cerberus acting on its own: never refused, not even in a lockdown.
func TestTheRestoreIsNeverRefusedUnderLockdown(t *testing.T) {
	root, _ := install(t, admintest.Plugin{Loaded: true})
	world := gatetest.New(t, gatetest.Options{})
	world.Engage(t, brake.State{Lockdown: &brake.Lockdown{ID: "lk-1", EngagedAt: time.Unix(0, 0)}})
	l := newLane(t, root, world)
	if err := l.svc.Plugins().RestorePlugins(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !l.svc.Plugins().Loaded("echo") {
		t.Fatalf("the daemon_start restore was refused under lockdown: %+v", world.Records(audit.KindOutcome))
	}
	// A person's load in the same lockdown is refused.
	if _, _, err := l.svc.Plugins().Load(owner(), "echo", admin.GateOpts{}); gate.CodeOf(err) != gate.CodeLockdown {
		t.Fatalf("a load under lockdown: %v, want lockdown", err)
	}
}

// The keyring names are a contract (plan section 5): service "cerberus", account
// "<id>/<name>"; ssh's per-resource key "ssh/<resource-id>/key"; a plugin's secret
// "<plugin-id>/<secret>"; keychain:// and keyring:// references name the same entries.
func TestKeyringServiceAndAccountNamesMatchTheOldBuild(t *testing.T) {
	root, _ := install(t, admintest.Plugin{})
	l := newLane(t, root, nil)
	ctx := owner()

	// The console's provider save writes service "cerberus", account "<plugin-id>/<secret>".
	if _, err := l.svc.SaveCredentials(ctx, "echo", admin.CredentialSaveRequest{Secrets: map[string]string{"token": "tok-1"}}, admin.GateOpts{}); err != nil {
		t.Fatal(err)
	}
	if got, err := keyring.Get("cerberus", "echo/token"); err != nil || got != "tok-1" {
		t.Fatalf("keyring cerberus echo/token = %q, %v", got, err)
	}
	creds := l.svc.Credentials(ctx)
	var present bool
	for _, p := range creds.Providers {
		for _, s := range p.Secrets {
			present = present || (p.ID == "echo" && s.Name == "token" && s.Present && s.Stored == "stored")
		}
	}
	if !present {
		t.Fatalf("the editor does not see the saved token: %+v", creds)
	}

	// ssh reads its key per resource from service "cerberus", account "ssh/<resource-id>/key".
	if err := keyring.Set("cerberus", "ssh/box2/key", "/keys/from-keyring"); err != nil {
		t.Fatal(err)
	}
	box2 := l.res["box"]
	box2.ID, box2.Config = "box2", map[string]any{"host": "box2.invalid"}
	l.res["box2"] = box2
	if _, err := l.svc.Execute(ctx, admin.OperationArgs{Connector: "ssh", Operation: "exec", Acknowledged: true, Config: map[string]any{"id": "box2", "command": "true"}}); err != nil {
		t.Fatal(err)
	}
	if len(l.ssh.keys) == 0 || l.ssh.keys[len(l.ssh.keys)-1] != "/keys/from-keyring" {
		t.Fatalf("ssh key = %v, want the keyring's cerberus ssh/box2/key", l.ssh.keys)
	}

	// A keychain:// or keyring:// reference in connector-secrets.yaml (the config dir's) names
	// the same entry: the plugin, loaded through the lane's chain, receives it.
	if err := keyring.Set("cerberus", "vault/master", "from-ref"); err != nil {
		t.Fatal(err)
	}
	if err := keyring.Delete("cerberus", "echo/token"); err != nil {
		t.Fatal(err)
	}
	for _, scheme := range []string{"keychain", "keyring"} {
		if err := os.MkdirAll(l.cfg, 0o700); err != nil {
			t.Fatal(err)
		}
		mapping := "echo:\n  token: " + scheme + "://vault/master\n"
		if err := os.WriteFile(filepath.Join(l.cfg, "connector-secrets.yaml"), []byte(mapping), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := l.svc.Plugins().Load(ctx, "echo", admin.GateOpts{}); err != nil {
			t.Fatal(err)
		}
		out, err := l.svc.Execute(ctx, admin.OperationArgs{Connector: "echo", Operation: "echo"})
		if err != nil {
			t.Fatal(err)
		}
		if data, _ := json.Marshal(out.Data); !strings.Contains(string(data), `"token_set":true`) {
			t.Fatalf("%s://vault/master did not reach the plugin as its token: %s", scheme, data)
		}
		if _, _, err := l.svc.Plugins().Unload(ctx, "echo", admin.GateOpts{}); err != nil {
			t.Fatal(err)
		}
	}
}

// An issuing plugin operation reserves its id with the preparing receipt in the class A
// store, writes the issued secret under <adopt_root>/credential-issuance/secrets/issued, and
// records each later state there; no receipt file is written, and the id cannot be reused.
func TestIssuanceReceiptsGoToTheStoreAndSecretsStayInTheAdoptedRoot(t *testing.T) {
	root, _ := install(t, admintest.Plugin{Loaded: true})
	l := newLane(t, root, nil)
	if err := l.svc.Plugins().RestorePlugins(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("b", 32)
	args := admin.OperationArgs{Connector: "echo", Operation: "provision", Acknowledged: true, Config: map[string]any{"issuance_id": id}}
	out, err := l.svc.Execute(owner(), args)
	if err != nil {
		t.Fatal(err)
	}
	if out.CredentialIssuance == nil || out.CredentialIssuance.State != "completed" {
		t.Fatalf("issuance = %+v", out.CredentialIssuance)
	}
	data, _ := json.Marshal(out)
	if !strings.Contains(string(data), `"issued":["runtime"]`) {
		t.Fatalf("the plugin did not receive the issued credential by name: %s", data)
	}
	got, err := storage.Receipts(context.Background(), l.st, id)
	if err != nil {
		t.Fatal(err)
	}
	var states []string
	for _, r := range got {
		states = append(states, r.State)
	}
	if strings.Join(states, ",") != "preparing,prepared,dispatching,completed" {
		t.Fatalf("receipts = %v", states)
	}
	secret := filepath.Join(root, "credential-issuance", "secrets", "issued", id, "runtime")
	if _, err := os.Stat(secret); err != nil {
		t.Fatalf("the issued secret is not under the adopted root: %v", err)
	}
	if got[1].References["runtime"] != "file://"+secret {
		t.Fatalf("reference = %q, want file://%s", got[1].References["runtime"], secret)
	}
	if _, err := os.Stat(filepath.Join(root, "credential-issuance", id)); !os.IsNotExist(err) {
		t.Fatalf("a receipt file directory was written beside the store: %v", err)
	}
	if _, err := l.svc.Execute(owner(), args); err == nil || !strings.Contains(err.Error(), "already reserved") {
		t.Fatalf("a reused issuance id: %v, want refused as already reserved", err)
	}
}

// An ssh operation resolves its configured resource, runs through the gate, and holds a local
// path to <adopt_root>/transfers for a caller that is not a person at a terminal.
func TestSSHRunsThroughTheResourceAndTheTransferRoot(t *testing.T) {
	l := newLane(t, "", nil)
	out, err := l.svc.Execute(owner(), admin.OperationArgs{Connector: "ssh", Operation: "exec", Acknowledged: true, Config: map[string]any{"id": "box", "command": "uptime"}})
	if err != nil {
		t.Fatal(err)
	}
	if res, ok := out.Data.(*sshconn.ExecResult); !ok || res.Stdout != "ran: uptime" {
		t.Fatalf("result = %#v", out.Data)
	}
	if len(l.ssh.hosts) != 1 || l.ssh.hosts[0] != "box.invalid" {
		t.Fatalf("connected to %v, want the resource's host", l.ssh.hosts)
	}
	if _, err = l.svc.Execute(owner(), admin.OperationArgs{Connector: "ssh", Operation: "exec", Config: map[string]any{"id": "box", "command": "uptime"}}); gate.CodeOf(err) != gate.CodeAckRequired {
		t.Fatalf("exec without acknowledgment: %v", err)
	}
	_, err = l.svc.Execute(owner(), admin.OperationArgs{Connector: "ssh", Operation: "get", Acknowledged: true,
		Config: map[string]any{"id": "box", "remote_path": "/etc/hostname", "local_path": "/etc/passwd-copy"}})
	if gate.CodeOf(err) != gate.CodeInvalidArgs || !strings.Contains(err.Error(), "transfer root") {
		t.Fatalf("a local path outside the transfer root: %v", err)
	}
	if _, err = l.svc.Execute(owner(), admin.OperationArgs{Connector: "ssh", Operation: "get", Acknowledged: true,
		Config: map[string]any{"id": "box", "remote_path": "/etc/hostname", "local_path": "host.txt"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.root, "transfers", "host.txt")); err != nil {
		t.Fatalf("a relative local path did not land in <adopt_root>/transfers: %v", err)
	}
}

// A plan request on the console's forms runs nothing: the plugin's load answers with the plan
// it would bind, and the plugin stays unloaded.
func TestAPluginLoadPlanRunsNothing(t *testing.T) {
	root, in := install(t, admintest.Plugin{})
	l := newLane(t, root, nil)
	_, shown, err := l.svc.Plugins().Load(owner(), "echo", admin.GateOpts{Plan: true})
	if err != nil {
		t.Fatal(err)
	}
	if shown == nil || shown.PlanHash == "" || shown.Plan.Digests["bundle"] != in.Digest {
		t.Fatalf("plan = %+v", shown)
	}
	if l.svc.Plugins().Loaded("echo") {
		t.Fatal("a plan request loaded the plugin")
	}
	st, _, err := l.svc.Plugins().Load(owner(), "echo", admin.GateOpts{})
	if err != nil || !st.Loaded {
		t.Fatalf("load = %+v, %v", st, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "plugin-connectors.json")) //nolint:gosec // the test's own file
	if !strings.Contains(string(data), `"loaded": true`) || !strings.Contains(string(data), `"version": 2`) {
		t.Fatalf("an operator's load was not recorded in the shared state file:\n%s", data)
	}
	if fi, err := os.Stat(filepath.Join(root, "plugin-connectors.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode: %v %v", fi, err)
	}
}
