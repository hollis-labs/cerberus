package mcp

import (
	"context"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// ctxCapturingClient records the context a tool hands the client.
type ctxCapturingClient struct {
	fakeSocketProgressClient
	ctx context.Context
}

func (c *ctxCapturingClient) ExecuteConnectorOperation(ctx context.Context, args cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	c.ctx = ctx
	return c.fakeSocketProgressClient.ExecuteConnectorOperation(ctx, args)
}

const labeledSecret = "token=abcdef1234567890" //nolint:gosec // a test sentinel, not a credential

func assertToolScoped(t *testing.T, name string, s *redact.Scope) {
	t.Helper()
	if s == nil {
		t.Fatalf("%s: tool call has no redaction scope", name)
	}
	s.Add("svc/key", "q7Zr2mXv9pLw")
	if got := s.Text("q7Zr2mXv9pLw"); got != redact.Marker {
		t.Fatalf("%s: scope did not remove a registered value: %q", name, got)
	}
}

// An MCP server has no middleware, so every served tool carries its own
// scope: the built-in list, and generated plugin tools. A handler built
// without that wrapper has no scope and still renders through the regex net.
func TestServedToolsBeginARequestScope(t *testing.T) {
	client := &ctxCapturingClient{}
	for _, tool := range AllTools(client) {
		if tool.Name != "cerberus_docker_ps" {
			continue
		}
		client.ctx = nil
		if _, err := tool.Handler(context.Background(), map[string]any{}); err != nil {
			t.Fatalf("handler: %v", err)
		}
		assertToolScoped(t, tool.Name, redact.ScopeFrom(client.ctx))
	}
	if client.ctx == nil {
		t.Fatal("cerberus_docker_ps is not in AllTools")
	}

	op := contract.Operation{Name: "list", Effect: contract.EffectRead}
	generated, err := pluginTool(client, "demo", op)
	if err != nil {
		t.Fatal(err)
	}
	client.ctx = nil
	if _, err := generated.Handler(context.Background(), map[string]any{}); err != nil {
		t.Fatalf("plugin handler: %v", err)
	}
	assertToolScoped(t, generated.Name, redact.ScopeFrom(client.ctx))

	client.ctx = nil
	if _, err := NewCerberusDockerPSTool(client).Handler(context.Background(), map[string]any{}); err != nil {
		t.Fatalf("unwrapped handler: %v", err)
	}
	if redact.ScopeFrom(client.ctx) != nil {
		t.Fatal("unwrapped handler has a scope")
	}
	if got := redact.ScopeFrom(client.ctx).Text(labeledSecret); got == labeledSecret || got != redact.Text(labeledSecret) {
		t.Fatalf("unwrapped handler rendered %q, want the regex net", got)
	}
}
