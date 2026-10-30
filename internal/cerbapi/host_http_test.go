package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
)

// serveOnSocket runs a real socket server over client and returns a real
// socket client to it.
func serveOnSocket(t *testing.T, client Client) *SocketClient {
	t.Helper()
	sockPath := filepath.Join(os.TempDir(), fmt.Sprintf("cerb-host-%d-%d.sock", os.Getpid(), time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(sockPath) })
	srv := NewSocketServer(client, sockPath)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("socket server: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if conn, err := net.Dial("unix", sockPath); err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return NewSocketClient(sockPath)
}

func TestHostsOverTheSocketMatchTheService(t *testing.T) {
	f := newCapacityFixture(t)
	client := serveOnSocket(t, NewInProcessClient(WithHostService(f.svc)))
	ctx := context.Background()

	views, err := client.ListHosts(ctx, HostFilter{Role: "worker", Labels: map[string]string{"agents": "true"}})
	if err != nil || !same(names(views), []string{"alpha", "bravo", "zulu"}) {
		t.Fatalf("list = %v, %v", names(views), err)
	}
	got, err := client.GetHost(ctx, "alpha")
	if err != nil || got.Probed == nil || got.Probed.CPU != 8 || got.ProbedAt == nil || got.Stale {
		t.Fatalf("get = %+v, %v", got, err)
	}
	res, err := client.HostCapacity(ctx, CapacityQuery{MinMemoryAvailable: 4 * hosts.Size(gib), MaxStaleAge: 10 * time.Minute, Explain: true})
	if err != nil {
		t.Fatal(err)
	}
	local, _ := f.svc.Capacity(ctx, CapacityQuery{MinMemoryAvailable: 4 * hosts.Size(gib), MaxStaleAge: 10 * time.Minute, Explain: true})
	a, _ := json.Marshal(res)
	b, _ := json.Marshal(local)
	if string(a) != string(b) || !same(names(res.Matched), []string{"alpha", "zulu"}) || len(res.Excluded) == 0 {
		t.Fatalf("capacity over the socket differs:\n%s\n%s", a, b)
	}

	// Refusals arrive as the service wrote them.
	if _, err = client.GetHost(ctx, "nope"); err == nil || !strings.Contains(err.Error(), `host "nope" is not declared; see `+"`cerberus host list`") {
		t.Fatalf("not found = %v", err)
	} else if status, ok := DaemonHTTPStatus(err); !ok || status != http.StatusNotFound {
		t.Fatalf("not found status = %d, %v", status, ok)
	}
	_, err = client.HostCapacity(ctx, CapacityQuery{RequireTools: []string{"kubectl"}})
	if status, _ := DaemonHTTPStatus(err); status != http.StatusBadRequest || !strings.Contains(err.Error(), "docker, tmux, tailscale, caddy") {
		t.Fatalf("invalid query = %v (status %d)", err, status)
	}
}

func TestHostProbeOverTheSocketRecordsFacts(t *testing.T) {
	f := newHostFixture(t, &probeBackend{stdout: goodProbeOutput})
	client := serveOnSocket(t, NewInProcessClient(WithHostService(f.svc)))
	v, err := client.ProbeHost(context.Background(), "box-a")
	if err != nil || v.State != hosts.StateOnline || v.Probed == nil || v.Probed.CPU != 4 {
		t.Fatalf("probe = %+v, %v", v, err)
	}
	if f.backend.execs != 1 {
		t.Fatalf("the probe ran %d times", f.backend.execs)
	}
	// An unreachable host is a state, relayed as one.
	f.backend.connectErr = errors.New("dial tcp 192.0.2.10:22: i/o timeout")
	*f.clock = f.clock.Add(time.Hour)
	v, err = client.ProbeHost(context.Background(), "box-a")
	if err != nil || v.State != hosts.StateOffline || !strings.Contains(v.Reason, "did not answer") {
		t.Fatalf("offline probe = %+v, %v", v, err)
	}
}

// A daemon that predates the host routes answers 404; the client says so in
// words instead of printing "404 page not found".
func TestHostsAgainstADaemonWithoutHostRoutes(t *testing.T) {
	sock := filepath.Join(tempSocketDir(t), "old.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.NewServeMux()} //nolint:gosec // a test server on a temp unix socket
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	_, err = NewSocketClient(sock).ListHosts(context.Background(), HostFilter{})
	if err == nil || !strings.Contains(err.Error(), "does not have host support") {
		t.Fatalf("old daemon = %v", err)
	}
	var unreachable *DaemonUnreachableError
	if errors.As(err, &unreachable) {
		t.Fatal("a delivered request read as never sent")
	}
}

// Only the documented verbs on the documented paths. DELETE and PATCH are the
// writes (Hosts C), which refuse a request with no revision before anything runs.
func TestHostRoutesAcceptOnlyTheirVerbs(t *testing.T) {
	f := newCapacityFixture(t)
	h := HostHTTP(HostClientOf(NewInProcessClient(WithHostService(f.svc))), "/hosts")
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/hosts", 200}, {"GET", "/hosts/alpha", 200}, {"POST", "/hosts/capacity", 200},
		{"DELETE", "/hosts/alpha", 428}, {"PUT", "/hosts/alpha", 404}, {"POST", "/hosts", 404},
		{"GET", "/hosts/alpha/probe", 404}, {"POST", "/hosts/alpha", 404}, {"GET", "/hosts/a/b/c", 404},
		{"GET", "/hosts/nope", 404},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		w, r := BeginHTTPRequest(rec, req, SurfaceSocket)
		h.ServeHTTP(w, r)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	// A body field this build does not know is refused, not ignored.
	rec := httptest.NewRecorder()
	w, r := BeginHTTPRequest(rec, httptest.NewRequest("POST", "/hosts/capacity", strings.NewReader(`{"min_gpu":1}`)), SurfaceSocket)
	h.ServeHTTP(w, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown constraint = %d: %s", rec.Code, rec.Body.String())
	}
}

// A serving host with no host service answers in words, on every route.
func TestHostsWithoutAServiceSaySo(t *testing.T) {
	client := serveOnSocket(t, NewInProcessClient())
	_, err := client.ListHosts(context.Background(), HostFilter{})
	if err == nil || !strings.Contains(err.Error(), "host support is not available") {
		t.Fatalf("err = %v", err)
	}
}
