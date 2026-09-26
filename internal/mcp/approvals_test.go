package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/connector"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// approvePDP asks for approval on everything.
type approvePDP struct{}

func (approvePDP) Authorize(policy.Request) policy.Result {
	return policy.Result{Decision: policy.Approve, WouldBlock: true}
}
func (approvePDP) GlobalPosture() string { return policy.PostureSecure }

type enforceEverything struct{}

func (enforceEverything) Enforced(policy.Request) bool { return true }

// gatedDaemon is a daemon socket with enforcement on and a broker, two
// docker resources (dev: tty_confirm; prod: out of band), and an MCP client
// in front of it that reaches it the way `cerberus mcp` does: a socket
// client claiming an MCP principal. It returns the MCP session, the broker,
// and the socket path for other callers.
func gatedDaemon(t *testing.T) (*mcpsdk.ClientSession, *cerbapi.Broker, string) {
	t.Helper()
	sink := audit.NewMemory()
	broker, err := cerbapi.NewBroker(sink, filepath.Join(t.TempDir(), "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	cerbapi.SetPolicyDecisionPoint(approvePDP{})
	cerbapi.SetEnforcement(enforceEverything{})
	cerbapi.SetBroker(broker)
	t.Cleanup(func() { cerbapi.SetEnforcement(nil); cerbapi.SetBroker(nil); cerbapi.SetPolicyDecisionPoint(nil) })

	registry := connector.NewRegistry()
	registry.RegisterDefinition(dockerconn.Definition())
	svc := cerbapi.NewExternalConnectorService(sink, registry)
	svc.SetResourceLookup(cerbapi.ConfigResourceLookup(&config.ConfigV2{Resources: []config.ResourceDef{
		{ID: "dev-box", Type: "container", Connector: "docker", Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}},
		{ID: "prod-box", Type: "container", Connector: "docker", Env: target.EnvProd, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}},
	}}))
	inProc := cerbapi.NewInProcessClient(cerbapi.WithExternalConnectorService(svc), cerbapi.WithInProcessAudit(sink))
	sockPath := shortSocketPath(t)
	srv := cerbapi.NewSocketServer(inProc, sockPath)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Run(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if conn, err := net.Dial("unix", sockPath); err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket %s never appeared", sockPath)
		}
	}
	client := cerbapi.NewSocketClient(sockPath, cerbapi.WithPrincipalClaim(func(context.Context) cerbapi.Principal {
		return cerbapi.Principal{Kind: cerbapi.PrincipalAgent, Via: cerbapi.ViaMCPStdio, Client: "claude-code"}
	}))
	return connectTools(t, AllTools(client)...), broker, sockPath
}

func callTool(t *testing.T, cs *mcpsdk.ClientSession, name string, args map[string]any) (*mcpsdk.CallToolResult, toolRefusal) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var body toolRefusal
	if res.IsError {
		if data, err := json.Marshal(res.StructuredContent); err == nil && res.StructuredContent != nil {
			_ = json.Unmarshal(data, &body)
		}
		if body.Error == "" {
			_ = json.Unmarshal([]byte(resultText(res)), &body)
		}
	}
	return res, body
}

// An agent is told approval_pending as data — the id, expiry, channel and
// approve command — with what to ask its human for each channel; it waits
// with cerberus_approval_wait, and a retry with approval_id is the approved
// call, bound to its requester, used once.
func TestAgentApprovalRoundTrip(t *testing.T) {
	approvalPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { approvalPollInterval = 500 * time.Millisecond })
	cs, broker, _ := gatedDaemon(t)
	stop := map[string]any{"resource_id": "dev-box", "acknowledged": true}

	res, pending := callTool(t, cs, "cerberus_docker_down", stop)
	if !res.IsError || pending.Code != "approval_pending" || pending.Approval == nil || pending.Approval.ID == "" ||
		pending.Approval.Channel != approval.ChannelTTYConfirm || pending.Approval.ApproveWith != "cerberus approvals approve "+pending.Approval.ID ||
		pending.Approval.ExpiresAt.IsZero() {
		t.Fatalf("approval_pending: %s", resultText(res))
	}
	for _, want := range []string{"ask your operator to run `cerberus approvals approve " + pending.Approval.ID + "`", "cannot approve it yourself",
		"cerberus_approval_wait with id " + pending.Approval.ID, "approval_id " + pending.Approval.ID} {
		if !strings.Contains(pending.NextStep, want) {
			t.Errorf("next step lacks %q: %s", want, pending.NextStep)
		}
	}
	if strings.Contains(resultText(res), redact.Marker) || redact.Text(pending.NextStep) != pending.NextStep {
		t.Fatalf("the refusal was redacted: %s", resultText(res))
	}
	id := pending.Approval.ID

	// Waiting while nothing happens times out with it still pending.
	res, _ = callTool(t, cs, "cerberus_approval_wait", map[string]any{"id": id, "timeout_seconds": 1})
	var waited approvalWaitResult
	if res.IsError || json.Unmarshal([]byte(resultText(res)), &waited) != nil || waited.Changed || waited.Approval.Status != approval.Pending ||
		!strings.Contains(waited.NextStep, "still pending") {
		t.Fatalf("wait on pending: %s", resultText(res))
	}
	if strings.Contains(resultText(res), "assertion") || strings.Contains(resultText(res), "args_digest") {
		t.Fatalf("the wait showed the broker's own fields: %s", resultText(res))
	}

	// The operator approves on their terminal while the agent waits.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(100 * time.Millisecond)
		human := cerbapi.WithPrincipal(cerbapi.BeginRequest(context.Background(), cerbapi.SurfaceSocket), cerbapi.Principal{Kind: cerbapi.PrincipalHuman, Via: cerbapi.ViaCLI, UID: 501})
		if _, err := broker.DecideAs(human, id, cerbapi.ApprovalDecisionArgs{Approve: true}); err != nil {
			t.Errorf("decide: %v", err)
		}
	}()
	res, _ = callTool(t, cs, "cerberus_approval_wait", map[string]any{"id": id, "timeout_seconds": 10})
	wg.Wait()
	if res.IsError || json.Unmarshal([]byte(resultText(res)), &waited) != nil || !waited.Changed || waited.Approval.Status != approval.Approved ||
		waited.Approval.Decision == nil || !strings.Contains(waited.NextStep, "approval_id "+id) {
		t.Fatalf("wait on approve: %s", resultText(res))
	}

	// The retry with approval_id is the approved call: the gate lets it
	// through and spends the approval. Nothing serves docker here, so what
	// follows the gate fails — but not as an approval.
	retry := map[string]any{"resource_id": "dev-box", "acknowledged": true, argApprovalID: id}
	_, body := callTool(t, cs, "cerberus_docker_down", retry)
	if strings.HasPrefix(body.Code, "approval_") || body.Code == "plan_stale" {
		t.Fatalf("the approved retry was refused as %s: %s", body.Code, body.Error)
	}
	if got, _ := broker.Get(id); got.Status != approval.Consumed {
		t.Fatalf("status after the retry = %s", got.Status)
	}
	// Used once.
	if _, body = callTool(t, cs, "cerberus_docker_down", retry); body.Code != "approval_required" || !strings.Contains(body.NextStep, "without approval_id") {
		t.Fatalf("second use: %+v", body)
	}
	// Other arguments under the same approval are not it.
	if _, body = callTool(t, cs, "cerberus_docker_up", retry); body.Code == "" || !strings.HasPrefix(body.Code, "approval_") && body.Code != "plan_stale" {
		t.Fatalf("another operation under the approval: %+v", body)
	}
}

// Out of band, the agent is told to ask for the passkey on the console.
func TestOutOfBandPendingTellsTheAgentAboutThePasskey(t *testing.T) {
	cs, _, _ := gatedDaemon(t)
	res, pending := callTool(t, cs, "cerberus_docker_down", map[string]any{"resource_id": "prod-box", "acknowledged": true})
	if !res.IsError || pending.Approval == nil || pending.Approval.Channel != approval.ChannelOutOfBand {
		t.Fatalf("approval_pending: %s", resultText(res))
	}
	for _, want := range []string{"approve " + pending.Approval.ID + " on the Cerberus console", "Touch ID", "cerberus_approval_wait"} {
		if !strings.Contains(pending.NextStep, want) {
			t.Errorf("next step lacks %q: %s", want, pending.NextStep)
		}
	}
}

// The requester binding survives MCP: an approval asked for by one MCP
// client is not usable by another caller.
func TestApprovalIsTheMCPRequesters(t *testing.T) {
	cs, broker, sock := gatedDaemon(t)
	_, pending := callTool(t, cs, "cerberus_docker_down", map[string]any{"resource_id": "dev-box", "acknowledged": true})
	if pending.Approval == nil {
		t.Fatalf("no approval: %+v", pending)
	}
	human := cerbapi.WithPrincipal(cerbapi.BeginRequest(context.Background(), cerbapi.SurfaceSocket), cerbapi.Principal{Kind: cerbapi.PrincipalHuman, Via: cerbapi.ViaCLI, UID: 501})
	if _, err := broker.DecideAs(human, pending.Approval.ID, cerbapi.ApprovalDecisionArgs{Approve: true}); err != nil {
		t.Fatal(err)
	}
	other := cerbapi.NewSocketClient(sock, cerbapi.WithPrincipalClaim(func(context.Context) cerbapi.Principal {
		return cerbapi.Principal{Kind: cerbapi.PrincipalAgent, Via: cerbapi.ViaCLI, Client: "some-script"}
	}))
	_, err := other.ExecuteConnectorOperation(context.Background(), cerbapi.ExternalConnectorOperationArgs{Connector: "docker", Operation: "stop",
		Config: map[string]any{"resource": "dev-box"}, Acknowledged: true, ApprovalID: pending.Approval.ID})
	var c *cerbapi.ExternalConnectorError
	if !errors.As(err, &c) || c.Code != cerbapi.ExternalConnectorApprovalRequired {
		t.Fatalf("another caller used the approval: %v", err)
	}
	if got, _ := broker.Get(pending.Approval.ID); got.Status != approval.Approved {
		t.Fatalf("status = %s", got.Status)
	}
}

// An MCP principal never decides (I5): no tool decides, and the socket
// refuses the MCP client's own socket connection when it tries.
func TestMCPNeverDecides(t *testing.T) {
	cs, broker, sock := gatedDaemon(t)
	_, pending := callTool(t, cs, "cerberus_docker_down", map[string]any{"resource_id": "dev-box", "acknowledged": true})
	if pending.Approval == nil {
		t.Fatalf("no approval: %+v", pending)
	}
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if strings.Contains(tool.Name, "approv") && tool.Name != "cerberus_approval_wait" {
			t.Errorf("an MCP tool over approvals other than wait: %s", tool.Name)
		}
	}
	if op, _ := ToolOperation("cerberus_approval_wait"); op.Effect != contract.EffectRead {
		t.Errorf("cerberus_approval_wait is %s, not a read", op.Effect)
	}
	mcpSocket := cerbapi.NewSocketClient(sock, cerbapi.WithPrincipalClaim(func(context.Context) cerbapi.Principal {
		return cerbapi.Principal{Kind: cerbapi.PrincipalAgent, Via: cerbapi.ViaMCPStdio, Client: "claude-code"}
	}))
	for _, via := range []string{cerbapi.ViaMCPStdio, cerbapi.ViaMCPHTTP} {
		c := cerbapi.NewSocketClient(sock, cerbapi.WithPrincipalClaim(func(context.Context) cerbapi.Principal {
			// Claiming to be human does not help an MCP surface.
			return cerbapi.Principal{Kind: cerbapi.PrincipalHuman, Via: via, Client: "claude-code"}
		}))
		if _, err := c.DecideApproval(context.Background(), pending.Approval.ID, cerbapi.ApprovalDecisionArgs{Approve: true}); err == nil ||
			!strings.Contains(err.Error(), "never approves") {
			t.Errorf("%s approved: %v", via, err)
		}
	}
	if _, err := mcpSocket.DecideApproval(context.Background(), pending.Approval.ID, cerbapi.ApprovalDecisionArgs{Approve: true}); err == nil {
		t.Error("the MCP client approved its own request")
	}
	if got, _ := broker.Get(pending.Approval.ID); got.Status != approval.Pending {
		t.Fatalf("status = %s", got.Status)
	}
}

// Every tool over an operation that can need approval advertises
// approval_id and passes it to the call; reads do neither.
func TestGatedToolsPassApprovalID(t *testing.T) {
	rec := &recordingClient{}
	for _, tool := range AllTools(rec) {
		op, _ := ToolOperation(tool.Name)
		props, _ := tool.InputSchema.(map[string]any)["properties"].(map[string]any)
		_, advertised := props[argApprovalID]
		if op.Effect == contract.EffectRead || ungatedTools[tool.Name] != "" {
			if advertised {
				t.Errorf("%s is not gated and advertises approval_id", tool.Name)
			}
			continue
		}
		if !advertised {
			t.Errorf("%s does not advertise approval_id", tool.Name)
			continue
		}
		args := map[string]any{argApprovalID: "apr_test"}
		for key, value := range map[string]any{"resource_id": "r", "command": "uptime", "local_path": "/tmp/a", "remote_path": "/a",
			"pipeline_id": "p", "acknowledged": true, "force": true} {
			if _, ok := props[key]; ok {
				args[key] = value
			}
		}
		rec.seen = ""
		if _, err := tool.Handler(context.Background(), args); err != nil && rec.seen == "" {
			t.Errorf("%s: %v", tool.Name, err)
		}
		if rec.seen != "apr_test" {
			t.Errorf("%s did not pass approval_id on (saw %q)", tool.Name, rec.seen)
		}
	}
}

// A generated plugin tool takes approval_id for a gated operation, and a
// plugin cannot declare an argument of that name.
func TestPluginToolsTakeApprovalID(t *testing.T) {
	rec := &recordingClient{}
	gated, err := pluginTool(rec, "demo", contract.Operation{Name: "delete_thing", Effect: contract.EffectDestructive, RequiresAck: true, InputSchema: contract.ObjectSchema(map[string]any{})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gated.Handler(context.Background(), map[string]any{argApprovalID: "apr_p", "acknowledged": true}); err != nil {
		t.Fatal(err)
	}
	if rec.seen != "apr_p" || rec.config[argApprovalID] != nil {
		t.Fatalf("seen %q, config %v", rec.seen, rec.config)
	}
	read, err := pluginTool(rec, "demo", contract.Operation{Name: "list_things", Effect: contract.EffectRead, InputSchema: contract.ObjectSchema(map[string]any{})})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := schemaProps(t, read)[argApprovalID]; ok {
		t.Error("a read plugin tool takes approval_id")
	}
	if _, err = pluginTool(rec, "demo", contract.Operation{Name: "sneaky", Effect: contract.EffectWrite,
		InputSchema: contract.ObjectSchema(map[string]any{argApprovalID: contract.StringSchema("mine")})}); err == nil {
		t.Error("a plugin declared approval_id")
	}
}

// recordingClient records the approval id each gated call carries.
type recordingClient struct {
	fakeSocketProgressClient
	seen   string
	config map[string]any
}

func (r *recordingClient) opts(o []cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	r.seen = cerbapi.ApplyMutationOptions(o).ApprovalID
	return &cerbapi.OpResult{Success: true}, nil
}
func (r *recordingClient) DeployResource(_ context.Context, _ string, o ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return r.opts(o)
}
func (r *recordingClient) ApplyResource(_ context.Context, _ string, o ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return r.opts(o)
}
func (r *recordingClient) ReloadResource(_ context.Context, _ string, o ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return r.opts(o)
}
func (r *recordingClient) StopResource(_ context.Context, _ string, o ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return r.opts(o)
}
func (r *recordingClient) SyncResource(_ context.Context, _ string, o ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return r.opts(o)
}
func (r *recordingClient) RemoveResource(_ context.Context, _ string, o ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return r.opts(o)
}
func (r *recordingClient) RunPipeline(_ context.Context, _ string, o ...cerbapi.MutationOption) (*cerbapi.PipelineRunResult, error) {
	r.seen = cerbapi.ApplyMutationOptions(o).ApprovalID
	return &cerbapi.PipelineRunResult{Success: true}, nil
}
func (r *recordingClient) ExecuteConnectorOperation(_ context.Context, args cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	r.seen, r.config = args.ApprovalID, args.Config
	return cerbapi.ExternalConnectorOperationResult{Connector: args.Connector, Operation: args.Operation}, nil
}

// pendingClient answers every resource verb and pipeline run with the coded
// approval_pending a daemon sends, as those tools return it raw.
type pendingClient struct{ fakeSocketProgressClient }

func pendingRefusal() error {
	return &cerbapi.ExternalConnectorError{Code: cerbapi.ExternalConnectorApprovalPending, Connector: "local", Operation: "deploy",
		Approval: &cerbapi.ApprovalRef{ID: "apr_raw", ExpiresAt: time.Now().Add(time.Hour), ApproveWith: "cerberus approvals approve apr_raw", Channel: approval.ChannelOutOfBand},
		Err:      redact.Guidance("local deploy needs approval: approval apr_raw is pending")}
}
func (pendingClient) DeployResource(context.Context, string, ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return nil, pendingRefusal()
}
func (pendingClient) RunPipeline(context.Context, string, ...cerbapi.MutationOption) (*cerbapi.PipelineRunResult, error) {
	return nil, pendingRefusal()
}

// A tool that returns the daemon's refusal as it came — the resource verbs
// and pipeline_run — still gives the agent the approval and its next step.
func TestRawRefusalsCarryTheApproval(t *testing.T) {
	client := pendingClient{}
	for _, tc := range []struct {
		tool Tool
		args map[string]any
	}{
		{NewCerberusResourceDeployTool(client), map[string]any{"resource_id": "svc", "acknowledged": true}},
		{NewCerberusPipelineRunTool(client), map[string]any{"pipeline_id": "ship", "acknowledged": true}},
	} {
		cs := connectTools(t, withRequestScope(tc.tool))
		res, body := callTool(t, cs, tc.tool.Name, tc.args)
		if !res.IsError || body.Code != "approval_pending" || body.Approval == nil || body.Approval.ID != "apr_raw" ||
			!strings.Contains(body.NextStep, "Touch ID") || !strings.Contains(body.Error, "apr_raw is pending") {
			t.Errorf("%s: %s", tc.tool.Name, resultText(res))
		}
	}
}

// The wait tool is bounded, says what is wrong with a bad id, and says so
// when its process holds no approvals.
func TestApprovalWaitBoundsAndErrors(t *testing.T) {
	for in, want := range map[float64]time.Duration{0: approvalWaitDefault, 5: 5 * time.Second, 120: approvalWaitMax, -3: approvalWaitDefault} {
		if got := waitTimeout(map[string]any{"timeout_seconds": in}); got != want {
			t.Errorf("timeout %v = %v, want %v", in, got, want)
		}
	}
	cs, _, _ := gatedDaemon(t)
	res, body := callTool(t, cs, "cerberus_approval_wait", map[string]any{"id": "apr_nope", "timeout_seconds": 1})
	if !res.IsError || !strings.Contains(body.Error, "apr_nope") {
		t.Fatalf("unknown id: %s", resultText(res))
	}
	cerbapi.SetBroker(nil)
	inProc := connectTools(t, withRequestScope(NewCerberusApprovalWaitTool(cerbapi.NewInProcessClient())))
	res, body = callTool(t, inProc, "cerberus_approval_wait", map[string]any{"id": "apr_x"})
	if !res.IsError || !strings.Contains(body.Error, "cerberus daemon") {
		t.Fatalf("no broker: %s", resultText(res))
	}
}
