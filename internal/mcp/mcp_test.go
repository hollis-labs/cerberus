package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/store"
	"github.com/hollis-labs/libs/core/store/migrate"
	libauth "github.com/hollis-labs/libs/security/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hollis-labs/substrate/boot"
	"github.com/hollis-labs/substrate/module"
	_ "github.com/hollis-labs/substrate/modules/auth"
	_ "github.com/hollis-labs/substrate/modules/health"
	smcp "github.com/hollis-labs/substrate/modules/mcp"

	"github.com/hollis-labs/cerberus/internal/api"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate/gatetest"
	cmcp "github.com/hollis-labs/cerberus/internal/mcp"
	"github.com/hollis-labs/cerberus/internal/service"
	"github.com/hollis-labs/cerberus/internal/settings"
	"github.com/hollis-labs/cerberus/internal/storage"
	"github.com/hollis-labs/cerberus/internal/supervise"
	"github.com/hollis-labs/cerberus/internal/target"
	"github.com/hollis-labs/cerberus/internal/testhome"
)

func TestMain(m *testing.M) { testhome.Main(m) }

// stdioScopes are `cerberus mcp`'s local principal's (cmd/cerberus): never approve.
var stdioScopes = []string{api.ScopeRead, api.ScopeOperate, api.ScopeAdmin}

// mcpApp boots what serves MCP: auth in enforce mode and the mcp module, configured as Cerberus
// configures them (name cerberus, version 0.1.0, /mcp needs cerberus.read).
func mcpApp(t *testing.T) *module.App {
	t.Helper()
	cfg := `{"auth":{"mode":"enforce","api_keys":[{"id":"svc/r","hash":"` + libauth.HashKey("read-key") + `","scopes":["cerberus.read"]}]},` +
		`"mcp":{"version":"0.1.0","scope":"cerberus.read"}}`
	run, err := boot.Start(context.Background(), "cerberus", []string{"health", "auth", "mcp"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Stop(context.Background()) })
	return run.App
}

func connect(t *testing.T, app *module.App, scopes []string) *mcpsdk.ClientSession {
	t.Helper()
	return connectNamed(t, app, scopes, "t")
}

// connectNamed connects an MCP client whose clientInfo name is client.
func connectNamed(t *testing.T, app *module.App, scopes []string, client string) *mcpsdk.ClientSession {
	t.Helper()
	srvIn, cliOut := io.Pipe()
	cliIn, srvOut := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	go func() {
		_ = smcp.ServeStdio(ctx, app, smcp.StdioOptions{In: srvIn, Out: srvOut, LocalTrust: true, LocalScopes: scopes, Notice: io.Discard})
		_ = srvOut.Close()
	}()
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: client, Version: "1"}, nil).
		Connect(ctx, &mcpsdk.IOTransport{Reader: cliIn, Writer: cliOut}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcpsdk.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error(), true
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

// none is a backend that is never called.
type none struct{}

func (none) Do(context.Context, string, string, any, any) error {
	return errors.New("no backend in this test")
}

func normalize(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The tool catalog served over MCP is main's, tool for tool: the same names in the same
// order, descriptions, input schemas (approval_id included) and hints, from a server that
// announces itself as main's did (cerberus 0.1.0). main-catalog.json is the dump of main's
// AllTools(nil) at f3f8181. The output lists what is served and what is pending, by slice.
func TestServedCatalogIsMain(t *testing.T) {
	app := mcpApp(t)
	if err := cmcp.Register(app, none{}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, app, stdioScopes)
	if info := cs.InitializeResult().ServerInfo; info.Name != "cerberus" || info.Version != "0.1.0" {
		t.Fatalf("server %+v, want cerberus 0.1.0", info)
	}
	want, err := cmcp.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	var got []*mcpsdk.Tool
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, tool)
	}
	if len(got) != len(want) || len(want) != 71 {
		t.Fatalf("served %d tools, main's catalog has %d (want 71)", len(got), len(want))
	}
	byName := map[string]*mcpsdk.Tool{}
	for _, g := range got {
		byName[g.Name] = g
	}
	deref := func(b *bool) bool { return b != nil && *b }
	for _, w := range want {
		g, ok := byName[w.Name]
		if !ok {
			t.Errorf("%s: not served", w.Name)
			continue
		}
		if g.Description != w.Description {
			t.Errorf("%s: description differs", w.Name)
		}
		if !reflect.DeepEqual(normalize(t, g.InputSchema), normalize(t, w.InputSchema)) {
			t.Errorf("%s: input schema differs:\n got %s\nwant %s", w.Name, jsonOf(g.InputSchema), jsonOf(w.InputSchema))
		}
		a := g.Annotations
		if a == nil {
			a = &mcpsdk.ToolAnnotations{}
		}
		if a.ReadOnlyHint != w.ReadOnlyHint || deref(a.DestructiveHint) != w.DestructiveHint || a.IdempotentHint != w.IdempotentHint || deref(a.OpenWorldHint) != w.OpenWorldHint {
			t.Errorf("%s: hints %+v, want read_only=%v destructive=%v idempotent=%v open_world=%v", w.Name, a,
				w.ReadOnlyHint, w.DestructiveHint, w.IdempotentHint, w.OpenWorldHint)
		}
	}

	pending, off := cmcp.Pending(), cmcp.SchedulerOff()
	served := len(want) - len(pending) - len(off)
	t.Logf("parity: %d tools, names, descriptions, input schemas and hints identical to main's", len(want))
	t.Logf("served: %d", served)
	t.Logf("scheduler off (answer that cerberus.scheduler.enabled is false): %d", len(off))
	t.Logf("dropped beside the tools: the host events SSE stream (Accept: text/event-stream on /hosts/_events); no caller in inventory-clients.md, and cerberus_host_events is served")
	byOwner := map[string][]string{}
	for _, p := range pending {
		byOwner[p.Owner] = append(byOwner[p.Owner], p.Name)
	}
	for _, owner := range []string{cmcp.OwnerSlice7, cmcp.OwnerSlice8b} {
		t.Logf("pending, slice %s (%d): %s", owner, len(byOwner[owner]), strings.Join(byOwner[owner], ", "))
	}
	if served != 22 || len(off) != 13 || len(byOwner) != 2 {
		t.Fatalf("served %d (want 22), scheduler-off %d (want 13), owners %v", served, len(off), byOwner)
	}
	// A pending tool answers that it is not available, naming its slice.
	if out, isErr := call(t, cs, "cerberus_ssh_status", map[string]any{"resource_id": "x"}); !isErr || !strings.Contains(out, "not available in this build (slice 7") {
		t.Fatalf("pending tool: %q %v", out, isErr)
	}
	if out, isErr := call(t, cs, "cerberus_schedule_list", map[string]any{}); !isErr || !strings.Contains(out, "scheduler is off") {
		t.Fatalf("scheduler tool: %q %v", out, isErr)
	}
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// gatedAPI is the API over a real gate and a real supervision lane in supervise mode, with one
// production resource (the API's own gate tests' setup).
func gatedAPI(t *testing.T) (*api.API, *gatetest.World) {
	t.Helper()
	return gatedAPIWith(t, gatetest.Options{})
}

func gatedAPIWith(t *testing.T, o gatetest.Options) (*api.API, *gatetest.World) {
	t.Helper()
	prod := config.ResourceDef{ID: "web", Type: "process", Project: "demo", Connector: "local",
		Env: target.EnvProd, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf},
		Config: map[string]any{"mode": "dev_session", "command": []any{"/bin/sleep", "300"}}}
	resources := []config.ResourceDef{prod}
	w := gatetest.New(t, o)
	base := t.TempDir()
	ctx := context.Background()
	cfg := store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: filepath.Join(base, "cerberus.db"), CreateParentDir: true}}
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fsys, root := storage.Migrations()
	if err = migrate.Ensure(ctx, cfg, st, migrate.Source{FS: fsys, Root: root}); err != nil {
		t.Fatal(err)
	}
	key, err := audit.OpenDigester(filepath.Join(base, "audit"))
	if err != nil {
		t.Fatal(err)
	}
	service.SetStateRoot(base)
	t.Cleanup(func() { service.SetStateRoot("") })
	lane, err := supervise.New(supervise.Options{Mode: settings.ModeSupervise, Home: base, StateDir: base, Store: st, Digester: key, Gate: w.Gate,
		Config: func() (*config.ConfigV2, error) { return &config.ConfigV2{Resources: resources}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	a, err := api.New(api.Options{Resources: func() ([]config.ResourceDef, error) { return resources, nil },
		Home: t.TempDir(), Gate: w.Gate, Mutator: lane})
	if err != nil {
		t.Fatal(err)
	}
	return a, w
}

// The stdio local principal (read, operate, admin) reaches the gate like any agent: an admin
// write on a production resource stops there, pending the owner's approval, and the tool
// answers main's structured refusal; nothing ran. It cannot approve: the approve route answers
// it 403 user_required, and no tool needs cerberus.approve.
func TestLocalPrincipalStopsAtTheGateAndCannotApprove(t *testing.T) {
	a, w := gatedAPI(t)
	app := mcpApp(t)
	backend := cmcp.MuxBackend{Handler: a.Mux()}
	if err := cmcp.Register(app, backend); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, app, stdioScopes)

	out, isErr := call(t, cs, "cerberus_resource_remove", map[string]any{"resource_id": "web", "acknowledged": true})
	var refusal struct {
		Success  bool   `json:"success"`
		Code     string `json:"code"`
		NextStep string `json:"next_step"`
		Approval struct {
			ID string `json:"id"`
		} `json:"approval"`
	}
	if !isErr || json.Unmarshal([]byte(out), &refusal) != nil || refusal.Success || refusal.Code != "approval_pending" || refusal.Approval.ID == "" ||
		!strings.Contains(refusal.NextStep, "Cerberus console") {
		t.Fatalf("remove as the local principal: isError=%v %s", isErr, out)
	}
	for _, r := range w.Records(audit.KindOutcome) {
		if r.Operation == "remove" && r.OutcomeCode == audit.OutcomeOK {
			t.Fatalf("the remove ran: %+v", r)
		}
	}

	local := libauth.WithPrincipal(context.Background(), libauth.Principal{Verified: true, ID: smcp.LocalPrincipalID,
		Kind: libauth.KindOperator, Scopes: stdioScopes})
	err := backend.Do(local, "POST", "/approvals/"+refusal.Approval.ID+"/decide", map[string]any{"approve": true}, nil)
	var r *cmcp.Refusal
	if !errors.As(err, &r) || r.Status != 403 || r.Code != "user_required" {
		t.Fatalf("the local principal approving: %v", err)
	}
}

// A local principal without a tool's scope is refused before the tool runs.
func TestToolScopesAreChecked(t *testing.T) {
	a, _ := gatedAPI(t)
	app := mcpApp(t)
	if err := cmcp.Register(app, cmcp.MuxBackend{Handler: a.Mux()}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, app, []string{api.ScopeRead})
	if out, isErr := call(t, cs, "cerberus_resource_stop", map[string]any{"resource_id": "web", "acknowledged": true}); !isErr ||
		strings.Contains(out, "approval_pending") {
		t.Fatalf("stop with read only: %q %v", out, isErr)
	}
	if out, isErr := call(t, cs, "cerberus_resource_list", map[string]any{}); isErr || !strings.Contains(out, `"total":1`) {
		t.Fatalf("list with read: %q %v", out, isErr)
	}
}
