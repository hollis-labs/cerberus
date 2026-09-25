package main

import (
	"context"
	"sync"
	"testing"
	"time"

	gmcp "github.com/hollis-labs/go-mcp/server"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// `cerberus mcp` claims, for each tool call, the MCP client that made it:
// an agent, named by the clientInfo the call carries.
func TestMCPToolCallsClaimTheirClient(t *testing.T) {
	legacy := &mcpClientInfo{}
	claim := mcpPrincipal(cerbapi.ViaMCPStdio, legacy)
	var mu sync.Mutex
	var seen cerbapi.Principal
	srv := buildCerberusMCPServer(cerbapi.NewInProcessClient(), gmcp.WithInitializedHandler(legacy.capture))
	srv.RegisterTool(gmcp.Tool{Name: "probe", Description: "probe", InputSchema: gmcp.EmptyObjectSchema(),
		Handler: func(ctx context.Context, _ map[string]any) (any, error) {
			mu.Lock()
			seen = claim(ctx)
			mu.Unlock()
			return "ok", nil
		}})
	serverT, clientT := mcpsdk.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ss, err := srv.SDKServer().Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "claude-code", Version: "2.1.0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	if _, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "probe", Arguments: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen.Kind != cerbapi.PrincipalAgent || seen.Via != cerbapi.ViaMCPStdio || seen.Client != "claude-code/2.1.0" {
		t.Fatalf("claim %+v", seen)
	}
}

func TestMCPClaimWithoutClientInfo(t *testing.T) {
	if p := mcpPrincipal(cerbapi.ViaMCPHTTP, nil)(context.Background()); p.Kind != cerbapi.PrincipalAgent || p.Client != "mcp-client" || p.Via != cerbapi.ViaMCPHTTP {
		t.Fatalf("claim %+v", p)
	}
}
