package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/domain"
)

// shortSocketPath returns a unix-socket path short enough to fit under
// macOS' 104-char sun_path limit. t.TempDir paths are too long on
// darwin (/var/folders/.../T/TestName12345/001/), so we synthesize a
// short, unique path under os.TempDir and clean it up via t.Cleanup.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	// Use PID + ns timestamp for uniqueness without a long test name.
	name := fmt.Sprintf("cerb-%d-%d.sock", os.Getpid(), time.Now().UnixNano())
	p := filepath.Join(os.TempDir(), name)
	t.Cleanup(func() { _ = os.Remove(p) })
	return p
}

// startSocket boots a SocketServer against the given backend and returns
// a connected SocketClient plus a stop function. The socket path is
// short (macOS sun_path limit is ~104 chars; t.TempDir blows that).
func startSocket(t *testing.T, backend Client) (*SocketClient, func()) {
	t.Helper()
	sockPath := shortSocketPath(t)
	srv := NewSocketServer(backend, sockPath)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if runErr := srv.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Logf("server exited: %v", runErr)
		}
	}()

	// Poll until the socket shows up — Run returns before Serve blocks,
	// but net.Listen completes before srv.Run logs "listening". Give it
	// a generous deadline so slow CI doesn't flake.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, sErr := os.Stat(sockPath); sErr == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("socket %s never appeared", sockPath)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cli := NewSocketClient(sockPath)
	stop := func() {
		cancel()
		<-done
	}
	return cli, stop
}

func TestSocketServer_DaemonUnreachable(t *testing.T) {
	// Point the client at a nonexistent socket. Any RPC must return a
	// DaemonUnreachableError, matching the CERB-2 "daemon down" spec.
	// Use a short path (macOS sun_path limit) for a missing.sock.
	sockPath := shortSocketPath(t) + ".missing"
	cli := NewSocketClient(sockPath)
	_, err := cli.ListServices(context.Background())
	if err == nil {
		t.Fatal("expected error when dialing missing socket")
	}
	var dErr *DaemonUnreachableError
	if !errors.As(err, &dErr) {
		t.Fatalf("expected DaemonUnreachableError, got %T: %v", err, err)
	}
	if !strings.Contains(dErr.Error(), "daemon not running") {
		t.Fatalf("expected operator-facing hint, got: %s", dErr.Error())
	}
}

func TestSocketServer_ListServices(t *testing.T) {
	reg, _ := newTestRegistry(t, `
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
`)
	cli, stop := startSocket(t, NewInProcessClient(reg))
	defer stop()

	out, err := cli.ListServices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != "one" {
		t.Fatalf("unexpected services: %+v", out)
	}
}

func TestSocketServer_GetService_NotFound(t *testing.T) {
	reg, _ := newTestRegistry(t, `
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
`)
	cli, stop := startSocket(t, NewInProcessClient(reg))
	defer stop()

	_, err := cli.GetService(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "daemon:") {
		t.Fatalf("expected wrapped daemon error, got: %s", err.Error())
	}
}

func TestSocketServer_APIVersionMismatch(t *testing.T) {
	reg, _ := newTestRegistry(t, `
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
`)
	cli, stop := startSocket(t, NewInProcessClient(reg))
	defer stop()

	// Swap the version header on the client to simulate a future
	// client talking to a v1 server.
	cli.http.Transport = wrapTransport(cli.http.Transport, func(hdr map[string]string) {
		hdr[APIHeaderName] = "v99"
	})
	_, err := cli.ListServices(context.Background())
	if err == nil {
		t.Fatal("expected version mismatch error")
	}
	if !strings.Contains(err.Error(), "unsupported X-Cerberus-Api") {
		t.Fatalf("expected version-mismatch error, got: %s", err.Error())
	}
}

func TestSocketServer_StopService_EndToEnd(t *testing.T) {
	reg, _ := newTestRegistry(t, `
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
`)
	cli, stop := startSocket(t, NewInProcessClient(reg))
	defer stop()

	res, err := cli.StopService(context.Background(), "one", AuditContext{Reason: "e2e test"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("stop should succeed for unstarted service, got: %+v", res)
	}
	if res.ServiceID != "one" {
		t.Fatalf("expected service_id=one, got %s", res.ServiceID)
	}
}

func TestSocketServer_Logs(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "svc.log")
	if err := os.WriteFile(logPath, []byte("line1\nline2\nline3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reg, _ := newTestRegistry(t, fmt.Sprintf(`
version: 1
services:
  - id: logged
    name: Logged
    dir: /tmp/logged
    command: ["echo", "x"]
    log_file: %s
`, logPath))
	cli, stop := startSocket(t, NewInProcessClient(reg))
	defer stop()

	ll, err := cli.ServiceLogs(context.Background(), "logged", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ll.Content, "line2") || !strings.Contains(ll.Content, "line3") {
		t.Fatalf("expected last 2 lines, got: %q", ll.Content)
	}
	if strings.Contains(ll.Content, "line1") {
		t.Fatalf("did not expect line1, got: %q", ll.Content)
	}
}

func TestSocketServer_ResourceLogs(t *testing.T) {
	reg, _ := newTestRegistry(t, `
version: 1
services: []
`)
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	sourcePath := filepath.Join(workspace, "app")
	if err := os.WriteFile(sourcePath, []byte("v1"), 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	logDir := filepath.Join(home, ".cerberus", "apps", "p1", "r1", "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "stderr.log"), []byte("x\ny\nz\n"), 0600); err != nil {
		t.Fatal(err)
	}

	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Setenv("HOME", oldHome) }()

	cfg := &config.ConfigV2{
		Version: 2,
		Resources: []config.ResourceDef{
			{
				ID:        "r1",
				Name:      "Res One",
				Type:      string(domain.ResourceProcess),
				Project:   "p1",
				Connector: "local",
				Config: map[string]any{
					"mode":       "os_service",
					"run_from":   "artifact",
					"dir":        workspace,
					"command":    []any{"./app", "serve"},
					"supervisor": "launchd",
				},
			},
		},
	}
	cli, stop := startSocket(t, NewInProcessClient(reg, WithConfigV2(cfg)))
	defer stop()

	out, err := cli.ResourceLogs(context.Background(), "r1", 2, "stderr")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Content, "y") || !strings.Contains(out.Content, "z") {
		t.Fatalf("expected last 2 lines, got %q", out.Content)
	}
	if strings.Contains(out.Content, "x") {
		t.Fatalf("did not expect first line, got %q", out.Content)
	}
}

// TestSocketServer_ConcurrentStopsFromMultipleClients is the multi-session
// equivalent from the CERB-2 spec: 3 simulated MCP subprocesses fire
// lifecycle ops concurrently through the same daemon and all see
// consistent state.
func TestSocketServer_ConcurrentStopsFromMultipleClients(t *testing.T) {
	reg, _ := newTestRegistry(t, `
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
`)
	cli, stop := startSocket(t, NewInProcessClient(reg))
	defer stop()

	// Three in-parallel StopService calls simulate three MCP
	// subprocesses all acting on the same service. All should return
	// without error and with consistent ServiceID.
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := cli.GetService(context.Background(), "one")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent GetService error: %v", err)
		}
	}
}

// TestSocketServer_SurfacesConfigEditsWithoutRestart is the CERB-2 repro
// at the socket layer. A long-lived SocketClient (simulating the
// standalone `cerberus mcp` subprocess) must see services added to
// config.yaml after the daemon + socket server came up.
//
// The daemon's InProcessClient reloads the registry on every call
// (CERB-1), so the socket round-trip inherits that freshness.
func TestSocketServer_SurfacesConfigEditsWithoutRestart(t *testing.T) {
	reg, path := newTestRegistry(t, `
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
`)
	cli, stop := startSocket(t, NewInProcessClient(reg))
	defer stop()

	// Baseline: only service "one".
	out, err := cli.ListServices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("baseline: want 1 service, got %d", len(out))
	}

	// Edit config on disk. No daemon restart. No client reconstruction.
	if werr := os.WriteFile(path, []byte(`
version: 1
services:
  - id: one
    name: One
    dir: /tmp/one
    command: ["echo", "one"]
  - id: two
    name: Two
    dir: /tmp/two
    command: ["echo", "two"]
`), 0600); werr != nil {
		t.Fatal(werr)
	}

	// Post-edit: ListServices via the same long-lived client must see
	// the new service. If the daemon were caching config (CERB-2 bug),
	// this would still return 1.
	out, err = cli.ListServices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("post-edit: want 2 services (client sees daemon's fresh view), got %d", len(out))
	}

	// And the second service must resolve by name via the same client.
	s, err := cli.GetService(context.Background(), "two")
	if err != nil {
		t.Fatalf("GetService(two) after edit: %v", err)
	}
	if s.Name != "Two" {
		t.Fatalf("expected Name=Two, got %q", s.Name)
	}
}

// TestSocketServer_SocketPermissions verifies the 0600 perms spec.
func TestSocketServer_SocketPermissions(t *testing.T) {
	reg, _ := newTestRegistry(t, `
version: 1
services: []
`)
	cli, stop := startSocket(t, NewInProcessClient(reg))
	defer stop()
	// Trigger at least one request so we know the server is up.
	_, _ = cli.ListServices(context.Background())

	info, err := os.Stat(cli.dialPath)
	if err != nil {
		t.Fatal(err)
	}
	mode := info.Mode().Perm()
	if mode != 0600 {
		t.Fatalf("expected 0600 perms, got %#o", mode)
	}
}

// TestSocketServer_StaleSocketTakeover verifies the spec's "unlink
// stale socket" behavior. A pre-existing socket file (left by a
// crashed daemon) must not prevent a fresh listen.
func TestSocketServer_StaleSocketTakeover(t *testing.T) {
	sockPath := shortSocketPath(t)
	// Simulate a stale socket: drop a regular file at the path.
	if err := os.WriteFile(sockPath, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}

	reg, _ := newTestRegistry(t, `
version: 1
services: []
`)
	srv := NewSocketServer(NewInProcessClient(reg), sockPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- srv.Run(ctx)
	}()

	// Drain socket readiness.
	deadline := time.Now().Add(1 * time.Second)
	for {
		fi, sErr := os.Stat(sockPath)
		if sErr == nil && fi.Mode()&os.ModeSocket != 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("server failed to take over stale socket")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cli := NewSocketClient(sockPath)
	if _, err := cli.ListServices(ctx); err != nil {
		cancel()
		<-done
		t.Fatalf("server did not serve after stale takeover: %v", err)
	}
	cancel()
	<-done
}

// TestSocketServer_Ping hits /health via Ping to validate the
// connectivity-check shortcut used by the MCP entrypoint.
func TestSocketServer_Ping(t *testing.T) {
	reg, _ := newTestRegistry(t, `
version: 1
services: []
`)
	cli, stop := startSocket(t, NewInProcessClient(reg))
	defer stop()
	if err := cli.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

// ---- helpers ----

// headerMutatingTransport is a test-only http.RoundTripper that rewrites
// outgoing request headers before delegating to the wrapped transport.
// Used by TestSocketServer_APIVersionMismatch to synthesize a future
// client talking to today's server.
type headerMutatingTransport struct {
	base    http.RoundTripper
	mutator func(map[string]string)
}

func wrapTransport(base http.RoundTripper, mutator func(map[string]string)) http.RoundTripper {
	return &headerMutatingTransport{base: base, mutator: mutator}
}

func (t *headerMutatingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	hdrs := make(map[string]string)
	t.mutator(hdrs)
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	return t.base.RoundTrip(req)
}
