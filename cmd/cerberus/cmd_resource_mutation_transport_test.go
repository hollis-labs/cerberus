package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/cerbapi"
)

// startHangUpSocket stands up a daemon that answers the availability probe and
// then drops the connection on the operation itself — a daemon restarting
// mid-mutation, which is precisely what deploying the daemon looks like from
// the CLI side.
func startHangUpSocket(t *testing.T) {
	t.Helper()
	homeDir, err := os.MkdirTemp("/tmp", "cerbmut-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(homeDir) })
	t.Setenv("HOME", homeDir)
	if err = os.MkdirAll(homeDir+"/.cerberus", 0o750); err != nil {
		t.Fatal(err)
	}
	socketPath, err := cerbapi.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{}"))
	})
	// Hijack and close rather than panic: the client sees the same EOF, and
	// the test output stays readable.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		conn, _, herr := w.(http.Hijacker).Hijack()
		if herr == nil {
			_ = conn.Close()
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 0}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// The invariant from cmd_transport.go: once an operation is sent, an error must
// never trigger an in-process retry of a possible mutation. A mutation
// therefore picks its transport before sending, and the error it gets back must
// not be the one that licenses a retry.
func TestResourceMutationChoosesTransportBeforeSending(t *testing.T) {
	startHangUpSocket(t)

	client, err := resourceMutationSocket(context.Background())
	if err != nil {
		t.Fatalf("choosing a transport failed: %v", err)
	}
	if client == nil {
		t.Fatal("daemon answered the probe, so the daemon lane must be chosen")
	}

	_, err = client.ReloadResource(context.Background(), "some-resource")
	if err == nil {
		t.Fatal("expected the hung-up mutation to fail")
	}
	var unreachable *cerbapi.DaemonUnreachableError
	if errors.As(err, &unreachable) {
		t.Fatalf("a mutation that may have executed came back as retryable: %v", err)
	}
	if !strings.Contains(err.Error(), "may have been executed") {
		t.Errorf("error should tell the operator the mutation is of unknown outcome: %v", err)
	}
}

// The fallback is not gone, only moved earlier: with no daemon listening, the
// mutation lane hands back a nil client and the caller runs in-process.
func TestResourceMutationFallsBackWhenNoDaemonIsListening(t *testing.T) {
	homeDir, err := os.MkdirTemp("/tmp", "cerbmut-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(homeDir) })
	t.Setenv("HOME", homeDir)

	client, err := resourceMutationSocket(context.Background())
	if err != nil {
		t.Fatalf("a missing daemon is not an error, it is the in-process lane: %v", err)
	}
	if client != nil {
		t.Fatal("no daemon is listening, so the in-process lane must be chosen")
	}
}
