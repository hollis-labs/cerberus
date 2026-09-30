package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// oobDaemon answers docker stop with approval_pending for an out-of-band
// approval until the call carries its id, and holds that approval's state.
type oobDaemon struct {
	fakeSocketProgressClient
	mu      sync.Mutex
	channel string
	status  approval.Status
	calls   []string // approval ids the operation was called with
}

func (d *oobDaemon) ExecuteConnectorOperation(_ context.Context, args cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, args.ApprovalID)
	if args.ApprovalID == "apr_oob" && d.status == approval.Approved {
		return cerbapi.ExternalConnectorOperationResult{Connector: "docker", Operation: "stop"}, nil
	}
	return cerbapi.ExternalConnectorOperationResult{}, &cerbapi.ExternalConnectorError{Code: cerbapi.ExternalConnectorApprovalPending, Connector: "docker", Operation: "stop",
		Approval: &cerbapi.ApprovalRef{ID: "apr_oob", ExpiresAt: time.Now().Add(time.Hour), ApproveWith: "cerberus approvals approve apr_oob", Channel: d.channel},
		Err:      redact.Guidance("docker stop needs approval: approval apr_oob is pending")}
}

func (d *oobDaemon) GetApproval(context.Context, string) (approval.Approval, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return approval.Approval{ID: "apr_oob", Status: d.status, Connector: "docker", Operation: "stop", Channel: d.channel, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (d *oobDaemon) set(s approval.Status) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status = s
}

// connectElicitingClient serves tools to a real go-mcp client that can open
// URLs (or not), whose handler plays the operator.
func connectElicitingClient(t *testing.T, url bool, operator func(*mcpsdk.ElicitParams) *mcpsdk.ElicitResult, tools ...Tool) *mcpsdk.ClientSession {
	t.Helper()
	srv := NewServer("cerberus", "test")
	for _, tool := range tools {
		srv.RegisterTool(tool)
	}
	serverT, clientT := mcpsdk.NewInMemoryTransports()
	ss, err := srv.SDKServer().Connect(context.Background(), serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	caps := &mcpsdk.ClientCapabilities{Elicitation: &mcpsdk.ElicitationCapabilities{Form: &mcpsdk.FormElicitationCapabilities{}}}
	if url {
		caps.Elicitation.URL = &mcpsdk.URLElicitationCapabilities{}
	}
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "claude-code", Version: "0"}, &mcpsdk.ClientOptions{
		Capabilities: caps,
		ElicitationHandler: func(_ context.Context, req *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
			return operator(req.Params), nil
		},
	}).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func oobFixture(t *testing.T, channel string) *oobDaemon {
	t.Helper()
	oldURL, oldWait, oldPoll := ConsoleApprovalURL, oobElicitWait, approvalPollInterval
	ConsoleApprovalURL = func(id string) (string, error) {
		return "http://localhost:4783/login?token=t&next=%2Fapprovals%3Fid%3D" + id, nil
	}
	oobElicitWait, approvalPollInterval = 300*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { ConsoleApprovalURL, oobElicitWait, approvalPollInterval = oldURL, oldWait, oldPoll })
	return &oobDaemon{channel: channel, status: approval.Pending}
}

func dockerDown(d *oobDaemon) Tool {
	for _, tool := range AllTools(d) {
		if tool.Name == "cerberus_docker_down" {
			return tool
		}
	}
	panic("no docker_down")
}

var stopArgs = map[string]any{"resource_id": "prod-box", "acknowledged": true}

// The operator is shown the console page for the approval, approves there
// with a passkey, and the call completes on the client's retry, run once
// more with the approval id.
func TestOutOfBandApprovalThroughTheClient(t *testing.T) {
	d := oobFixture(t, approval.ChannelOutOfBand)
	var asked []*mcpsdk.ElicitParams
	cs := connectElicitingClient(t, true, func(p *mcpsdk.ElicitParams) *mcpsdk.ElicitResult {
		asked = append(asked, p)
		d.set(approval.Approved) // the passkey, on the console page
		return &mcpsdk.ElicitResult{Action: "accept"}
	}, dockerDown(d))
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "cerberus_docker_down", Arguments: stopArgs})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %s", err, resultText(res))
	}
	if len(asked) != 1 || asked[0].Mode != "url" || asked[0].ElicitationID != "apr_oob" || !strings.Contains(asked[0].URL, "approvals%3Fid%3Dapr_oob") ||
		!strings.Contains(asked[0].Message, "passkey") {
		t.Fatalf("elicitation: %+v", asked)
	}
	if strings.Join(d.calls, ",") != ",apr_oob" {
		t.Fatalf("calls: %q", d.calls)
	}
}

// Declined, or accepted but not decided in time: the same approval, still
// pending, and the operation ran once, never under a new approval.
func TestOutOfBandElicitationNotApproved(t *testing.T) {
	for name, action := range map[string]string{"declined": "decline", "not decided in time": "accept"} {
		d := oobFixture(t, approval.ChannelOutOfBand)
		cs := connectElicitingClient(t, true, func(*mcpsdk.ElicitParams) *mcpsdk.ElicitResult { return &mcpsdk.ElicitResult{Action: action} }, dockerDown(d))
		res, body := callTool(t, cs, "cerberus_docker_down", stopArgs)
		if !res.IsError || body.Code != "approval_pending" || body.Approval == nil || body.Approval.ID != "apr_oob" || !strings.Contains(body.NextStep, "cerberus_approval_wait") {
			t.Errorf("%s: %s", name, resultText(res))
		}
		if len(d.calls) != 1 {
			t.Errorf("%s: the operation ran %d times", name, len(d.calls))
		}
	}
	d := oobFixture(t, approval.ChannelOutOfBand)
	cs := connectElicitingClient(t, true, func(*mcpsdk.ElicitParams) *mcpsdk.ElicitResult {
		d.set(approval.Denied)
		return &mcpsdk.ElicitResult{Action: "accept"}
	}, dockerDown(d))
	if res, body := callTool(t, cs, "cerberus_docker_down", stopArgs); !res.IsError || body.Code != "approval_required" || !strings.Contains(body.Error, "denied") {
		t.Errorf("denied: %s", resultText(res))
	}
}

// Without a client that opens URLs, a console to send the operator to, or
// an out-of-band approval, the answer is today's approval_pending.
func TestOutOfBandElicitationFallsBack(t *testing.T) {
	never := func(*mcpsdk.ElicitParams) *mcpsdk.ElicitResult {
		t.Error("the client was asked")
		return &mcpsdk.ElicitResult{Action: "decline"}
	}
	d := oobFixture(t, approval.ChannelOutOfBand)
	if res, body := callTool(t, connectElicitingClient(t, false, never, dockerDown(d)), "cerberus_docker_down", stopArgs); body.Code != "approval_pending" {
		t.Errorf("no URL mode: %s", resultText(res))
	}
	d = oobFixture(t, approval.ChannelOutOfBand)
	ConsoleApprovalURL = nil
	if res, body := callTool(t, connectElicitingClient(t, true, never, dockerDown(d)), "cerberus_docker_down", stopArgs); body.Code != "approval_pending" {
		t.Errorf("no console: %s", resultText(res))
	}
	d = oobFixture(t, approval.ChannelTTYConfirm)
	if res, body := callTool(t, connectElicitingClient(t, true, never, dockerDown(d)), "cerberus_docker_down", stopArgs); body.Code != "approval_pending" {
		t.Errorf("tty_confirm: %s", resultText(res))
	}
}

// The retry state is bound to the approval and the tool, and signed.
func TestOutOfBandStateVerifies(t *testing.T) {
	state := signState("apr_1", "cerberus_docker_down")
	if id, ok := verifyState(state, "cerberus_docker_down"); !ok || id != "apr_1" {
		t.Fatalf("round trip: %q %v", id, ok)
	}
	if _, ok := verifyState(state, "cerberus_docker_destroy"); ok {
		t.Fatal("another tool's state verified")
	}
	forged := signState("apr_1", "cerberus_docker_down")
	forged = forged[:len(forged)-4] + "AAAA"
	if _, ok := verifyState(forged, "cerberus_docker_down"); ok {
		t.Fatal("a forged state verified")
	}
}
