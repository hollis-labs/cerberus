package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/connector"
)

// refusingDaemon is the daemon side: every connector operation is refused
// with one coded error.
type refusingDaemon struct {
	cerbapi.Client
	err error
}

func (d refusingDaemon) ExecuteConnectorOperation(context.Context, cerbapi.ExternalConnectorOperationArgs) (cerbapi.ExternalConnectorOperationResult, error) {
	return cerbapi.ExternalConnectorOperationResult{}, d.err
}

// startRefusingDaemon serves d on a real unix socket, so the console talks to
// it the way it talks to the running daemon.
func startRefusingDaemon(t *testing.T, d refusingDaemon) *cerbapi.SocketClient {
	t.Helper()
	path := filepath.Join(os.TempDir(), fmt.Sprintf("cerb-web-%d-%d.sock", os.Getpid(), time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(path) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = cerbapi.NewSocketServer(d, path).Run(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket %s never appeared", path)
		}
	}
	return cerbapi.NewSocketClient(path)
}

// A refusal the daemon reports reaches the console with the daemon's status,
// its code, and its text under `message` — the field the console's API client
// renders. Before this, the socket client flattened the error to a string, so
// every refusal was a 500 and the console showed "Internal Server Error".
func TestWebConnectorRefusalsKeepTheirStatusAndMessage(t *testing.T) {
	wantStatus := map[cerbapi.ExternalConnectorErrorCode]int{
		cerbapi.ExternalConnectorInvalidArgs:        http.StatusBadRequest,
		cerbapi.ExternalConnectorAckRequired:        http.StatusConflict,
		cerbapi.ExternalConnectorPreviewUnsupported: http.StatusUnprocessableEntity,
		cerbapi.ExternalConnectorUnsupported:        http.StatusNotFound,
		cerbapi.ExternalConnectorUnavailable:        http.StatusServiceUnavailable,
		cerbapi.ExternalConnectorCredentialMissing:  http.StatusServiceUnavailable,
	}
	for _, code := range cerbapi.ExternalConnectorErrorCodes() {
		t.Run(string(code), func(t *testing.T) {
			status, ok := wantStatus[code]
			if !ok {
				t.Fatalf("%s: add its console status to this test", code)
			}
			refusal := &cerbapi.ExternalConnectorError{Code: code, Connector: "digitalocean", Operation: "stop",
				Err: errors.New("refused for the test; retry with acknowledged=true")}
			sock := startRefusingDaemon(t, refusingDaemon{
				Client: cerbapi.NewInProcessClient(cerbapi.WithExternalConnectorService(cerbapi.NewExternalConnectorService(connector.NewRegistry()))),
				err:    refusal,
			})
			handler := mustNew(t, sock).Handler(testGuard())
			req := newTestRequest(http.MethodPost, "/api/connectors/digitalocean/operations/stop", strings.NewReader(`{"config":{"droplet_id":42}}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Cerberus-Web-Token", sessionToken(t, handler))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != status {
				t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body.String())
			}
			var body struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode %s: %v", rec.Body.String(), err)
			}
			if body.Code != string(code) {
				t.Fatalf("code %q, want %q", body.Code, code)
			}
			if !strings.Contains(body.Message, refusal.Error()) || !strings.Contains(body.Message, "acknowledged=true") {
				t.Fatalf("message lost: %q", body.Message)
			}
		})
	}
}
