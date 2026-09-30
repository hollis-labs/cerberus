package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/oauth"
)

// fakeToken stands in for a minted token.
const fakeToken = "eyJ.test.token" //nolint:gosec // not a credential

type fakeTokens struct {
	issued  []cerbapi.TokenIssueArgs
	revoked []string
	err     error
}

func (f *fakeTokens) IssueToken(_ context.Context, args cerbapi.TokenIssueArgs) (cerbapi.IssuedToken, error) {
	f.issued = append(f.issued, args)
	if f.err != nil {
		return cerbapi.IssuedToken{}, f.err
	}
	return cerbapi.IssuedToken{Token: fakeToken, Record: oauth.TokenRecord{ID: "tok_1", Client: args.Client, Scopes: args.Scopes, ExpiresAt: time.Now().Add(args.TTL)}}, nil
}

func (f *fakeTokens) ListTokens(context.Context) ([]oauth.TokenRecord, error) {
	return []oauth.TokenRecord{{ID: "tok_1", Client: "bot", Scopes: []string{"cerberus:read"}, ExpiresAt: time.Now().Add(time.Hour)}}, f.err
}

func (f *fakeTokens) RevokeToken(_ context.Context, id string, _ cerbapi.TokenRevokeArgs) (oauth.TokenRecord, error) {
	f.revoked = append(f.revoked, id)
	now := time.Now()
	return oauth.TokenRecord{ID: id, Client: "bot", RevokedAt: &now}, f.err
}

func tokensFixture(t *testing.T, terminal bool) *fakeTokens {
	t.Helper()
	f := &fakeTokens{}
	oldTerm, oldClient := policyIsTerminal, newTokenClient
	policyIsTerminal = func() bool { return terminal }
	newTokenClient = func() (tokenClient, error) { return f, nil }
	t.Cleanup(func() {
		policyIsTerminal, newTokenClient = oldTerm, oldClient
		mcpHTTPTokenFlags.client, mcpHTTPTokenFlags.scopes, mcpHTTPTokenFlags.ttl = "", nil, "7d"
	})
	return f
}

func runTokenCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	// Flag values outlive an Execute; start each run from the defaults.
	mcpHTTPTokenFlags.client, mcpHTTPTokenFlags.scopes, mcpHTTPTokenFlags.ttl = "", nil, "7d"
	var out bytes.Buffer
	rootCmd.SetArgs(append([]string{"mcp-http", "token"}, args...))
	rootCmd.SetOut(&out)
	rootCmd.SetIn(strings.NewReader(stdin))
	t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil); rootCmd.SetIn(nil) })
	err := rootCmd.Execute()
	return out.String(), err
}

// Minting is on a terminal, with the phrase, which travels to the daemon;
// the token is printed once with how to use it.
func TestMCPHTTPTokenIssue(t *testing.T) {
	f := tokensFixture(t, false)
	if _, err := runTokenCmd(t, "issue bot\n", "issue", "--client", "bot", "--scope", "cerberus:read"); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("off a terminal: %v", err)
	}
	policyIsTerminal = func() bool { return true }
	if _, err := runTokenCmd(t, "yes\n", "issue", "--client", "bot", "--scope", "cerberus:read"); err == nil || len(f.issued) != 0 {
		t.Fatalf("a wrong phrase: %v", err)
	}
	if _, err := runTokenCmd(t, "issue bot\n", "issue", "--client", "bot", "--scope", "cerberus:admin"); err == nil {
		t.Fatal("an unknown scope")
	}
	out, err := runTokenCmd(t, "issue bot\n", "issue", "--client", "bot", "--scope", "cerberus:read,cerberus:operate", "--ttl", "2d")
	if err != nil || len(f.issued) != 1 || f.issued[0].Typed != "issue bot" || f.issued[0].TTL != 48*time.Hour ||
		strings.Join(f.issued[0].Scopes, " ") != "cerberus:read cerberus:operate" {
		t.Fatalf("issue: %v %+v", err, f.issued)
	}
	for _, want := range []string{"eyJ.test.token", "shown once", `"Authorization": "Bearer <the token above>"`, "cerberus mcp-http token revoke tok_1"} {
		if !strings.Contains(out, want) {
			t.Errorf("issue output lacks %q:\n%s", want, out)
		}
	}
}

func TestMCPHTTPTokenListAndRevoke(t *testing.T) {
	f := tokensFixture(t, true)
	out, err := runTokenCmd(t, "", "list")
	if err != nil || !strings.Contains(out, "tok_1  bot  [cerberus:read]") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if _, err = runTokenCmd(t, "revoke tok_1\n", "revoke", "tok_1"); err != nil || len(f.revoked) != 1 {
		t.Fatalf("revoke: %v", err)
	}
}

// With the daemon down, minting runs in-process against the same store.
func TestMCPHTTPTokenIssueWithTheDaemonDown(t *testing.T) {
	f := tokensFixture(t, true)
	f.err = &cerbapi.DaemonUnreachableError{Path: "/nowhere", Err: errors.New("dial")}
	cfg := oauth.Config{Resource: loopResource, Builtin: true}
	iss, _ := withAuthConfig(t, cfg)
	oldAuth, oldDetect := inProcessAuth, detectCLI
	inProcessAuth = func(context.Context) (*cerbapi.Auth, error) {
		return &cerbapi.Auth{Config: cfg, Issuer: iss, Sink: audit.NewMemory()}, nil
	}
	detectCLI = func() cerbapi.CLIClassification { return cerbapi.ClassifyCLI(true, true, "") }
	t.Cleanup(func() { inProcessAuth, detectCLI = oldAuth, oldDetect })
	out, err := runTokenCmd(t, "issue bot\n", "issue", "--client", "bot", "--scope", "cerberus:read")
	if err != nil || !strings.Contains(out, "Minted tok_") {
		t.Fatalf("in-process issue: %v\n%s", err, out)
	}
	if list, _ := iss.Store.List(); len(list) != 1 {
		t.Fatal("the store holds no token")
	}
}

func TestParseTTL(t *testing.T) {
	for in, want := range map[string]time.Duration{"": oauth.DefaultBuiltinTTL, "7d": 7 * 24 * time.Hour, "12h": 12 * time.Hour} {
		if got, err := parseTTL(in); err != nil || got != want {
			t.Errorf("%q: %s %v", in, got, err)
		}
	}
	for _, bad := range []string{"0d", "-1h", "soon"} {
		if _, err := parseTTL(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
