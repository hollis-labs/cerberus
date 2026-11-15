package api

import (
	"encoding/json"
	"net/http"
	"testing"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate"
)

// GET /api/whoami answers each caller as the gate labels it, with the identity module's
// principal and scopes: the owner's console session is a person via web (the only caller who
// approves), a key an agent via api, local trust an agent via mcp_stdio. The posture is the
// baseline's with no policy store.
func TestWhoamiIsTheServersView(t *testing.T) {
	a, err := New(Options{Resources: func() ([]config.ResourceDef, error) { return nil, nil }, Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	g := &gatedAPI{h: a.Mux()}
	for _, c := range []struct {
		name    string
		p       libauth.Principal
		kind    gate.PrincipalKind
		via     string
		console bool
	}{
		{"owner", ownerSession, gate.PrincipalHuman, gate.ViaWeb, true},
		{"key", apiKey, gate.PrincipalAgent, ViaAPI, false},
		{"local trust", localTrust, gate.PrincipalAgent, gate.ViaMCPStdio, false},
	} {
		code, body := g.call(t, c.p, "GET", "/api/whoami", "")
		var got WhoAmI
		if err = json.Unmarshal([]byte(body), &got); err != nil || code != http.StatusOK || got.Principal.Kind != c.kind || got.Principal.Via != c.via ||
			got.Identity.Console != c.console || got.Identity.ID != c.p.ID || len(got.Identity.Scopes) != len(c.p.Scopes) || got.Posture.Global != "secure" {
			t.Errorf("%s: %d %s", c.name, code, body)
		}
	}
}
