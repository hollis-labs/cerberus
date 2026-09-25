package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gmcp "github.com/hollis-labs/go-mcp/server"

	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// A credential registered on a socket request is gone from every body the
// socket writes for it: a JSON error, a JSON result, and each envelope of a
// progress stream.
func TestSocketResponsesRenderThroughTheRequestScope(t *testing.T) {
	serve := func(h http.HandlerFunc, stream bool) string {
		req := httptest.NewRequest(http.MethodPost, "/connectors/x/operations/y", nil)
		if stream {
			req.Header.Set(ProgressHeaderName, "1")
		}
		rec := httptest.NewRecorder()
		w, r := BeginHTTPRequest(rec, req, SurfaceSocket)
		h(w, r)
		return rec.Body.String()
	}
	register := func(r *http.Request) { redact.ScopeFrom(r.Context()).Add("github/token", scopeSentinel) }

	bodies := map[string]string{
		"error": serve(func(w http.ResponseWriter, r *http.Request) {
			register(r)
			writeJSONError(w, http.StatusBadGateway, "vendor said "+scopeSentinel+" rejected")
		}, false),
		"result": serve(func(w http.ResponseWriter, r *http.Request) {
			register(r)
			writeJSON(w, http.StatusOK, map[string]any{"stdout": scopeSentinel})
		}, false),
	}
	s := &SocketServer{}
	bodies["stream"] = serve(func(w http.ResponseWriter, r *http.Request) {
		register(r)
		s.handleStream(w, r, func(ctx context.Context) (interface{}, error) {
			gmcp.NotifyMessage(ctx, "error", "stage failed: "+scopeSentinel)
			return nil, fmt.Errorf("upstream echoed %s", scopeSentinel)
		})
	}, true)
	for name, body := range bodies {
		if strings.Contains(body, scopeSentinel) {
			t.Errorf("%s body leaked the credential: %s", name, body)
		}
		if !strings.Contains(body, redact.Marker) {
			t.Errorf("%s body has no marker, so the test proved nothing: %s", name, body)
		}
	}

	// A writer BeginHTTPRequest did not produce renders through the regex
	// net alone, never through nothing.
	rec := httptest.NewRecorder()
	writeJSONError(rec, http.StatusBadGateway, "token=abcdef1234567890")
	if strings.Contains(rec.Body.String(), "abcdef1234567890") {
		t.Fatalf("an unscoped writer skipped the regex net: %s", rec.Body.String())
	}
}

// Execute's error is rendered through the scope the credentials were
// registered in, and keeps its type and code.
func TestScopeErrorRedactsAndKeepsTheConnectorError(t *testing.T) {
	scope := redact.NewScope()
	scope.Add("github/token", scopeSentinel)
	connErr := &ExternalConnectorError{Connector: "github", Operation: "status", Code: ExternalConnectorOperationFailed, Err: errors.New("401 for " + scopeSentinel)}
	for _, err := range []error{scopeError(scope, connErr), scopeError(scope, fmt.Errorf("wrapped: %w", &ExternalConnectorError{Connector: "github", Operation: "status", Code: ExternalConnectorOperationFailed, Err: errors.New("401 for " + scopeSentinel)}))} {
		if strings.Contains(err.Error(), scopeSentinel) {
			t.Errorf("error leaked the credential: %q", err)
		}
		var got *ExternalConnectorError
		if !errors.As(err, &got) || got.Code != ExternalConnectorOperationFailed {
			t.Errorf("errors.As lost the connector error: %v", err)
		}
	}
	if scopeError(scope, nil) != nil {
		t.Fatal("scopeError(nil) != nil")
	}
}

// The in-process lane renders a result the way the socket does, into the
// same DTO, so ssh exec output and docker logs are redacted on both lanes.
func TestRenderInProcessResultMatchesTheSocketLane(t *testing.T) {
	ctx := BeginRequest(context.Background(), SurfaceInProcess)
	redact.ScopeFrom(ctx).Add("github/token", scopeSentinel)
	args := ExternalConnectorOperationArgs{Connector: "ssh", Operation: "exec"}
	result, err := RenderInProcessResult(ctx, args, ExternalConnectorOperationResult{Connector: "ssh", Operation: "exec",
		Data: &sshconn.ExecResult{Stdout: "echo " + scopeSentinel, Stderr: "token=abcdef1234567890", ExitCode: 2}})
	if err != nil {
		t.Fatal(err)
	}
	exec, ok := result.Data.(*sshconn.ExecResult)
	if !ok {
		t.Fatalf("Data = %T, want *ssh.ExecResult", result.Data)
	}
	if strings.Contains(exec.Stdout, scopeSentinel) || strings.Contains(exec.Stderr, "abcdef1234567890") || exec.ExitCode != 2 {
		t.Fatalf("rendered = %+v", exec)
	}

	logs, err := RenderInProcessResult(ctx, ExternalConnectorOperationArgs{Connector: "docker", Operation: "logs"},
		ExternalConnectorOperationResult{Data: "line " + scopeSentinel + "\n"})
	if err != nil || logs.Data != "line "+redact.Marker+"\n" {
		t.Fatalf("docker logs = %#v, %v", logs.Data, err)
	}
}
