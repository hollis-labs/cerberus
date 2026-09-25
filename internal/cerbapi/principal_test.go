package cerbapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/redact"
)

func TestClassifyCLI(t *testing.T) {
	for _, c := range []struct {
		stdin, stdout bool
		marker        string
		want          PrincipalKind
	}{
		{true, true, "", PrincipalHuman},
		{true, true, "human", PrincipalHuman},
		{true, true, "agent", PrincipalAgent},
		{true, true, " AGENT ", PrincipalAgent},
		{false, true, "", PrincipalAgent},
		{true, false, "", PrincipalAgent},
		{false, false, "human", PrincipalAgent},
	} {
		got := ClassifyCLI(c.stdin, c.stdout, c.marker)
		if got.Principal.Kind != c.want || got.Principal.Via != ViaCLI || !got.Principal.UIDVerified || got.Explanation == "" {
			t.Errorf("stdin=%t stdout=%t marker=%q: %+v", c.stdin, c.stdout, c.marker, got)
		}
	}
}

func TestPrincipalClaimHeader(t *testing.T) {
	h := http.Header{}
	setPrincipalHeader(h, Principal{Kind: PrincipalHuman, Via: ViaCLI, Client: "cerberus-cli", UID: 0, UIDVerified: true})
	if strings.Contains(h.Get(PrincipalHeader), "uid") {
		t.Fatalf("the claim carries a uid: %s", h.Get(PrincipalHeader))
	}
	got, ok := principalFromHeader(h)
	if !ok || got.Kind != PrincipalHuman || got.Via != ViaCLI || !got.SelfReported {
		t.Fatalf("round trip: %+v", got)
	}
	for name, raw := range map[string]string{
		"automation": `{"kind":"automation","via":"monitor"}`,
		"unknown":    `{"kind":"root","via":"cli"}`,
	} {
		h.Set(PrincipalHeader, raw)
		if got, _ := principalFromHeader(h); got.Kind != PrincipalAgent {
			t.Errorf("%s claim read as %s, want agent", name, got.Kind)
		}
	}
	h.Set(PrincipalHeader, "{not json")
	if _, ok := principalFromHeader(h); ok {
		t.Error("a malformed claim was read")
	}
	h.Set(PrincipalHeader, `{"kind":"human","via":"`+strings.Repeat("x", 2000)+`"}`)
	if _, ok := principalFromHeader(h); ok {
		t.Error("an oversized claim was read")
	}
}

func TestRequestPrincipalPerSurface(t *testing.T) {
	human := WithPrincipal(context.Background(), Principal{Kind: PrincipalHuman, Via: ViaCLI})
	// The web console reads nothing the browser claims.
	if p, _ := PrincipalFrom(BeginRequest(human, SurfaceWeb)); p.Kind != PrincipalHuman || p.Via != ViaWeb || p.UIDVerified {
		t.Errorf("web: %+v", p)
	}
	fakeAgent := WithPrincipal(context.Background(), Principal{Kind: PrincipalAgent, Via: ViaMCPStdio})
	if p, _ := PrincipalFrom(BeginRequest(fakeAgent, SurfaceWeb)); p.Via != ViaWeb {
		t.Errorf("web took a claim: %+v", p)
	}
	// A socket caller without a claim is an agent; the uid is the peer's.
	peer := withPeer(context.Background(), peerCred{uid: 4242})
	if p, _ := PrincipalFrom(BeginRequest(peer, SurfaceSocket)); p.Kind != PrincipalAgent || p.Via != ViaUnknown || p.UID != 4242 || !p.UIDVerified || !p.SelfReported {
		t.Errorf("socket, no claim: %+v", p)
	}
	if p, _ := PrincipalFrom(BeginRequest(context.Background(), SurfaceMonitor)); p.Kind != PrincipalAutomation || p.SelfReported {
		t.Errorf("monitor: %+v", p)
	}
	if p, _ := PrincipalFrom(BeginRequest(human, SurfaceInProcess)); p.Kind != PrincipalHuman || p.UID != os.Getuid() || !p.UIDVerified {
		t.Errorf("in-process with a CLI claim: %+v", p)
	}
	if p, _ := PrincipalFrom(BeginRequest(context.Background(), SurfaceUnknown)); p.Kind != PrincipalAgent {
		t.Errorf("unknown: %+v", p)
	}
	run := pipelinePrincipal(WithPrincipal(context.Background(), Principal{Kind: PrincipalHuman, Via: ViaCLI, Client: "cerberus-cli"}), "release")
	if run.Kind != PrincipalAutomation || run.Via != ViaPipeline || run.Client != "pipeline:release" || run.OnBehalfOf != "human via cli (cerberus-cli)" {
		t.Errorf("pipeline: %+v", run)
	}
}

// startPeerSocket serves client on a real unix socket whose peer
// credentials come from creds.
func startPeerSocket(t *testing.T, client Client, creds func(net.Conn) peerCred) string {
	t.Helper()
	path := filepath.Join(os.TempDir(), fmt.Sprintf("cerb-peer-%d-%d.sock", os.Getpid(), time.Now().UnixNano()))
	server := NewSocketServer(client, path)
	if creds != nil {
		server.peerCreds = creds
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done; _ = os.Remove(path) })
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if conn, err := net.Dial("unix", path); err == nil {
			_ = conn.Close()
			return path
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket %s never appeared", path)
		}
	}
}

// A connection from another user, or one whose peer cannot be read, is
// refused with principal_refused before anything is served.
func TestSocketRefusesAnotherUser(t *testing.T) {
	for name, creds := range map[string]func(net.Conn) peerCred{
		"another uid": func(net.Conn) peerCred { return peerCred{uid: os.Getuid() + 1} },
		"unreadable":  func(net.Conn) peerCred { return peerCred{uid: -1, err: errors.New("no creds")} },
	} {
		t.Run(name, func(t *testing.T) {
			path := startPeerSocket(t, NewInProcessClient(), creds)
			_, err := NewSocketClient(path).WhoAmI(context.Background())
			var coded *ExternalConnectorError
			if !errors.As(err, &coded) || coded.Code != ExternalConnectorPrincipalRefused {
				t.Fatalf("err = %v, want principal_refused", err)
			}
			if got := redact.Text(err.Error()); got != err.Error() {
				t.Fatalf("redaction rewrote the refusal: %q", got)
			}
			if name == "another uid" && !strings.Contains(err.Error(), "run cerberus as that user") {
				t.Fatalf("the refusal does not name the recovery: %v", err)
			}
			// The daemon marks the refusal rendered: it is its own guidance,
			// rendered once in the request's scope.
			conn, dialErr := net.Dial("unix", path)
			if dialErr != nil {
				t.Fatal(dialErr)
			}
			defer func() { _ = conn.Close() }()
			if _, werr := conn.Write([]byte("GET /whoami HTTP/1.1\r\nHost: cerberus-daemon\r\nConnection: close\r\n\r\n")); werr != nil {
				t.Fatal(werr)
			}
			raw, _ := io.ReadAll(conn)
			if !strings.Contains(string(raw), "403 Forbidden") || !strings.Contains(string(raw), `"rendered":true`) || !strings.Contains(string(raw), `"code":"principal_refused"`) {
				t.Fatalf("socket body: %s", raw)
			}
		})
	}
}

// With the kernel's own answer, the daemon verifies the uid and records the
// caller's claim as self-reported — through to the audit record.
func TestSocketPrincipalReachesTheAuditRecord(t *testing.T) {
	sink := audit.NewMemory()
	external := auditedDockerService(sink)
	path := startPeerSocket(t, NewInProcessClient(WithExternalConnectorService(external)), nil)
	claim := func(context.Context) Principal {
		return Principal{Kind: PrincipalAgent, Via: ViaMCPStdio, Client: "claude-code/2.1"}
	}
	client := NewSocketClient(path, WithPrincipalClaim(claim))

	who, err := client.WhoAmI(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if who.Kind != PrincipalAgent || who.Via != ViaMCPStdio || who.Client != "claude-code/2.1" || who.UID != os.Getuid() || !who.UIDVerified || !who.SelfReported {
		t.Fatalf("whoami %+v", who)
	}

	_, _ = client.ExecuteConnectorOperation(context.Background(), ExternalConnectorOperationArgs{Connector: "docker", Operation: "list_containers"})
	recs := sink.Records()
	if len(recs) == 0 {
		t.Fatal("no records")
	}
	p := recs[0].Principal
	if p.Kind != "agent" || p.Via != ViaMCPStdio || p.Client != "claude-code/2.1" || p.Surface != "socket" || p.UID == nil || *p.UID != os.Getuid() || !p.UIDVerified || !p.SelfReported {
		t.Fatalf("recorded principal %+v", p)
	}

	// The claim can also ride on the context, as the CLI's does.
	plain := NewSocketClient(path)
	ctx := WithPrincipal(context.Background(), Principal{Kind: PrincipalHuman, Via: ViaCLI, Client: "cerberus-cli"})
	if who, _ := plain.WhoAmI(ctx); who.Kind != PrincipalHuman || who.Via != ViaCLI {
		t.Fatalf("ctx claim: %+v", who)
	}
	if who, _ := plain.WhoAmI(context.Background()); who.Kind != PrincipalAgent || who.Via != ViaUnknown {
		t.Fatalf("no claim: %+v", who)
	}
}
