package main

import (
	"github.com/hollis-labs/cerberus/internal/audit"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/loopback"
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

	client := cerbapi.NewInProcessClient(cerbapi.WithConfigV2(cfg), cerbapi.WithInProcessAudit(audit.NewMemory()))
	srv := buildCerberusMCPServer(client, slog.Default())
	h := mcpHTTPHandler(srv, "/mcp", loopback.NewGuard("127.0.0.1", "4785"))

	t.Run("discover", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"d1","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"cerberus-smoke-test","version":"0.0.0"},"io.modelcontextprotocol/clientCapabilities":{}}}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", "server/discover")
		req.Host = "127.0.0.1:4785"
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
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-03-26")
		req.Host = "127.0.0.1:4785"
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
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"r1","method":"tools/call","params":{"name":"cerberus_pipeline_run","arguments":{"pipeline_id":"smoke-pipeline","acknowledged":true}}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream, application/json")
		req.Header.Set("MCP-Protocol-Version", "2025-03-26")
		req.Host = "127.0.0.1:4785"
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

// TestCerberusMCPHTTPLoopbackGuard is the DNS-rebinding case for mcp-http: a
// page on evil.example re-pointed at 127.0.0.1 reaches the socket but carries
// its own Host, and must be refused before the MCP handler runs.
func TestCerberusMCPHTTPLoopbackGuard(t *testing.T) {
	client := cerbapi.NewInProcessClient(cerbapi.WithConfigV2(&config.ConfigV2{Version: 2}))
	h := mcpHTTPHandler(buildCerberusMCPServer(client, slog.Default()), "/mcp", loopback.NewGuard("127.0.0.1", "4785"))

	toolsList := func(host, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"l1","method":"tools/list","params":{}}`))
		req.Host = host
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-03-26")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for _, tc := range []struct {
		name, host, origin string
		want               int
	}{
		{"rebinding host", "evil.example:4785", "http://evil.example:4785", http.StatusForbidden},
		{"rebinding host without origin", "evil.example:4785", "", http.StatusForbidden},
		{"loopback host", "127.0.0.1:4785", "", http.StatusOK},
		{"localhost on a forwarded port", "localhost:9000", "", http.StatusOK},
		{"loopback origin", "localhost:4785", "http://localhost:4785", http.StatusOK},
		{"foreign origin", "127.0.0.1:4785", "http://evil.example", http.StatusForbidden},
		{"loopback origin on another port", "127.0.0.1:4785", "http://127.0.0.1:1234", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := toolsList(tc.host, tc.origin); rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}

	t.Run("health is behind the guard", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Host = "evil.example:4785"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("empty allow-origin is the loopback set, not every origin", func(t *testing.T) {
		if len(loopback.NewGuard("127.0.0.1", "4785").Origins()) == 0 {
			t.Fatal("guard with no extra origins must still hand go-mcp a non-empty list")
		}
	})

	t.Run("allow-origin adds an exact origin", func(t *testing.T) {
		g := loopback.NewGuard("127.0.0.1", "4785", "https://inspector.example")
		h := mcpHTTPHandler(buildCerberusMCPServer(client, slog.Default()), "/mcp", g)
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"l1","method":"tools/list","params":{}}`))
		req.Host = "127.0.0.1:4785"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-03-26")
		req.Header.Set("Origin", "https://inspector.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
	})
}
