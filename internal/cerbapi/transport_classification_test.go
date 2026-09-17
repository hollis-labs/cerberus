package cerbapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func tempSocketDir(t *testing.T) string {
	t.Helper()
	// Not t.TempDir(): its path is long enough to exceed the sun_path limit,
	// and bind then fails with EINVAL rather than anything meaningful.
	dir, err := os.MkdirTemp("/tmp", "cerbsock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// DaemonUnreachableError is what licenses a caller to re-run an operation
// in-process, so it may only be returned when non-delivery is proven. A daemon
// that accepts the request and then dies — which is exactly what deploying the
// daemon does to an in-flight mutation — may already have executed it.
func TestTransportErrorAfterDeliveryIsNotUnreachable(t *testing.T) {
	sock := filepath.Join(tempSocketDir(t), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	delivered := make(chan struct{}, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		_, _ = conn.Read(make([]byte, 4096)) // the request arrived
		delivered <- struct{}{}
		_ = conn.Close() // ...and then the daemon went away
	}()

	err = NewSocketClient(sock).doJSON(context.Background(), http.MethodPost, "/v2/resources/x/reload", map[string]string{"id": "x"}, nil)
	<-delivered

	if err == nil {
		t.Fatal("expected a transport error")
	}
	var unreachable *DaemonUnreachableError
	if errors.As(err, &unreachable) {
		t.Fatalf("post-delivery error classified as unreachable, which licenses an in-process retry of a possible mutation: %v", err)
	}
}

// The fallback still has to work: a dial failure proves nothing was sent.
func TestDialFailureIsUnreachable(t *testing.T) {
	sock := filepath.Join(tempSocketDir(t), "absent.sock")
	err := NewSocketClient(sock).doJSON(context.Background(), http.MethodGet, "/ping", nil, nil)
	var unreachable *DaemonUnreachableError
	if !errors.As(err, &unreachable) {
		t.Fatalf("dial failure must stay retryable, got %v", err)
	}
}

// requestNeverSent is the whole predicate, so pin it directly: the question is
// "was this definitely never delivered", not "does this look like the daemon
// is down".
func TestRequestNeverSentOnlyAcceptsDialFailures(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"dial refused", &net.OpError{Op: "dial", Err: errors.New("connect: connection refused")}, true},
		{"dial timeout", &net.OpError{Op: "dial", Err: errors.New("i/o timeout")}, true},
		{"read reset", &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}, false},
		{"write reset", &net.OpError{Op: "write", Err: errors.New("broken pipe")}, false},
		{"eof", io.EOF, false},
		{"unexpected eof", io.ErrUnexpectedEOF, false},
		{"context deadline", context.DeadlineExceeded, false},
		{"context canceled", context.Canceled, false},
	} {
		if got := requestNeverSent(tt.err); got != tt.want {
			t.Errorf("requestNeverSent(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
