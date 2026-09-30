package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/oauth"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// mcp-http as an OAuth 2.1 resource server (WP-S8). mcp-http checks a
// bearer token for the protocol's sake and forwards it; the daemon checks
// it again here and only then gives the call a verified principal, so a
// process running as the operator's uid cannot mint one without a token
// that verifies. A verified caller is an agent, whatever the token says,
// and its scopes narrow what it may ask for before policy decides.

// BearerHeader carries mcp-http's caller's bearer token over the socket.
// The token is a credential: it is never logged or recorded.
const BearerHeader = "X-Cerberus-Bearer"

// TokenVerifier verifies a bearer token for this resource.
type TokenVerifier interface {
	Verify(ctx context.Context, raw string) (oauth.Identity, error)
}

// Auth is the daemon's mcp-http auth: its config, the verifier, and the
// built-in issuer when the config enables it.
type Auth struct {
	Config   oauth.Config
	Verifier TokenVerifier
	Issuer   *oauth.Issuer
	// Sink records minting and revoking.
	Sink audit.Sink
}

var authPoint atomic.Pointer[Auth]

// SetAuth installs the daemon's auth; nil removes it.
func SetAuth(a *Auth) { authPoint.Store(a) }

// ProcessAuth is the installed auth, or nil.
func ProcessAuth() *Auth { return authPoint.Load() }

// verifyBearer gives a socket request carrying a bearer token its verified
// principal, or refuses it. A token that does not verify is never read as
// a lesser claim: the caller sent a credential, and it failed.
func verifyBearer(r *http.Request) (*http.Request, error) {
	raw := r.Header.Get(BearerHeader)
	if raw == "" {
		return r, nil
	}
	ctx := r.Context()
	// The token joins the request's redaction scope before anything can
	// render it.
	redact.ScopeFrom(ctx).Add("bearer token", raw)
	a := ProcessAuth()
	if a == nil || a.Verifier == nil {
		return r, errors.New("this daemon has no mcp-http auth configured, so it cannot verify the caller's token; configure " + oauth.ConfigFilename + " and restart the daemon")
	}
	id, err := a.Verifier.Verify(ctx, raw)
	if err != nil {
		return r, err
	}
	p, _ := PrincipalFrom(ctx)
	// An OAuth caller is an agent over mcp-http, whatever it claimed.
	p.Kind, p.Via, p.SelfReported = PrincipalAgent, ViaMCPHTTP, false
	p.Subject, p.Issuer, p.AuthMethod, p.TokenID, p.Scopes = id.Subject, id.Issuer, AuthOAuth, id.TokenID, id.Scopes
	// The client is the token's, or none: never the caller's own claim,
	// which it could vary per call (M13).
	p.Client = clip(id.Client)
	return r.WithContext(WithPrincipal(ctx, p)), nil
}

// scopeRefusal refuses a verified caller's call its scopes do not cover.
// It runs before the brakes and policy, in every mode: a scope is what the
// operator granted the token, not a decision to shadow.
func scopeRefusal(ctx context.Context, spec auditSpec) error {
	p, ok := PrincipalFrom(ctx)
	if !ok || !p.Verified() || spec.automation {
		return nil
	}
	// A read that touches local files needs operate, like a write (H2).
	effect := spec.op.PolicyEffect()
	if !spec.known || effect == "" {
		effect = contract.EffectExec
	}
	if oauth.Allows(p.Scopes, effect) {
		return nil
	}
	return insufficientScope(spec.connector, spec.operation, effect, p.Scopes, p.Client)
}

// insufficientScope is the refusal an agent reads: the text says what the
// token lacks and what to ask for, because a client may show the model
// only the text of a result.
func insufficientScope(connector, operation string, effect contract.Effect, scopes []string, client string) error {
	need := oauth.RequiredScope(effect)
	if client == "" {
		client = "<client>"
	}
	return externalConnectorError(ExternalConnectorOperationArgs{Connector: connector, Operation: operation}, ExternalConnectorInsufficientScope,
		redact.Guidance("this token's scopes (%s) do not cover %s %s, a %s operation, which needs %s. You cannot widen them yourself: ask your operator for a token with %s (`cerberus mcp-http token issue --client %s --scope %s` on their terminal)",
			strings.Join(scopes, " "), connector, operation, effect, need, need, client, need))
}

// AuthCapabilities is what the daemon says about its mcp-http auth, so
// mcp-http refuses to require auth from a daemon that would not verify it
// (an older one answers 404).
type AuthCapabilities struct {
	OAuth           bool        `json:"oauth"`
	Resource        string      `json:"resource,omitempty"`
	Builtin         bool        `json:"builtin"`
	BuiltinIssuer   string      `json:"builtin_issuer,omitempty"`
	BuiltinJWKS     *oauth.JWKS `json:"builtin_jwks,omitempty"`
	Issuers         []string    `json:"issuers,omitempty"`
	ScopesSupported []string    `json:"scopes_supported"`
}

func authCapabilities() AuthCapabilities {
	out := AuthCapabilities{ScopesSupported: oauth.Supported}
	a := ProcessAuth()
	if a == nil {
		return out
	}
	out.OAuth, out.Resource, out.Builtin = true, a.Config.Resource, a.Config.Builtin
	if a.Issuer != nil {
		set := a.Issuer.JWKS()
		out.BuiltinIssuer, out.BuiltinJWKS = a.Config.BuiltinIssuer(), &set
	}
	for _, is := range a.Config.Issuers {
		out.Issuers = append(out.Issuers, strings.TrimSuffix(is.Issuer, "/"))
	}
	return out
}

// TokenIssueArgs mint a built-in token; Typed is the confirmation the
// operator typed, "issue <client>", checked here.
type TokenIssueArgs struct {
	Client string        `json:"client"`
	Scopes []string      `json:"scopes"`
	TTL    time.Duration `json:"ttl"`
	Typed  string        `json:"typed"`
}

// TokenRevokeArgs revoke one; Typed is "revoke <id>".
type TokenRevokeArgs struct {
	Typed string `json:"typed"`
}

// IssuedToken is a minted token, shown once.
type IssuedToken struct {
	Token  string            `json:"token"`
	Record oauth.TokenRecord `json:"record"`
}

// tokenAdmin refuses anyone but a person at the CLI: minting and revoking
// tokens is admin, and an agent holding a token must not mint another.
func tokenAdmin(ctx context.Context, op string) error {
	p, _ := PrincipalFrom(ctx)
	if p.Kind != PrincipalHuman || p.Via != ViaCLI || strings.HasPrefix(p.Via, "mcp") {
		return externalConnectorError(ExternalConnectorOperationArgs{Connector: "mcp-http", Operation: op}, ExternalConnectorApprovalRequired,
			redact.Guidance("mcp-http tokens are minted and revoked by a person at an interactive terminal (`cerberus mcp-http token %s`), and this caller is %s over %s", strings.TrimPrefix(op, "token_"), p.Kind, p.Via))
	}
	return nil
}

var errNoBuiltinIssuer = errors.New("the built-in issuer is not enabled; set `builtin: true` in " + oauth.ConfigFilename + " and restart the daemon")

// IssueToken mints a built-in token, recorded as an admin operation.
func IssueToken(ctx context.Context, a *Auth, args TokenIssueArgs) (IssuedToken, error) {
	if err := tokenAdmin(ctx, "token_issue"); err != nil {
		return IssuedToken{}, err
	}
	if a == nil || a.Issuer == nil {
		return IssuedToken{}, errNoBuiltinIssuer
	}
	if strings.TrimSpace(args.Typed) != "issue "+strings.TrimSpace(args.Client) {
		return IssuedToken{}, externalConnectorError(ExternalConnectorOperationArgs{Connector: "mcp-http", Operation: "token_issue"}, ExternalConnectorApprovalRequired,
			redact.Guidance("type %q to mint it; nothing was minted", "issue "+strings.TrimSpace(args.Client)))
	}
	call, err := beginAudit(ctx, a.Sink, slog.Default(), tokenSpec("token_issue", map[string]any{"client": args.Client, "scopes": args.Scopes, "ttl": args.TTL.String()}))
	if err != nil {
		return IssuedToken{}, err
	}
	token, rec, err := a.Issuer.Issue(args.Client, args.Scopes, args.TTL, principalFor(ctx, auditSpec{}))
	call.finish(err)
	if err != nil {
		return IssuedToken{}, err
	}
	return IssuedToken{Token: token, Record: rec}, nil
}

// RevokeToken revokes a built-in token, recorded as an admin operation.
func RevokeToken(ctx context.Context, a *Auth, id string, args TokenRevokeArgs) (oauth.TokenRecord, error) {
	if err := tokenAdmin(ctx, "token_revoke"); err != nil {
		return oauth.TokenRecord{}, err
	}
	if a == nil || a.Issuer == nil {
		return oauth.TokenRecord{}, errNoBuiltinIssuer
	}
	if strings.TrimSpace(args.Typed) != "revoke "+id {
		return oauth.TokenRecord{}, externalConnectorError(ExternalConnectorOperationArgs{Connector: "mcp-http", Operation: "token_revoke"}, ExternalConnectorApprovalRequired,
			redact.Guidance("type %q to revoke it; nothing was revoked", "revoke "+id))
	}
	call, err := beginAudit(ctx, a.Sink, slog.Default(), tokenSpec("token_revoke", map[string]any{"id": id}))
	if err != nil {
		return oauth.TokenRecord{}, err
	}
	rec, err := a.Issuer.Store.Revoke(id)
	call.finish(err)
	return rec, err
}

func tokenSpec(op string, cfg map[string]any) auditSpec {
	o := contract.Operation{Name: op, Effect: contract.EffectAdmin, RequiresAck: true,
		Target:  contract.TargetDescriptor{Kind: "mcp-http.token", From: []string{"client", "id"}},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone}.Finalize()
	return auditSpec{connector: "mcp-http", operation: op, op: o, known: true, acknowledged: true, config: cfg}
}

// handleAuth is GET /auth/capabilities, GET /auth/tokens, POST
// /auth/tokens and POST /auth/tokens/{id}/revoke.
func (s *SocketServer) handleAuth(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/auth"), "/")
	a := ProcessAuth()
	switch {
	case rest == "capabilities" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, authCapabilities())
	case rest == "tokens" && r.Method == http.MethodGet:
		if a == nil || a.Issuer == nil {
			writeJSONError(w, http.StatusConflict, errNoBuiltinIssuer.Error())
			return
		}
		list, err := a.Issuer.Store.List()
		if err != nil {
			writeServiceError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tokens": list})
	case rest == "tokens" && r.Method == http.MethodPost:
		var args TokenIssueArgs
		if err := decodeJSONBody(r, &args); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := IssueToken(r.Context(), a, args)
		if err != nil {
			writeAuthError(w, err)
			return
		}
		// The one response that carries a token: to the operator's terminal.
		w.Header().Set("Cache-Control", "no-store")
		data, _ := json.Marshal(out)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	case strings.HasPrefix(rest, "tokens/") && strings.HasSuffix(rest, "/revoke") && r.Method == http.MethodPost:
		var args TokenRevokeArgs
		if err := decodeJSONBody(r, &args); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		id, _ := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(rest, "tokens/"), "/revoke"))
		rec, err := RevokeToken(r.Context(), a, id, args)
		if err != nil {
			writeAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rec)
	default:
		writeJSONError(w, http.StatusNotFound, "expected GET /auth/capabilities, GET|POST /auth/tokens or POST /auth/tokens/{id}/revoke")
	}
}

func writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, oauth.ErrUnknownToken):
		writeJSONError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errNoBuiltinIssuer):
		writeJSONError(w, http.StatusConflict, err.Error())
	default:
		writeServiceError(w, http.StatusBadRequest, err)
	}
}

// AuthCapabilities asks the daemon about its mcp-http auth. An older
// daemon answers 404, which the caller reads as no auth.
func (c *SocketClient) AuthCapabilities(ctx context.Context) (AuthCapabilities, error) {
	var out AuthCapabilities
	err := c.doJSON(ctx, http.MethodGet, "/auth/capabilities", nil, &out)
	return out, err
}

// IssueToken mints a built-in token through the daemon.
func (c *SocketClient) IssueToken(ctx context.Context, args TokenIssueArgs) (IssuedToken, error) {
	var out IssuedToken
	err := c.doJSON(ctx, http.MethodPost, "/auth/tokens", args, &out)
	return out, err
}

// ListTokens lists the built-in tokens, without their text.
func (c *SocketClient) ListTokens(ctx context.Context) ([]oauth.TokenRecord, error) {
	var out struct {
		Tokens []oauth.TokenRecord `json:"tokens"`
	}
	err := c.doJSON(ctx, http.MethodGet, "/auth/tokens", nil, &out)
	return out.Tokens, err
}

// RevokeToken revokes a built-in token through the daemon.
func (c *SocketClient) RevokeToken(ctx context.Context, id string, args TokenRevokeArgs) (oauth.TokenRecord, error) {
	var out oauth.TokenRecord
	err := c.doJSON(ctx, http.MethodPost, "/auth/tokens/"+url.PathEscape(id)+"/revoke", args, &out)
	return out, err
}

// describeToken is a token's line for a terminal.
func describeToken(t oauth.TokenRecord) string {
	state := "active"
	switch {
	case t.RevokedAt != nil:
		state = "revoked " + t.RevokedAt.Local().Format(time.RFC3339)
	case time.Now().After(t.ExpiresAt):
		state = "expired"
	}
	return fmt.Sprintf("%s  %s  [%s]  expires %s  %s", t.ID, t.Client, strings.Join(t.Scopes, " "), t.ExpiresAt.Local().Format(time.RFC3339), state)
}

// DescribeToken is describeToken for the CLI.
func DescribeToken(t oauth.TokenRecord) string { return describeToken(t) }
