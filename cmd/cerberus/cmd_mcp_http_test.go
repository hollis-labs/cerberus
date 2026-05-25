package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/chrispian/cerberus/internal/config"
	httptransport "github.com/hollis-labs/go-mcp/transport/http"
)

func TestCerberusMCPHTTPSmoke(t *testing.T) {
	cfg := &config.ConfigV2{
		Version: 2,
		Pipelines: []config.PipelineDef{
			{
				ID:   "smoke-pipeline",
				Name: "Smoke Pipeline",
				Stages: []config.StageDef{
					{
						Name: "smoke",
						Actions: []config.ActionDef{
							{
								Type:    "shell",
								Command: "printf smoke-ok",
							},
						},
					},
				},
			},
		},
	}

	client := cerbapi.NewInProcessClient(cerbapi.WithConfigV2(cfg))
	srv := buildCerberusMCPServer(client, slog.Default())
	h := httptransport.NewHandler(srv, httptransport.HandlerOptions{})

	t.Run("discover", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"d1","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2025-03-26"}}}`))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Mcp-Method", "server/discover")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, `"supportedVersions"`) {
			t.Fatalf("body = %s, want supportedVersions", body)
		}
	})

	t.Run("tools list", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"l1","method":"tools/list","params":{}}`))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Mcp-Method", "tools/list")
		req.Header.Set("MCP-Protocol-Version", "2025-03-26")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"cerberus_pipeline_run"`) {
			t.Fatalf("body = %s, want cerberus_pipeline_run", body)
		}
	})

	t.Run("pipeline run SSE", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"r1","method":"tools/call","params":{"name":"cerberus_pipeline_run","arguments":{"pipeline_id":"smoke-pipeline"}}}`))
		req.Header.Set("Accept", "text/event-stream, application/json")
		req.Header.Set("Mcp-Method", "tools/call")
		req.Header.Set("Mcp-Name", "cerberus_pipeline_run")
		req.Header.Set("MCP-Protocol-Version", "2025-03-26")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
			t.Fatalf("Content-Type = %q, want text/event-stream", got)
		}

		body := rec.Body.String()
		if !strings.Contains(body, `"method":"notifications/message"`) {
			t.Fatalf("body = %s, want message notification", body)
		}
		if !strings.Contains(body, `"method":"notifications/progress"`) {
			t.Fatalf("body = %s, want progress notification", body)
		}
		if !strings.Contains(body, `Starting pipeline smoke-pipeline`) {
			t.Fatalf("body = %s, want pipeline start message", body)
		}
		if !strings.Contains(body, `Stage smoke started`) {
			t.Fatalf("body = %s, want stage start message", body)
		}
		if !strings.Contains(body, `pipeline_id`) || !strings.Contains(body, `smoke-pipeline`) {
			t.Fatalf("body = %s, want final pipeline result", body)
		}
	})
}
