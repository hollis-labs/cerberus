package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/connector"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	"github.com/hollis-labs/cerberus/internal/redact"
)

func auditedDockerService(sink audit.Sink) *ExternalConnectorService {
	return auditedDockerServiceWith(sink, &fakeDockerBackend{})
}

func auditedDockerServiceWith(sink audit.Sink, backend *fakeDockerBackend) *ExternalConnectorService {
	registry := connector.NewRegistry()
	registry.Register(dockerconn.NewWithBackend(backend))
	return NewExternalConnectorService(sink, registry)
}

// pairs groups a sink's records into intent/outcome pairs by operation id.
func pairs(t *testing.T, recs []audit.Record) map[string][2]audit.Record {
	t.Helper()
	out := map[string][2]audit.Record{}
	for _, rec := range recs {
		p := out[rec.OperationID]
		switch rec.Kind {
		case audit.KindIntent:
			p[0] = rec
		case audit.KindOutcome:
			p[1] = rec
		}
		out[rec.OperationID] = p
	}
	for id, p := range out {
		if p[0].Kind != audit.KindIntent || p[1].Kind != audit.KindOutcome {
			t.Errorf("operation %s: unpaired records %+v", id, p)
		}
	}
	return out
}

// Every exit of the admin lane is recorded: a read that runs, a refusal by
// each gate, a dry run and a failure. Each writes an intent before anything
// happens and an outcome after, with the same operation id.
func TestAdminLaneRecordsEveryExit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     ExternalConnectorOperationArgs
		decision string
		code     string
		effect   string
	}{
		{"read", ExternalConnectorOperationArgs{Connector: "docker", Operation: "list_containers"}, audit.DecisionAllowed, audit.OutcomeOK, "read"},
		{"ack refusal", ExternalConnectorOperationArgs{Connector: "docker", Operation: "destroy", Config: map[string]any{"container": "web"}}, audit.DecisionRefused, "acknowledgment_required", "destructive"},
		{"argument refusal", ExternalConnectorOperationArgs{Connector: "docker", Operation: "logs", Config: map[string]any{"bogus": 1, "container": "web"}}, audit.DecisionRefused, "invalid_args", "read_sensitive"},
		{"undeclared", ExternalConnectorOperationArgs{Connector: "docker", Operation: "wipe"}, audit.DecisionRefused, "operation_unsupported", ""},
		// What `connectors exec` now sends rather than refusing locally.
		{"unknown connector", ExternalConnectorOperationArgs{Connector: "nope", Operation: "x"}, audit.DecisionRefused, "operation_unsupported", ""},
		{"dry run with no preview", ExternalConnectorOperationArgs{Connector: "docker", Operation: "stop", DryRun: true, Acknowledged: true, Config: map[string]any{"container": "web"}}, audit.DecisionRefused, "preview_unsupported", "lifecycle"},
		{"acknowledged lifecycle", ExternalConnectorOperationArgs{Connector: "docker", Operation: "start", Acknowledged: true, Config: map[string]any{"container": "web"}}, audit.DecisionAllowed, audit.OutcomeOK, "lifecycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := audit.NewMemory()
			_, _ = auditedDockerService(sink).Execute(WithCallerSurface(context.Background(), SurfaceSocket), tc.args)
			got := pairs(t, sink.Records())
			if len(got) != 1 {
				t.Fatalf("%d operations recorded, want 1", len(got))
			}
			for _, p := range got {
				intent, outcome := p[0], p[1]
				if intent.Seq > outcome.Seq {
					t.Fatal("outcome written before intent")
				}
				if outcome.Decision != tc.decision || outcome.OutcomeCode != tc.code || outcome.Effect != tc.effect {
					t.Fatalf("outcome %+v, want decision %s code %s effect %s", outcome, tc.decision, tc.code, tc.effect)
				}
				if intent.Principal.Surface != "socket" || !intent.Principal.SelfReported || intent.Posture != audit.PostureSecure {
					t.Fatalf("intent principal/posture: %+v", intent)
				}
				if intent.Acknowledged != tc.args.Acknowledged || intent.DryRun != tc.args.DryRun || !strings.HasPrefix(intent.ArgsDigest, "hmac-sha256:") {
					t.Fatalf("intent %+v", intent)
				}
			}
		})
	}
}

// The target is recorded from the contract's target fields; nothing else from
// the arguments is.
func TestAuditRecordsTheTargetAndNotTheArguments(t *testing.T) {
	sink := audit.NewMemory()
	_, _ = auditedDockerService(sink).Execute(context.Background(), ExternalConnectorOperationArgs{
		Connector: "docker", Operation: "logs", Config: map[string]any{"container": "web", "lines": 5},
	})
	rec := sink.Records()[0]
	if rec.Target.Kind != "docker.container" || rec.Target.Fields["container"] != "web" {
		t.Fatalf("target %+v", rec.Target)
	}
	if _, ok := rec.Target.Fields["lines"]; ok {
		t.Fatal("a non-target argument was recorded")
	}
}

// Decision 8: when the log cannot be written, a non-read is refused before
// anything resolves, with a coded error that survives redaction; a read goes
// on and is logged.
func TestUnwritableAuditRefusesNonReads(t *testing.T) {
	resolves := 0
	svc := NewExternalConnectorService(audit.Failing{}, resolveCountingRegistry(&resolves))
	_, err := svc.Execute(context.Background(), ExternalConnectorOperationArgs{Connector: "docker", Operation: "stop", Acknowledged: true, Config: map[string]any{"container": "web"}})
	var connErr *ExternalConnectorError
	if !errors.As(err, &connErr) || connErr.Code != ExternalConnectorAuditUnavailable {
		t.Fatalf("err = %v, want audit_unavailable", err)
	}
	if resolves != 0 {
		t.Fatal("a refused non-read reached resolution")
	}
	if got := redact.Text(err.Error()); got != err.Error() || !strings.Contains(err.Error(), "nothing ran") {
		t.Fatalf("refusal lost its explanation: %q", got)
	}
	if status := ExternalConnectorHTTPStatus(err, 500); status != 503 {
		t.Fatalf("status %d, want 503", status)
	}

	_, _ = svc.Execute(context.Background(), ExternalConnectorOperationArgs{Connector: "docker", Operation: "list_containers"})
	if resolves != 1 {
		t.Fatal("a read was refused because its record could not be written")
	}
}

// A credential sent in config, and a secret a plugin resolves, appear nowhere
// in any record.
func TestAuditRecordsCarryNoCredential(t *testing.T) {
	const configSentinel = "SENTINEL-CONFIG-7d3e"
	sink := audit.NewMemory()
	svc := auditedDockerService(sink)
	ctx := context.Background()
	for _, args := range []ExternalConnectorOperationArgs{
		{Connector: "docker", Operation: "logs", Config: map[string]any{"container": "web", "api_token": configSentinel}},
		{Connector: "docker", Operation: "list_containers", Config: map[string]any{"password": configSentinel}},
		{Connector: "ssh", Operation: "exec", Config: map[string]any{"id": "box", "command": "export TOKEN=" + configSentinel}},
	} {
		_, _ = svc.Execute(ctx, args)
	}

	managed := leakyManagedService(t)
	managed.audit = sink
	_, _ = NewExternalConnectorService(sink, connector.NewRegistry(), managed).Execute(ctx, ExternalConnectorOperationArgs{Connector: "leaky", Operation: "list_things"})
	_, _ = managed.Execute(ctx, "leaky", PluginConnectorExecArgs{Operation: "list_things"})

	recs := sink.Records()
	if len(recs) < 10 {
		t.Fatalf("only %d records", len(recs))
	}
	data, err := json.Marshal(recs)
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{configSentinel, resolvedSentinel, shapedSentinel} {
		if strings.Contains(string(data), sentinel) {
			t.Fatalf("a record carries %q:\n%s", sentinel, data)
		}
	}
	// The plugin's records name its credential and carry its fingerprints.
	var named bool
	for _, rec := range recs {
		if rec.Connector == "leaky" && rec.Kind == audit.KindOutcome {
			named = named || strings.Join(rec.CredentialNames, ",") == "leaky/token"
		}
	}
	if !named {
		t.Fatal("the plugin's outcome does not name the credential it resolves")
	}
}

// The direct plugin route and the plugin lifecycle write their own records;
// the admin lane's call into a plugin is recorded once, not twice.
func TestPluginPathsAreRecordedOnce(t *testing.T) {
	sink := audit.NewMemory()
	managed := leakyManagedService(t)
	managed.audit = sink
	ctx := context.Background()

	_, _ = NewExternalConnectorService(sink, connector.NewRegistry(), managed).Execute(ctx, ExternalConnectorOperationArgs{Connector: "leaky", Operation: "list_things"})
	if got := len(pairs(t, sink.Records())); got != 1 {
		t.Fatalf("admin lane into a plugin recorded %d operations, want 1", got)
	}
	_, _ = managed.Execute(ctx, "leaky", PluginConnectorExecArgs{Operation: "list_things"})
	_, _ = managed.Unload(ctx, "leaky")
	_, _ = managed.Load(ctx, "ghost")
	got := pairs(t, sink.Records())
	if len(got) != 4 {
		t.Fatalf("%d operations recorded, want 4", len(got))
	}
	for _, p := range got {
		if p[1].Operation == "unload" && (p[1].Effect != "admin" || p[1].Connector != "plugin" || p[1].Target.Fields["id"] != "leaky") {
			t.Fatalf("unload record %+v", p[1])
		}
	}
}

// The services that write the audit log are constructed, outside tests, only
// in internal/app, which is what opens the real sink. Anywhere else would be
// a place a service could be built on a sink nobody reads.
func TestServicesAreConstructedOnlyInApp(t *testing.T) {
	constructors := map[string]bool{"NewExternalConnectorService": true, "NewManagedPluginConnectorService": true, "NewPluginConnectorService": true, "NewResourceRuntimeService": true}
	// The one construction outside internal/app that is allowed, and why.
	allowed := map[string]string{
		filepath.Join("internal", "cerbapi", "inprocess.go") + ":NewResourceRuntimeService": "the in-process client's fallback runtime, which refuses every mutation unless a sink is injected",
	}
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "node_modules" || name == "web" || name == "dist" || strings.HasPrefix(name, ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.HasPrefix(rel, filepath.Join("internal", "app")+string(filepath.Separator)) {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var name string
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "cerbapi" {
					name = fn.Sel.Name
				}
			case *ast.Ident:
				if f.Name.Name == "cerbapi" {
					name = fn.Name
				}
			}
			if _, ok := allowed[rel+":"+name]; ok {
				return true
			}
			if constructors[name] {
				t.Errorf("%s: %s is constructed outside internal/app", fset.Position(call.Pos()), name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, construct := range []func(){
		func() { NewExternalConnectorService(nil, connector.NewRegistry()) },
		func() { _, _ = NewManagedPluginConnectorService(nil, "t", io.Discard, "") },
		func() { NewPluginConnectorService(nil, "t", io.Discard) },
		func() { NewResourceRuntimeService(nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("a nil audit sink was accepted")
				}
			}()
			construct()
		}()
	}
}

// Every Client method is classified. An audited method writes its records;
// the rest only read. A new
// method fails here until someone decides which it is.
func TestClientMethodsAreClassifiedForAudit(t *testing.T) {
	const (
		audited  = "audited"
		readOnly = "read_only"
	)
	classification := map[string]string{
		"ExecuteConnectorOperation": audited,
		"ReloadManagedPlugin":       audited,
		"LoadManagedPlugin":         audited,
		"UnloadManagedPlugin":       audited,
		"UninstallManagedPlugin":    audited,
		"ExecuteManagedPlugin":      audited,

		"DeployResource": audited, "ApplyResource": audited, "ReloadResource": audited, "StopResource": audited,
		"SyncResource": audited, "RemoveResource": audited, "RunPipeline": audited,

		"ResourceLogs": readOnly, "Health": readOnly, "ListProjects": readOnly, "ListResources": readOnly,
		"ResolveDiagnostics": readOnly, "GetResourceRuntime": readOnly, "GetResourceInspect": readOnly,
		"GetResourceDoctor": readOnly, "ListPipelines": readOnly, "GetPipeline": readOnly,
		"ListConnectors": readOnly, "ListLiveConnectors": readOnly, "ListManagedPlugins": readOnly,
		"ManagedPluginHealth": readOnly,
	}
	iface := reflect.TypeOf((*Client)(nil)).Elem()
	for i := 0; i < iface.NumMethod(); i++ {
		name := iface.Method(i).Name
		if _, ok := classification[name]; !ok {
			t.Errorf("Client.%s is not classified for audit: add it as audited, read_only or pending", name)
		}
	}
	for name := range classification {
		if _, ok := iface.MethodByName(name); !ok {
			t.Errorf("classification names %s, which Client no longer has", name)
		}
	}

	// The audited methods really write: each call, through the in-process
	// client, adds an intent and an outcome.
	sink := audit.NewMemory()
	managed := leakyManagedService(t)
	managed.audit = sink
	runtime := NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(&config.ConfigV2{}))
	client := NewInProcessClient(WithExternalConnectorService(auditedDockerService(sink)), WithManagedPluginConnectorService(managed), WithResourceRuntimeService(runtime))
	ctx := context.Background()
	calls := map[string]func(){
		"ExecuteConnectorOperation": func() {
			_, _ = client.ExecuteConnectorOperation(ctx, ExternalConnectorOperationArgs{Connector: "docker", Operation: "list_containers"})
		},
		"ReloadManagedPlugin":    func() { _, _ = client.ReloadManagedPlugin(ctx, "ghost") },
		"LoadManagedPlugin":      func() { _, _ = client.LoadManagedPlugin(ctx, "ghost") },
		"UnloadManagedPlugin":    func() { _, _ = client.UnloadManagedPlugin(ctx, "ghost") },
		"UninstallManagedPlugin": func() { _, _ = client.UninstallManagedPlugin(ctx, "ghost") },
		"ExecuteManagedPlugin": func() {
			_, _ = client.ExecuteManagedPlugin(ctx, "leaky", PluginConnectorExecArgs{Operation: "list_things"})
		},
		"DeployResource": func() { _, _ = client.DeployResource(ctx, "svc") },
		"ApplyResource":  func() { _, _ = client.ApplyResource(ctx, "svc") },
		"ReloadResource": func() { _, _ = client.ReloadResource(ctx, "svc") },
		"StopResource":   func() { _, _ = client.StopResource(ctx, "svc") },
		"SyncResource":   func() { _, _ = client.SyncResource(ctx, "svc") },
		"RemoveResource": func() { _, _ = client.RemoveResource(ctx, "svc") },
		"RunPipeline":    func() { _, _ = client.RunPipeline(ctx, "p") },
	}
	for name, kind := range classification {
		if kind != audited {
			continue
		}
		call, ok := calls[name]
		if !ok {
			t.Errorf("%s is audited but this test does not exercise it", name)
			continue
		}
		before := len(sink.Records())
		call()
		if got := len(sink.Records()) - before; got != 2 {
			t.Errorf("%s wrote %d records, want an intent and an outcome", name, got)
		}
	}
}

// The console's plugin reload after a secret save is an unload and a load
// through the client, on the request's web-surface context. Both are admin
// lifecycle operations, and both are recorded as the web console's.
func TestConsoleReloadIsAuditedAsWeb(t *testing.T) {
	sink := audit.NewMemory()
	managed := leakyManagedService(t)
	managed.audit = sink
	client := NewInProcessClient(WithManagedPluginConnectorService(managed))
	ctx := WithCallerSurface(context.Background(), SurfaceWeb)

	if _, err := client.UnloadManagedPlugin(ctx, "leaky"); err != nil {
		t.Fatalf("unload: %v", err)
	}
	if _, err := client.LoadManagedPlugin(ctx, "leaky"); err != nil {
		t.Fatalf("load: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range pairs(t, sink.Records()) {
		outcome := p[1]
		if outcome.Connector != "plugin" || outcome.Target.Fields["id"] != "leaky" {
			continue
		}
		if outcome.Effect != "admin" || outcome.Principal.Surface != string(SurfaceWeb) || outcome.OutcomeCode != "ok" {
			t.Fatalf("%s record = %+v, want an admin outcome from the web surface", outcome.Operation, outcome)
		}
		seen[outcome.Operation] = true
	}
	if !seen["unload"] || !seen["load"] {
		t.Fatalf("recorded %v, want both unload and load", seen)
	}
}
