package api

import (
	"net/http"

	libauth "github.com/hollis-labs/libs/security/auth"

	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/policy"
)

// WhoAmI is GET /api/whoami (slice 7d): the old socket's GET /whoami, which answered the
// principal the daemon gave the request, with what this server knows beside it. Principal is the
// gate's view of the caller (GatePrincipal, and an MCP tool call's mark), the label policy and
// the records see; Identity is what the identity module verified (libauth.Principal: who, which
// kind of credential, its scopes); Posture is the applied posture the caller's calls are
// evaluated under (old's whoami showed it too).
type WhoAmI struct {
	Principal gate.Principal        `json:"principal"`
	Identity  WhoAmIIdentity        `json:"identity"`
	Posture   policy.PostureSummary `json:"posture"`
}

// WhoAmIIdentity is the verified credential: its principal id ("key:<id>", "user:<id>",
// "local:operator"), kind, display name (a key's name), scopes, and for a session or a minted
// token its session, token and parent. Never a secret.
type WhoAmIIdentity struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Display  string   `json:"display,omitempty"`
	Verified bool     `json:"verified"`
	Scopes   []string `json:"scopes"`
	Session  string   `json:"session,omitempty"`
	TokenID  string   `json:"token_id,omitempty"`
	Parent   string   `json:"parent,omitempty"`
	// Console is api.IsConsolePerson: the owner signed in to the console, the only caller who
	// approves (decision 0.1).
	Console bool `json:"console"`
}

// whoami answers the caller as this server sees it. It needs cerberus.read, like old's.
func (a *API) whoami(w http.ResponseWriter, r *http.Request) {
	p, ok := libauth.FromContext(r.Context())
	who, _ := gate.PrincipalFrom(GateContext(r.Context()))
	out := WhoAmI{Principal: who, Posture: a.posture()}
	if ok {
		out.Identity = WhoAmIIdentity{ID: p.ID, Kind: p.Kind, Display: p.Display, Verified: p.Verified,
			Scopes: append([]string{}, p.Scopes...), Session: p.SessionID, TokenID: p.TokenID, Parent: p.Parent, Console: IsConsolePerson(p)}
	}
	if out.Identity.Scopes == nil {
		out.Identity.Scopes = []string{}
	}
	writeJSON(w, out)
}

// posture is the applied posture as old's currentPosture read it: the hash-checked snapshot,
// secure when nothing is applied or the snapshot fails its check (the baseline when this server
// has no policy store).
func (a *API) posture() policy.PostureSummary {
	if a.o.Policy == nil {
		return policy.PostureSummary{Global: policy.PostureSecure, Snapshot: policy.SnapshotBaseline}
	}
	return a.o.Policy.Store.CurrentPosture()
}
