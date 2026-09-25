package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// refusingClient refuses everything the way the serving process does: a
// connector operation fails with a coded ExternalConnectorError, and a
// resource or pipeline verb returns an OpResult with success:false.
type refusingClient struct {
	fakeSocketProgressClient
}

const ackRefusal = "requires operator acknowledgment"

func (refusingClient) ExecuteConnectorOperation(_ context.Context, args cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	return cerbapi.ExternalConnectorOperationResult{}, &cerbapi.ExternalConnectorError{
		Code:      cerbapi.ExternalConnectorAckRequired,
		Connector: args.Connector,
		Operation: args.Operation,
		Err:       errors.New("destructive operation " + args.Operation + " " + ackRefusal),
	}
}

func refusedOp(verb string) *cerbapi.OpResult {
	return &cerbapi.OpResult{Success: false, Error: verb + " refused: resource is unsupervised"}
}

func (refusingClient) StopResource(context.Context, string, ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return refusedOp("stop"), nil
}
func (refusingClient) ReloadResource(context.Context, string, ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return refusedOp("reload"), nil
}
func (refusingClient) DeployResource(context.Context, string, ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return refusedOp("deploy"), nil
}
func (refusingClient) ApplyResource(context.Context, string, ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return refusedOp("apply"), nil
}
func (refusingClient) SyncResource(context.Context, string, ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return refusedOp("sync"), nil
}
func (refusingClient) RemoveResource(context.Context, string, ...cerbapi.MutationOption) (*cerbapi.OpResult, error) {
	return refusedOp("remove"), nil
}
func (refusingClient) GetResourceRuntime(context.Context, string) (*cerbapi.ResourceRuntimeStatus, error) {
	return nil, errors.New("resource not found: nope")
}
func (refusingClient) RunPipeline(context.Context, string, ...cerbapi.MutationOption) (*cerbapi.PipelineRunResult, error) {
	return &cerbapi.PipelineRunResult{Success: false, Error: "pipeline stage smoke failed"}, nil
}

// connectTools serves tools from a real go-mcp server over an in-memory
// transport, so a test sees the CallToolResult a client receives — including
// isError, which calling a Handler directly cannot show.
func connectTools(t *testing.T, tools ...Tool) *mcpsdk.ClientSession {
	t.Helper()
	srv := NewServer("cerberus", "test")
	for _, tool := range tools {
		srv.RegisterTool(tool)
	}
	ctx := context.Background()
	serverT, clientT := mcpsdk.NewInMemoryTransports()
	ss, err := srv.SDKServer().Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// Every refusal or failure a tool returns sets isError, with its message
// intact. A success:false body without the flag reads as a successful call to
// a client that checks the flag.
func TestToolRefusalsSetIsError(t *testing.T) {
	client := refusingClient{}
	for _, tc := range []struct {
		tool Tool
		args map[string]any
		want string
	}{
		// Connector refusals, relayed from the serving process.
		{NewCerberusSSHExecTool(client), map[string]any{"resource_id": "box", "command": "uptime"}, ackRefusal},
		{NewCerberusSSHGetTool(client), map[string]any{"resource_id": "box", "remote_path": "/a", "local_path": "/tmp/a"}, ackRefusal},
		{NewCerberusDockerDownTool(client), map[string]any{"resource_id": "stack"}, ackRefusal},
		{NewCerberusDockerPSTool(client), map[string]any{}, ackRefusal},
		{NewCerberusDropletStopTool(client), map[string]any{"droplet_id": 42}, ackRefusal},
		{NewCerberusDropletCreateTool(client), map[string]any{"name": "n", "region": "r", "size": "s", "image": "i"}, ackRefusal},
		{NewCerberusCloudflareDNSDeleteTool(client), map[string]any{"zone_id": "z", "record_id": "r"}, ackRefusal},
		{NewCerberusGithubStatusTool(client), map[string]any{"owner": "o", "repo": "r"}, ackRefusal},
		// Refusals the tool makes itself, before reaching the serving process.
		{NewCerberusDNSCreateTool(client), map[string]any{"domain": "example.com"}, "per-record create/delete is disabled"},
		{NewCerberusConnectorDescribeTool(client), map[string]any{"id": ""}, `missing \"id\"`},
		// Resource and pipeline verbs whose OpResult reports failure.
		{NewCerberusResourceStopTool(client), map[string]any{"resource_id": "svc"}, "stop refused"},
		{NewCerberusResourceReloadTool(client), map[string]any{"resource_id": "svc"}, "reload refused"},
		{NewCerberusResourceDeployTool(client), map[string]any{"resource_id": "svc"}, "deploy refused"},
		{NewCerberusResourceApplyTool(client), map[string]any{"resource_id": "svc"}, "apply refused"},
		{NewCerberusResourceSyncTool(client), map[string]any{"resource_id": "svc"}, "sync refused"},
		{NewCerberusResourceRemoveTool(client), map[string]any{"resource_id": "svc"}, "remove refused"},
		{NewCerberusResourceEnsureFreshTool(client), map[string]any{"resource_id": "svc", "force": true}, "deploy refused"},
		{NewCerberusResourceStatusTool(client), map[string]any{"resource_id": "nope"}, "resource not found"},
		{NewCerberusPipelineRunTool(client), map[string]any{"pipeline_id": "smoke"}, "pipeline stage smoke failed"},
	} {
		t.Run(tc.tool.Name, func(t *testing.T) {
			cs := connectTools(t, tc.tool)
			res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: tc.tool.Name, Arguments: tc.args})
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if !res.IsError {
				t.Fatalf("isError not set on a refusal: %s", resultText(res))
			}
			if text := resultText(res); !strings.Contains(text, tc.want) {
				t.Fatalf("refusal message lost: want %q in %s", tc.want, text)
			}
		})
	}
}

// A success keeps isError unset: the flag marks failures, not every call.
func TestToolSuccessLeavesIsErrorUnset(t *testing.T) {
	tool := Tool{Name: "ok_tool", Description: "ok", InputSchema: emptyObjectSchema(), ReadOnlyHint: true,
		Handler: func(context.Context, map[string]any) (any, error) {
			return toolResult(lifecycleResult{Success: true, Message: "done"})
		}}
	cs := connectTools(t, tool)
	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "ok_tool"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(resultText(res), "done") {
		t.Fatalf("success reported as error: %+v", res)
	}
}

// The failure body goes out already redacted, since go-mcp marshals tool
// error content with plain encoding/json.
func TestToolFailureBodyIsRedacted(t *testing.T) {
	_, err := toolResult(lifecycleResult{Success: false, Error: "API_KEY=mcp-sentinel failed", BuildOutput: "Authorization: Bearer build-sentinel"})
	var failure toolFailure
	if !errors.As(err, &failure) {
		t.Fatalf("want toolFailure, got %v", err)
	}
	body := string(failure.ToolErrorContent().(json.RawMessage))
	for _, leak := range []string{"mcp-sentinel", "build-sentinel"} {
		if strings.Contains(body, leak) || strings.Contains(failure.Error(), leak) {
			t.Fatalf("secret %q leaked: %s / %s", leak, body, failure.Error())
		}
	}
	if got := redact.Text(failure.Error()); got != failure.Error() {
		t.Fatalf("message not stable under redact.Text: %q -> %q", failure.Error(), got)
	}
}

func resultText(res *mcpsdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*mcpsdk.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}
