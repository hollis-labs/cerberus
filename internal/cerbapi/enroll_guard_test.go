package cerbapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/presence"
	"github.com/hollis-labs/cerberus/internal/presence/presencetest"
	"github.com/hollis-labs/cerberus/internal/redact"
)

// Enrolling a passkey is a person's act, held by the daemon (B1): over the
// real socket, a caller with no claim (an agent), an agent over MCP or the
// CLI, and automation are all refused allowing an enrollment, and nothing is
// allowed, so a key they then try to register has no token to use. A
// person at the CLI allows it, and the console finishes it.
func TestAnAgentCannotEnrollAPasskeyOverTheSocket(t *testing.T) {
	sink := audit.NewMemory()
	broker, err := NewBroker(sink, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	SetBroker(broker)
	t.Cleanup(func() { SetBroker(nil) })
	SetPresence(presence.New(t.TempDir(), sink, presence.Options{Origins: func() []string { return []string{consoleOrigin} }}))
	t.Cleanup(func() { presencePoint.Store(nil) })
	path := startPeerSocket(t, NewInProcessClient(), nil)

	// The first-run repro: a raw socket call with no principal claim.
	raw := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	token, digest := presence.NewEnrollToken()
	resp, err := raw.Post("http://cerberus-daemon/approvals/keys/enroll-allow", "application/json", strings.NewReader(`{"digest":"`+digest+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionRequired || !strings.Contains(string(body), "approval_required") {
		t.Fatalf("a caller with no claim: %d %s", resp.StatusCode, body)
	}

	for name, who := range map[string]Principal{
		"agent over mcp":   {Kind: PrincipalAgent, Via: ViaMCPStdio},
		"agent at the cli": {Kind: PrincipalAgent, Via: ViaCLI},
		"automation":       {Kind: PrincipalAutomation, Via: "monitor"},
		"a person on mcp":  {Kind: PrincipalHuman, Via: ViaMCPStdio},
		"the console":      {Kind: PrincipalHuman, Via: ViaWeb},
	} {
		client := NewSocketClient(path, WithPrincipalClaim(func(context.Context) Principal { return who }))
		err = client.PasskeyAllowEnrollment(context.Background(), EnrollAllowArgs{Digest: digest})
		var coded *ExternalConnectorError
		if !errors.As(err, &coded) || coded.Code != ExternalConnectorApprovalRequired || !strings.Contains(err.Error(), "cerberus approvals enroll") {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := redact.Text(err.Error()); got != err.Error() {
			t.Errorf("%s: redaction rewrote the refusal: %q", name, got)
		}
	}
	// None of those allowed the token, so registering with it fails.
	human := NewSocketClient(path, WithPrincipalClaim(func(context.Context) Principal { return Principal{Kind: PrincipalHuman, Via: ViaWeb} }))
	if _, err = human.PasskeyEnrollBegin(context.Background(), EnrollBeginArgs{Token: token, Origin: consoleOrigin}); err == nil {
		t.Fatal("a token no person allowed began an enrollment")
	}
	agent := NewSocketClient(path, WithPrincipalClaim(func(context.Context) Principal { return Principal{Kind: PrincipalAgent, Via: ViaMCPStdio} }))

	// A person at the CLI allows it; an agent still cannot run the
	// ceremony with that token, the console can.
	cli := NewSocketClient(path, WithPrincipalClaim(func(context.Context) Principal { return Principal{Kind: PrincipalHuman, Via: ViaCLI} }))
	if err = cli.PasskeyAllowEnrollment(context.Background(), EnrollAllowArgs{Digest: digest}); err != nil {
		t.Fatalf("a person at the CLI: %v", err)
	}
	if _, err = agent.PasskeyEnrollBegin(context.Background(), EnrollBeginArgs{Token: token, Origin: consoleOrigin}); err == nil {
		t.Fatal("an agent began an enrollment with a person's token")
	}
	begin, err := human.PasskeyEnrollBegin(context.Background(), EnrollBeginArgs{Token: token, Origin: consoleOrigin})
	if err != nil {
		t.Fatalf("the console: %v", err)
	}
	key := presencetest.New(t, presence.RPID, consoleOrigin)
	if _, err = human.PasskeyEnrollFinish(context.Background(), EnrollFinishArgs{Ceremony: begin.Ceremony, Attestation: key.Register(t, optionsJSON(t, begin.Creation))}); err != nil {
		t.Fatalf("finishing on the console: %v", err)
	}
}
