package cerbapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// statusSocket is a daemon whose /approvals/{status} answers with that
// status: a JSON refusal, or for "raw" a body that is not JSON.
func statusSocket(t *testing.T) *SocketClient {
	t.Helper()
	sock := filepath.Join(tempSocketDir(t), "status.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/approvals/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/approvals/")
		if code, ok := strings.CutPrefix(rest, "raw"); ok {
			status, _ := strconv.Atoi(code)
			w.WriteHeader(status)
			_, _ = w.Write([]byte("upstream gateway said no"))
			return
		}
		status, _ := strconv.Atoi(rest)
		rec, _ := BeginHTTPRequest(w, r, SurfaceSocket)
		writeJSONError(rec, status, redact.Guidance("refused with %d; run `cerberus approvals list` to see why", status).Error())
	})
	srv := &http.Server{Handler: mux} //nolint:gosec // a test server on a temp unix socket
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return NewSocketClient(sock)
}

// A daemon refusal keeps the status the daemon answered with, for every
// class the console maps, and the wrapper changes neither its text nor how
// it renders.
func TestDaemonRefusalKeepsItsStatus(t *testing.T) {
	client := statusSocket(t)
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusLocked,
		http.StatusInternalServerError, http.StatusServiceUnavailable} {
		_, err := client.GetApproval(context.Background(), strconv.Itoa(status))
		got, ok := DaemonHTTPStatus(err)
		if !ok || got != status {
			t.Errorf("%d: DaemonHTTPStatus = %d, %v (%v)", status, got, ok, err)
		}
		want := "daemon: refused with " + strconv.Itoa(status) + "; run `cerberus approvals list` to see why"
		if err.Error() != want || redact.ErrorText(err) != want {
			t.Errorf("%d: text %q, rendered %q", status, err.Error(), redact.ErrorText(err))
		}
		var unreachable *DaemonUnreachableError
		if errors.As(err, &unreachable) {
			t.Errorf("%d: a delivered request read as never sent", status)
		}
	}
	_, err := client.GetApproval(context.Background(), "raw502")
	if got, ok := DaemonHTTPStatus(err); !ok || got != http.StatusBadGateway || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("non-JSON body: %d %v %v", got, ok, err)
	}
	if _, ok := DaemonHTTPStatus(errors.New("not from a daemon")); ok {
		t.Error("an error that is not a daemon refusal has a status")
	}
}
