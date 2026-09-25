package cerbapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/connector"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// Every code has a status, and none of them is a 500: a refusal is the
// caller's to act on, not a server fault.
func TestExternalConnectorHTTPStatusCoversEveryCode(t *testing.T) {
	if len(externalConnectorHTTPStatus) != len(externalConnectorErrorCodes) {
		t.Fatalf("status table has %d codes, vocabulary has %d", len(externalConnectorHTTPStatus), len(externalConnectorErrorCodes))
	}
	for _, code := range externalConnectorErrorCodes {
		status, ok := externalConnectorHTTPStatus[code]
		if !ok {
			t.Errorf("%s has no HTTP status", code)
			continue
		}
		if status < 400 || status == http.StatusInternalServerError {
			t.Errorf("%s maps to %d", code, status)
		}
		err := &ExternalConnectorError{Code: code}
		if got := ExternalConnectorHTTPStatus(err, http.StatusTeapot); got != status {
			t.Errorf("%s: ExternalConnectorHTTPStatus = %d, want %d", code, got, status)
		}
	}
	if got := ExternalConnectorHTTPStatus(errors.New("plain"), http.StatusTeapot); got != http.StatusTeapot {
		t.Errorf("uncoded error: got %d, want the fallback", got)
	}
}

// codedErrorClient answers every connector operation with err.
type codedErrorClient struct {
	Client
	err error
}

func (c codedErrorClient) ExecuteConnectorOperation(context.Context, ExternalConnectorOperationArgs) (ExternalConnectorOperationResult, error) {
	return ExternalConnectorOperationResult{}, c.err
}

func codedRefusal(code ExternalConnectorErrorCode) *ExternalConnectorError {
	return &ExternalConnectorError{
		Code:      code,
		Connector: "digitalocean",
		Operation: "stop",
		Err:       errors.New("refused for the test; run `cerberus droplet stop 42 --ack` to retry"),
	}
}

// A refusal crosses the socket with its code. The client rebuilds an
// *ExternalConnectorError with the same code, target and text, on both the
// streamed and the plain response paths, and the socket answers with the
// table's status rather than a 500.
func TestConnectorErrorCodeSurvivesTheSocket(t *testing.T) {
	for _, code := range externalConnectorErrorCodes {
		t.Run(string(code), func(t *testing.T) {
			want := codedRefusal(code)
			sock := startConnectorSocket(t, codedErrorClient{
				Client: NewInProcessClient(WithExternalConnectorService(NewExternalConnectorService(connector.NewRegistry()))),
				err:    want,
			})

			// Streamed: the path every SocketClient connector call takes.
			_, err := sock.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{Connector: "digitalocean", Operation: "stop"})
			assertRebuiltRefusal(t, err, want)

			// Plain: the status and the body a non-streaming caller sees.
			status, body := postSocket(t, sock, "/connectors/digitalocean/operations/stop")
			if wantStatus := externalConnectorHTTPStatus[code]; status != wantStatus {
				t.Fatalf("socket status %d, want %d", status, wantStatus)
			}
			var resp ErrorResponse
			if err := json.Unmarshal(body, &resp); err != nil {
				t.Fatalf("decode %s: %v", body, err)
			}
			if resp.Code != code || resp.Error != want.Error() {
				t.Fatalf("wire lost the refusal: %+v", resp)
			}
			assertRebuiltRefusal(t, daemonError(resp.Error, resp.connectorErrorWire), want)
		})
	}
}

func assertRebuiltRefusal(t *testing.T, err error, want *ExternalConnectorError) {
	t.Helper()
	var got *ExternalConnectorError
	if !errors.As(err, &got) {
		t.Fatalf("code lost across the socket: %T %v", err, err)
	}
	if got.Code != want.Code || got.Connector != want.Connector || got.Operation != want.Operation {
		t.Fatalf("rebuilt %+v, want %+v", got, want)
	}
	if got.Error() != want.Error() {
		t.Fatalf("text changed:\n got %q\nwant %q", got.Error(), want.Error())
	}
	if !strings.Contains(err.Error(), "--ack") {
		t.Fatalf("recovery instruction lost: %q", err.Error())
	}
	if redacted := redact.Text(err.Error()); redacted != err.Error() {
		t.Fatalf("redact.Text changed the refusal:\n got %q\nwant %q", redacted, err.Error())
	}
}

// postSocket sends a request without the progress header, so the server
// answers with a status and an ErrorResponse rather than a stream.
func postSocket(t *testing.T, sock *SocketClient, path string) (int, []byte) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock.dialPath)
	}}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://cerberus-daemon"+path, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(APIHeaderName, APIVersion)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}
