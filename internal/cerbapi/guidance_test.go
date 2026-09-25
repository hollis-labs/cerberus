package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/connector"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/pluginhost"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// lanes renders err the three ways an operator sees a refusal: the
// in-process CLI (ErrorText on the error Execute returned), the socket's
// JSON error body, and a client of that socket (daemonError from the body,
// then ErrorText). Each returns the text the operator reads.
func lanes(t *testing.T, err error) map[string]string {
	t.Helper()
	ctx := BeginRequest(context.Background(), SurfaceInProcess)
	rendered := scopeError(redact.ScopeFrom(ctx), err)

	rec := httptest.NewRecorder()
	w, r := BeginHTTPRequest(rec, httptest.NewRequest(http.MethodPost, "/x", nil), SurfaceSocket)
	served := scopeError(redact.ScopeFrom(r.Context()), err)
	writeServiceError(w, http.StatusInternalServerError, served)
	var body ErrorResponse
	if jerr := json.Unmarshal(rec.Body.Bytes(), &body); jerr != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), jerr)
	}
	if !body.Rendered {
		t.Errorf("the daemon did not mark its rendered error: %s", rec.Body.String())
	}
	return map[string]string{
		"in-process": redact.ErrorText(rendered),
		"socket":     body.Error,
		"client":     strings.TrimPrefix(redact.ErrorText(daemonError(body.Error, body.connectorErrorWire)), "daemon: "),
	}
}

// Every refusal converted to guidance reaches the operator exactly as
// Cerberus wrote it, on every lane.
func TestConvertedRefusalsSurviveEveryLane(t *testing.T) {
	gate := gateFakeConnector{}.Definition()
	writeOp, _ := gate.Operation("create_thing")
	sshRegistry := connector.NewRegistry()
	sshRegistry.RegisterDefinition(sshconn.Definition())
	sshSvc := NewExternalConnectorService(audit.NewMemory(), sshRegistry)
	args := ExternalConnectorOperationArgs{Connector: "gatefake", Operation: "create_thing"}
	localWrite := writeOp
	localWrite.Effect, localWrite.LocalFS = contract.EffectRead, contract.LocalFSWrites

	sites := map[string]error{
		"ack":                      requireAcknowledgment(args, writeOp),
		"ack, local writes":        requireAcknowledgment(args, localWrite),
		"preview_unsupported":      previewUnsupportedError(args),
		"unsupported":              ghostRefusal(NewExternalConnectorService(audit.NewMemory(), connector.NewRegistry())),
		"runtime gate unsupported": runtimeGate(context.Background(), gate, "nope", nil, MutationOpts{}),
		"runtime gate inputs":      runtimeGate(context.Background(), gate, "create_thing", map[string]any{"bogus": 1}, MutationOpts{}),
		"runtime gate ack":         runtimeGate(context.Background(), gate, "create_thing", map[string]any{"name": "x"}, MutationOpts{}),
		"plugin ack":               managedPluginExecuteError(args, fmt.Errorf("%s operation %q %w", contract.EffectWrite, "create_thing", pluginhost.ErrAckRequired)),
		"plugin preview":           managedPluginExecuteError(args, fmt.Errorf("plugin %q operation %q: %w", "gatefake", "create_thing", pluginhost.ErrPreviewUnsupported)),
		"plugin missing secret": managedPluginExecuteError(args, &pluginhost.MissingSecretsError{Connector: "contextforge", Secrets: []string{"token"},
			Err: errors.New("401 from the gateway")}),
	}
	sites["principal_refused"] = (&SocketServer{uid: os.Getuid()}).checkPeer(withPeer(context.Background(), peerCred{uid: os.Getuid() + 1}))
	_, sites["ssh input refusal"] = sshSvc.declaredOperation(context.Background(),
		ExternalConnectorOperationArgs{Connector: "ssh", Operation: "exec", Config: map[string]any{"host": "box", "command": "uptime"}})

	for name, err := range sites {
		t.Run(name, func(t *testing.T) {
			if err == nil {
				t.Fatal("the site did not refuse")
			}
			want := err.Error()
			for lane, got := range lanes(t, err) {
				if got != want {
					t.Errorf("%s changed the refusal:\n got %q\nwant %q", lane, got, want)
				}
			}
		})
	}
}

// Guidance the regex net would eat arrives intact, which is the point: the
// prose is rendered once and never re-ruled. Its cause is still redacted.
func TestGuidanceTheNetWouldEatArrivesIntact(t *testing.T) {
	const prose = "the plugin rejected its token: rotated keys need a reload"
	if redact.Text(prose) == prose {
		t.Fatal("precondition: the regex net should eat this prose")
	}
	err := externalConnectorError(ExternalConnectorOperationArgs{Connector: "demo", Operation: "sync"}, ExternalConnectorOperationFailed,
		redact.GuidanceWrap(errors.New("upstream echoed token=abcdef1234567890"), "the plugin rejected its %s: rotated keys need a reload", "token"))
	for lane, got := range lanes(t, err) {
		if !strings.Contains(got, prose) || strings.Contains(got, "abcdef1234567890") {
			t.Errorf("%s = %q", lane, got)
		}
	}
}

// Version skew: a client trusts daemon text only when the daemon marked it
// rendered. Without the marker — an older daemon — the rules run.
func TestClientTrustsDaemonTextOnlyWhenMarked(t *testing.T) {
	const prose = "demo sync: operation_failed: the plugin rejected its token: rotated keys need a reload"
	wire := connectorErrorWire{Code: ExternalConnectorOperationFailed, Connector: "demo", Operation: "sync",
		Detail: "the plugin rejected its token: rotated keys need a reload"}

	unmarked := redact.ErrorText(daemonError(prose, wire))
	if strings.Contains(unmarked, "rotated keys") {
		t.Fatalf("unmarked daemon text skipped the rules: %q", unmarked)
	}
	var connErr *ExternalConnectorError
	if !errors.As(daemonError(prose, wire), &connErr) || connErr.Code != ExternalConnectorOperationFailed {
		t.Fatal("the unmarked error lost its code")
	}

	wire.Rendered = true
	marked := daemonError(prose, wire)
	if got := redact.ErrorText(marked); got != "daemon: "+prose {
		t.Fatalf("marked daemon text = %q, want it as rendered", got)
	}
	if !errors.As(marked, &connErr) || connErr.Code != ExternalConnectorOperationFailed {
		t.Fatal("the marked error lost its code")
	}

	if got := redact.ErrorText(daemonError("plain "+prose, connectorErrorWire{})); strings.Contains(got, "rotated keys") {
		t.Fatalf("an uncoded unmarked error skipped the rules: %q", got)
	}
	if got := redact.ErrorText(daemonError(prose, connectorErrorWire{Rendered: true})); got != "daemon: "+prose {
		t.Fatalf("an uncoded marked error = %q", got)
	}
}

// The daemon marks only text it rendered: a message composed at the handler
// from an error's text, or a detail nobody rendered, goes out unmarked.
func TestDaemonMarksOnlyWhatItRendered(t *testing.T) {
	rec := httptest.NewRecorder()
	w, _ := BeginHTTPRequest(rec, httptest.NewRequest(http.MethodGet, "/x", nil), SurfaceSocket)
	writeJSONError(w, http.StatusBadRequest, "decode body: unexpected EOF")
	if strings.Contains(rec.Body.String(), `"rendered"`) {
		t.Fatalf("an unrendered message was marked: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	w, r := BeginHTTPRequest(rec, httptest.NewRequest(http.MethodGet, "/x", nil), SurfaceSocket)
	err := scopeError(redact.ScopeFrom(r.Context()), externalConnectorError(ExternalConnectorOperationArgs{Connector: "demo", Operation: "sync"},
		ExternalConnectorOperationFailed, errors.New("vendor text")))
	writeServiceError(w, http.StatusBadGateway, fmt.Errorf("wrapped at the handler: %w", err))
	if strings.Contains(rec.Body.String(), `"rendered"`) {
		t.Fatalf("text composed around a rendered error was marked: %s", rec.Body.String())
	}
}
