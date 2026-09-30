package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gmcp "github.com/hollis-labs/go-mcp/server"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/mcp"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/testfixture/sentinel"
)

// WP-S2 acceptance: a credential resolved during an operation cannot appear
// in that operation's error text even when someone else composes the
// message. A managed plugin (a real subprocess) is handed sentinel.Value
// through the registering provider, an upstream 401 echoes it back with no
// label, and the plugin wraps it in vendor-style text without scrubbing.
// Every surface that renders the failure is checked: the in-process CLI, the
// socket (JSON error and progress stream), MCP through the socket (`cerberus
// mcp` and mcp-http serve this tool list over it) as the tool generated for
// the exposed operation, the daemon's stdio MCP on InProcessClient, and the
// audit log. The console is checked in internal/webui, with the same fixture.
func TestResolvedCredentialNeverReachesAnySurface(t *testing.T) {
	if got := redact.Text("401 Bad credentials for " + sentinel.Value); got == "401 Bad credentials for "+redact.Marker {
		t.Fatal("precondition: the regex net alone removes the sentinel, so this test would prove nothing")
	}
	api := sentinel.UpstreamAPI(t)
	sink := audit.NewMemory()
	svc, managed := sentinel.Service(t, sink, sentinel.Provider(), api.URL, "TestSentinelPluginHelperProcess")
	inProc := cerbapi.NewInProcessClient(cerbapi.WithExternalConnectorService(svc),
		cerbapi.WithManagedPluginConnectorService(managed), cerbapi.WithInProcessAudit(sink))
	args := cerbapi.ExternalConnectorOperationArgs{Connector: sentinel.ConnectorID, Operation: sentinel.Operation, Config: sentinel.Args()}

	// The in-process CLI: the lane the operator's shell takes with no
	// daemon, printed the way main prints an error.
	_, err := localConnectorExecutor{svc: svc}.Execute(context.Background(), args)
	var connErr *cerbapi.ExternalConnectorError
	if !errors.As(err, &connErr) || connErr.Code != cerbapi.ExternalConnectorOperationFailed {
		t.Fatalf("in-process: err = %v, want operation_failed from the 401", err)
	}
	sentinel.AssertAbsent(t, "in-process CLI", redact.Text(err.Error()))

	// The socket, which always streams: the error envelope and every
	// notification sent while the call ran.
	client := startAcceptanceSocket(t, inProc)
	var notes []string
	ctx := gmcp.WithNotifier(context.Background(), func(n gmcp.Notification) {
		data, _ := json.Marshal(n)
		notes = append(notes, string(data))
	})
	_, err = client.ExecuteConnectorOperation(ctx, args)
	if err == nil {
		t.Fatal("socket: want the 401")
	}
	sentinel.AssertAbsent(t, "socket", err.Error())
	for _, note := range notes {
		if containsValue(note) {
			t.Errorf("socket notification leaked: %s", note)
		}
	}

	// MCP: over the socket, and on the daemon's own stdio server.
	for surface, c := range map[string]cerbapi.Client{"MCP over the socket": client, "daemon stdio MCP": inProc} {
		set, err := mcp.PluginTools(context.Background(), c, mcp.ReservedToolNames(mcp.AllTools(c)))
		if err != nil || len(set.Refused) > 0 {
			t.Fatalf("%s: generated tools: %v %v", surface, err, set.Refused)
		}
		tool := toolNamed(t, set.Tools, sentinel.ToolName)
		result, err := tool.Handler(context.Background(), sentinel.Args())
		if err == nil {
			t.Fatalf("%s: want the 401, got %v", surface, result)
		}
		sentinel.AssertAbsent(t, surface, err.Error())
		var structured interface{ ToolErrorContent() any }
		if errors.As(err, &structured) {
			data, _ := json.Marshal(structured.ToolErrorContent())
			if containsValue(string(data)) {
				t.Errorf("%s: error content leaked: %s", surface, data)
			}
		}
	}

	// The audit log holds every call's intent and outcome.
	records, _ := json.Marshal(sink.Records())
	if len(sink.Records()) == 0 || containsValue(string(records)) {
		t.Fatalf("audit records = %s", records)
	}
}

// TestSentinelPluginHelperProcess is the fixture plugin's entrypoint: the
// test binary re-executed by the plugin host. It does nothing in a normal run.
func TestSentinelPluginHelperProcess(*testing.T) { sentinel.ServeHelper() }

func containsValue(s string) bool { return strings.Contains(s, sentinel.Value) }

func toolNamed(t *testing.T, tools []mcp.Tool, name string) mcp.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("%s is not served", name)
	return mcp.Tool{}
}

func startAcceptanceSocket(t *testing.T, client cerbapi.Client) *cerbapi.SocketClient {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cerbacc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	srv := cerbapi.NewSocketServer(client, sock)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if conn, err := net.Dial("unix", sock); err == nil {
			_ = conn.Close()
			return cerbapi.NewSocketClient(sock)
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket %s never appeared", sock)
		}
	}
}
