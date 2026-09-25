package mcp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// pluginDaemon is the daemon as the generator sees it: managed plugins with
// their exposure, their definitions, and the admin lane that runs a call.
type pluginDaemon struct {
	fakeSocketProgressClient

	mu      sync.Mutex
	plugins []cerbapi.ManagedPluginConnectorState
	defs    []contract.Definition
	listErr error
	execErr error
	calls   []cerbapi.ExternalConnectorOperationArgs
}

func (d *pluginDaemon) ListManagedPlugins(context.Context) ([]cerbapi.ManagedPluginConnectorState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]cerbapi.ManagedPluginConnectorState(nil), d.plugins...), d.listErr
}

func (d *pluginDaemon) ListConnectors(context.Context) ([]contract.Definition, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]contract.Definition(nil), d.defs...), nil
}

func (d *pluginDaemon) ExecuteConnectorOperation(_ context.Context, args cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, args)
	if d.execErr != nil {
		return cerbapi.ExternalConnectorOperationResult{}, d.execErr
	}
	return cerbapi.ExternalConnectorOperationResult{Connector: args.Connector, Operation: args.Operation, Data: map[string]any{"ok": true}}, nil
}

func (d *pluginDaemon) expose(names ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.plugins[0].MCPExpose = names
}

// demoDefinition is what ListConnectors returns for a plugin: the manifest's
// effective contract, so an operation that declares no effect reads as exec.
func demoDefinition() contract.Definition {
	return contract.DefinitionFromManifest(contract.Manifest{
		APIVersion: contract.ManifestAPIVersion, Kind: "Connector", ID: "demo", Version: "1",
		Operations: []contract.ManifestOperation{
			{Name: "list_things", Description: "List things.", Effect: contract.EffectRead, Target: contract.TargetDescriptor{Kind: "demo.account"},
				InputSchema: contract.ObjectSchema(map[string]any{"region": contract.StringSchema("Region.")})},
			{Name: "delete_thing", Description: "Delete a thing.", Effect: contract.EffectDestructive, Preview: contract.PreviewPlugin, Target: contract.TargetDescriptor{Kind: "demo.thing"},
				InputSchema: contract.ObjectSchema(map[string]any{"id": contract.StringSchema("Thing id.")}, "id")},
			{Name: "legacy", Description: "No effect declared.", InputSchema: contract.ObjectSchema(map[string]any{})},
			{Name: "status", Description: "Collides with nothing yet.", Effect: contract.EffectRead, InputSchema: contract.ObjectSchema(map[string]any{})},
		},
	})
}

func newPluginDaemon(loaded bool, expose ...string) *pluginDaemon {
	return &pluginDaemon{
		plugins: []cerbapi.ManagedPluginConnectorState{{ID: "demo", Loaded: loaded, MCPExpose: expose}},
		defs:    []contract.Definition{demoDefinition()},
	}
}

func toolNames(tools []Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

// Default-deny: nothing is generated for a plugin that exposes nothing, or
// for exposure on a plugin that is not loaded.
func TestPluginToolsNotEnabledAreNotListed(t *testing.T) {
	for name, daemon := range map[string]*pluginDaemon{
		"loaded, nothing exposed":   newPluginDaemon(true),
		"exposed but not loaded":    newPluginDaemon(false, "list_things"),
		"no managed plugins at all": {defs: []contract.Definition{demoDefinition()}},
	} {
		t.Run(name, func(t *testing.T) {
			set, err := PluginTools(context.Background(), daemon, nil)
			if err != nil || len(set.Tools) != 0 {
				t.Fatalf("tools = %v, err = %v; want none", toolNames(set.Tools), err)
			}
		})
	}
}

// An enabled operation is listed under cerberus_<plugin>_<op>, with hints
// derived from its contract and the host's arguments where the contract calls
// for them.
func TestPluginToolsEnabledAreListedWithDerivedHints(t *testing.T) {
	set, err := PluginTools(context.Background(), newPluginDaemon(true, "list_things", "delete_thing", "legacy"), nil)
	if err != nil {
		t.Fatalf("PluginTools: %v", err)
	}
	byName := map[string]Tool{}
	for _, tool := range set.Tools {
		byName[tool.Name] = tool
	}
	if len(byName) != 3 {
		t.Fatalf("tools = %v, want exactly the three exposed", toolNames(set.Tools))
	}

	for name, op := range map[string]string{"cerberus_demo_list_things": "list_things", "cerberus_demo_delete_thing": "delete_thing", "cerberus_demo_legacy": "legacy"} {
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("%s not generated", name)
		}
		want := contract.HintsFor(mustOperation(t, op))
		got := contract.ToolHints{ReadOnly: tool.ReadOnlyHint, Destructive: tool.DestructiveHint, Idempotent: tool.IdempotentHint, OpenWorld: tool.OpenWorldHint}
		if got != want {
			t.Errorf("%s hints = %+v, want %+v from the contract", name, got, want)
		}
	}

	read := byName["cerberus_demo_list_things"]
	if !read.ReadOnlyHint || read.DestructiveHint {
		t.Errorf("a read is not read-only: %+v", read)
	}
	props := schemaProps(t, read)
	if _, ok := props["acknowledged"]; ok {
		t.Error("a read must not take acknowledged")
	}
	if _, ok := props["dry_run"]; ok {
		t.Error("an op without a preview must not take dry_run")
	}

	destructive := schemaProps(t, byName["cerberus_demo_delete_thing"])
	for _, arg := range []string{"id", "dry_run", "acknowledged"} {
		if _, ok := destructive[arg]; !ok {
			t.Errorf("delete_thing lacks %q", arg)
		}
	}

	// A gap reads as exec: it needs acknowledgment, and its hints say so.
	legacy := byName["cerberus_demo_legacy"]
	if legacy.ReadOnlyHint || !legacy.DestructiveHint {
		t.Errorf("an op with no effect must read as exec: %+v", legacy)
	}
	if _, ok := schemaProps(t, legacy)["acknowledged"]; !ok {
		t.Error("an op with no effect must take acknowledged")
	}
	if !strings.Contains(legacy.Description, "effect exec") {
		t.Errorf("description should name the effective effect: %q", legacy.Description)
	}
}

func schemaProps(t *testing.T, tool Tool) map[string]any {
	t.Helper()
	schema, ok := tool.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("%s input schema is %T", tool.Name, tool.InputSchema)
	}
	props, _ := schema["properties"].(map[string]any)
	return props
}

func mustOperation(t *testing.T, name string) contract.Operation {
	t.Helper()
	op, ok := demoDefinition().Operation(name)
	if !ok {
		t.Fatalf("no operation %q", name)
	}
	return op
}

// A generated tool never takes a hand-written tool's name.
func TestPluginToolCollisionIsRefused(t *testing.T) {
	reserved := map[string]bool{"cerberus_demo_status": true}
	set, err := PluginTools(context.Background(), newPluginDaemon(true, "status", "list_things"), reserved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(toolNames(set.Tools), ",") != "cerberus_demo_list_things" {
		t.Fatalf("tools = %v, want the colliding one left out", toolNames(set.Tools))
	}
	if len(set.Refused) != 1 || !strings.Contains(set.Refused[0], "cerberus_demo_status, which a built-in tool already is") {
		t.Fatalf("refused = %v", set.Refused)
	}
}

// The real hand-written list is what reserves names in production.
func TestReservedToolNamesCoverAllTools(t *testing.T) {
	reserved := ReservedToolNames(AllTools(fakeSocketProgressClient{}))
	for _, name := range []string{"cerberus_health", "cerberus_ssh_exec", "cerberus_docker_ps"} {
		if !reserved[name] {
			t.Errorf("%s is not reserved", name)
		}
	}
}

// A manifest argument named like a host argument would make the call
// ambiguous, so that tool is not generated.
func TestPluginToolWithHostArgumentNameIsRefused(t *testing.T) {
	daemon := newPluginDaemon(true, "clash")
	daemon.defs = []contract.Definition{contract.DefinitionFromManifest(contract.Manifest{
		APIVersion: contract.ManifestAPIVersion, Kind: "Connector", ID: "demo", Version: "1",
		Operations: []contract.ManifestOperation{{Name: "clash", Effect: contract.EffectRead,
			InputSchema: contract.ObjectSchema(map[string]any{"dry_run": map[string]any{"type": "boolean"}})}},
	})}
	set, _ := PluginTools(context.Background(), daemon, nil)
	if len(set.Tools) != 0 || len(set.Refused) != 1 || !strings.Contains(set.Refused[0], `"dry_run", which the host adds itself`) {
		t.Fatalf("tools = %v, refused = %v", toolNames(set.Tools), set.Refused)
	}
}

// A call goes through the admin lane with the host's arguments lifted out of
// the operation's config.
func TestPluginToolCallsTheAdminLane(t *testing.T) {
	daemon := newPluginDaemon(true, "delete_thing")
	set, _ := PluginTools(context.Background(), daemon, nil)
	cs := connectTools(t, set.Tools...)
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "cerberus_demo_delete_thing",
		Arguments: map[string]any{"id": "t-1", "dry_run": true, "acknowledged": true}})
	if err != nil || res.IsError {
		t.Fatalf("call: %v, isError=%v %s", err, res != nil && res.IsError, resultText(res))
	}
	if len(daemon.calls) != 1 {
		t.Fatalf("calls = %+v", daemon.calls)
	}
	call := daemon.calls[0]
	if call.Connector != "demo" || call.Operation != "delete_thing" || !call.DryRun || !call.Acknowledged {
		t.Fatalf("call = %+v", call)
	}
	if _, leaked := call.Config["dry_run"]; leaked || call.Config["id"] != "t-1" || len(call.Config) != 1 {
		t.Fatalf("config = %v, want only the operation's own arguments", call.Config)
	}
}

// A refusal from the admin lane reaches the client as isError, message intact.
func TestPluginToolRefusalSetsIsError(t *testing.T) {
	daemon := newPluginDaemon(true, "delete_thing")
	daemon.execErr = &cerbapi.ExternalConnectorError{Code: cerbapi.ExternalConnectorAckRequired, Connector: "demo", Operation: "delete_thing",
		Err: errors.New("destructive operation delete_thing " + ackRefusal)}
	set, _ := PluginTools(context.Background(), daemon, nil)
	cs := connectTools(t, set.Tools...)
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "cerberus_demo_delete_thing", Arguments: map[string]any{"id": "t-1"}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError || !strings.Contains(resultText(res), ackRefusal) {
		t.Fatalf("isError=%v %s", res.IsError, resultText(res))
	}
}

// The served list follows the daemon: a newly exposed op appears, a withdrawn
// one goes, and a client is told through tools/list_changed. A daemon that
// cannot be asked leaves the served set alone.
func TestPluginToolSyncFollowsTheDaemon(t *testing.T) {
	daemon := newPluginDaemon(true, "list_things")
	srv := NewServer("cerberus", "test")
	// As in production, hand-written tools are registered before any client
	// connects. That is what makes the server advertise tools.listChanged,
	// which a client needs to see at connect to subscribe to changes.
	static := NewCerberusHealthTool(fakeSocketProgressClient{})
	srv.RegisterTool(static)
	syncer := NewPluginToolSync(srv, daemon, map[string]bool{static.Name: true}, nil)

	changed := make(chan struct{}, 8)
	serverT, clientT := mcpsdk.NewInMemoryTransports()
	ss, err := srv.SDKServer().Connect(context.Background(), serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, &mcpsdk.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcpsdk.ToolListChangedRequest) { changed <- struct{}{} },
	}).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	listed := func() []string {
		res, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		var names []string
		for _, tool := range res.Tools {
			if tool.Name != static.Name {
				names = append(names, tool.Name)
			}
		}
		return names
	}
	awaitChange := func() {
		select {
		case <-changed:
		case <-time.After(2 * time.Second):
			t.Fatal("no tools/list_changed notification")
		}
	}

	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitChange()
	if got := strings.Join(listed(), ","); got != "cerberus_demo_list_things" {
		t.Fatalf("listed = %s", got)
	}

	daemon.expose("delete_thing")
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitChange()
	if got := strings.Join(listed(), ","); got != "cerberus_demo_delete_thing" {
		t.Fatalf("after re-exposure, listed = %s", got)
	}

	daemon.mu.Lock()
	daemon.listErr = errors.New("daemon unreachable")
	daemon.mu.Unlock()
	if err := syncer.Reconcile(context.Background()); err == nil {
		t.Fatal("Reconcile hid the daemon error")
	}
	if got := strings.Join(syncer.Served(), ","); got != "cerberus_demo_delete_thing" {
		t.Fatalf("an unreachable daemon changed the served set: %s", got)
	}

	daemon.mu.Lock()
	daemon.listErr = nil
	daemon.plugins[0].Loaded = false
	daemon.mu.Unlock()
	if err := syncer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitChange()
	if got := listed(); len(got) != 0 {
		t.Fatalf("an unloaded plugin's tools are still listed: %v", got)
	}
}
