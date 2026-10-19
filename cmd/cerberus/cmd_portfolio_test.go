package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/mcp"
)

func TestPortfolioHealthCLIAndMCPTool(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("cerb-pf-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	cfg := &config.ConfigV2{
		Version: 2,
		Projects: []config.ProjectDef{
			{ID: "demo", Name: "Demo Project"},
		},
		Resources: []config.ResourceDef{
			{
				ID:        "demo-ssh",
				Name:      "Demo SSH",
				Type:      "server",
				Connector: "ssh",
				Project:   "demo",
				Config: map[string]any{
					"host": "192.0.2.10",
				},
			},
		},
	}

	sink := audit.NewMemory()
	runtimeSvc := cerbapi.NewResourceRuntimeService(sink, cerbapi.WithResourceRuntimeConfigV2(cfg))
	inProc := cerbapi.NewInProcessClient(
		cerbapi.WithInProcessAudit(sink),
		cerbapi.WithResourceRuntimeService(runtimeSvc),
		cerbapi.WithPortfolioHealthOptions(cerbapi.PortfolioHealthOptions{
			ControllerID:     "agent-os",
			ProviderInstance: "cerberus-local",
		}),
	)

	// Verify writePortfolioHealth text formatter.
	proj, err := inProc.PortfolioHealth(context.Background(), cerbapi.PortfolioHealthQuery{})
	if err != nil {
		t.Fatalf("PortfolioHealth failed: %v", err)
	}
	var buf bytes.Buffer
	if err = writePortfolioHealth(&buf, proj); err != nil {
		t.Fatalf("writePortfolioHealth failed: %v", err)
	}
	text := buf.String()
	if !strings.Contains(text, "cerberus.portfolio.health.v1") || !strings.Contains(text, "demo-ssh") {
		t.Fatalf("unexpected text output:\n%s", text)
	}

	// Verify MCP tool cerberus_portfolio_health over SocketServer.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := cerbapi.NewSocketServer(inProc, sockPath)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	sockClient := cerbapi.NewSocketClient(sockPath)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	for srv.Addr() == "" {
		if waitCtx.Err() != nil {
			t.Fatal("timed out waiting for socket server")
		}
		time.Sleep(5 * time.Millisecond)
	}
	tool := mcp.NewCerberusPortfolioHealthTool(sockClient)
	res, err := tool.Handler(context.Background(), map[string]any{
		"project":              "demo",
		"include_unsupervised": true,
	})
	if err != nil {
		t.Fatalf("cerberus_portfolio_health tool failed: %v", err)
	}
	rawStr, ok := res.(string)
	if !ok {
		t.Fatalf("expected string JSON result, got %T", res)
	}
	var decoded cerbapi.PortfolioHealthProjection
	if err = json.Unmarshal([]byte(rawStr), &decoded); err != nil {
		t.Fatalf("decode MCP tool output: %v", err)
	}
	if decoded.Contract != cerbapi.PortfolioHealthContract || len(decoded.Deployments) != 1 || decoded.Deployments[0].ID != "demo-ssh" {
		t.Fatalf("unexpected decoded projection: %+v", decoded)
	}
}
