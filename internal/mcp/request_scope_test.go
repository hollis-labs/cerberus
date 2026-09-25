package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hollis-labs/go-mcp/budget"
	gmcp "github.com/hollis-labs/go-mcp/server"

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

// Whatever a served tool hands back is rendered through its call's scope:
// a string result, a structured result, each kind of error go-mcp reads, and
// a notification sent mid-call.
func TestServedToolOutputRendersThroughTheCallScope(t *testing.T) {
	const sentinel = "q7Zr2mXv9pLw" //nolint:gosec // a test sentinel, not a credential
	run := func(ret func(ctx context.Context) (any, error)) (any, []gmcp.Notification, error) {
		var sent []gmcp.Notification
		ctx := gmcp.WithNotifier(context.Background(), func(n gmcp.Notification) { sent = append(sent, n) })
		tool := withRequestScope(Tool{Name: "t", Handler: func(ctx context.Context, _ map[string]any) (any, error) {
			redact.ScopeFrom(ctx).Add("github/token", sentinel)
			gmcp.NotifyMessage(ctx, "error", "stage printed "+sentinel)
			return ret(ctx)
		}})
		result, err := tool.Handler(ctx, nil)
		return result, sent, err
	}
	leaks := func(v any) bool {
		data, _ := json.Marshal(v)
		return strings.Contains(string(data), sentinel) || strings.Contains(fmt.Sprint(v), sentinel)
	}

	for name, ret := range map[string]func(context.Context) (any, error){
		"string":      func(context.Context) (any, error) { return `{"stdout":"` + sentinel + `"}`, nil },
		"structured":  func(context.Context) (any, error) { return map[string]any{"stdout": sentinel}, nil },
		"plain error": func(context.Context) (any, error) { return nil, fmt.Errorf("upstream echoed %s", sentinel) },
		"tool failure": func(context.Context) (any, error) {
			return nil, toolFailure{message: "failed: " + sentinel, content: map[string]any{"error": sentinel}}
		},
		"budget error": func(context.Context) (any, error) {
			return nil, &budget.ToolError{Code: "x", Message: "bad " + sentinel, NextStep: "retry " + sentinel}
		},
	} {
		result, sent, err := run(ret)
		if leaks(result) {
			t.Errorf("%s: result leaked: %v", name, result)
		}
		if err != nil {
			if strings.Contains(err.Error(), sentinel) {
				t.Errorf("%s: error text leaked: %v", name, err)
			}
			var structured budget.StructuredError
			if errors.As(err, &structured) && leaks(structured.ToolErrorContent()) {
				t.Errorf("%s: error content leaked: %s", name, structured.ToolErrorContent())
			}
			var toolErr *budget.ToolError
			if errors.As(err, &toolErr) && (leaks(toolErr.Message) || leaks(toolErr.NextStep)) {
				t.Errorf("%s: tool error leaked: %+v", name, toolErr)
			}
		}
		if len(sent) != 1 || leaks(sent[0].Params) {
			t.Errorf("%s: notification = %+v", name, sent)
		}
		if params, ok := sent[0].Params.(map[string]any); !ok || params["level"] != "error" {
			t.Errorf("%s: notification params lost their shape: %#v", name, sent[0].Params)
		}
	}
}

type erroringClient struct {
	fakeSocketProgressClient
	err error
}

func (c *erroringClient) ExecuteConnectorOperation(context.Context, cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	return cerbapi.ExternalConnectorOperationResult{}, c.err
}

type renderedErr struct{ text string }

func (e renderedErr) Error() string     { return e.text }
func (e renderedErr) PreRendered() bool { return true }

// A connector refusal an MCP tool relays keeps its prose when it arrives
// pre-rendered, in its text and its structured content; anything else gets
// the rules.
func TestConnectorToolRelaysRenderedGuidanceIntact(t *testing.T) {
	const prose = "daemon: demo sync: operation_failed: the plugin rejected its token: rotated keys need a reload"
	for name, tc := range map[string]struct {
		err  error
		keep bool
	}{
		"pre-rendered": {renderedErr{prose}, true},
		"plain":        {errors.New(prose), false},
	} {
		tool := withRequestScope(NewCerberusDockerPSTool(&erroringClient{err: tc.err}))
		_, err := tool.Handler(context.Background(), map[string]any{})
		if err == nil {
			t.Fatalf("%s: want the refusal", name)
		}
		var structured budget.StructuredError
		if !errors.As(err, &structured) {
			t.Fatalf("%s: not structured: %T", name, err)
		}
		content, _ := json.Marshal(structured.ToolErrorContent())
		for where, got := range map[string]string{"text": err.Error(), "content": string(content)} {
			if kept := strings.Contains(got, "rotated keys"); kept != tc.keep {
				t.Errorf("%s %s = %q, want prose kept=%v", name, where, got, tc.keep)
			}
		}
	}
}
