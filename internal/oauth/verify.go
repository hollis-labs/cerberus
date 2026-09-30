package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Identity is what a verified token says about its caller. It carries no
// token text.
type Identity struct {
	Issuer  string    `json:"issuer"`
	Subject string    `json:"subject"`
	Client  string    `json:"client,omitempty"`
	TokenID string    `json:"token_id,omitempty"`
	Scopes  []string  `json:"scopes"`
	Expiry  time.Time `json:"expiry"`
}

// ErrInvalidToken is a token that did not verify. Its message names why,
// never the token.
var ErrInvalidToken = errors.New("the bearer token did not verify")

// Verifier checks tokens for one resource: the signature against the
// issuer's key, the issuer against the configured ones, the audience
// against the resource (RFC 8707), expiry, and revocation for built-in
// tokens.
type Verifier struct {
	cfg     Config
	sources map[string]keySource
	issuers map[string]IssuerConfig
	revoked func(jti string) bool
	now     func() time.Time
}

// VerifierOptions are a verifier's key sources beyond the config.
type VerifierOptions struct {
	// Builtin is the built-in issuer's key set, when cfg.Builtin.
	Builtin *JWKS
	// Revoked reports a revoked built-in token id.
	Revoked func(jti string) bool
	HTTP    *http.Client
	Now     func() time.Time
}

// allowedAlgs are the only signatures accepted: never none, never HMAC,
// which would make the verifier's key a signing key.
var allowedAlgs = []string{"EdDSA", "ES256", "RS256"}

// NewVerifier builds a verifier from cfg.
func NewVerifier(cfg Config, o VerifierOptions) (*Verifier, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if o.Revoked == nil {
		o.Revoked = func(string) bool { return false }
	}
	v := &Verifier{cfg: cfg, sources: map[string]keySource{}, issuers: map[string]IssuerConfig{}, revoked: o.Revoked, now: o.Now}
	if cfg.Builtin {
		if o.Builtin == nil || len(o.Builtin.Keys) == 0 {
			return nil, errors.New("the built-in issuer is configured but has no key")
		}
		keys := staticKeys{}
		for _, k := range o.Builtin.Keys {
			pub, err := k.PublicKey()
			if err != nil {
				return nil, err
			}
			keys[k.Kid] = pub
		}
		v.sources[cfg.BuiltinIssuer()] = keys
	}
	for _, is := range cfg.Issuers {
		id := strings.TrimSuffix(is.Issuer, "/")
		v.issuers[id] = is
		v.sources[id] = &remoteKeys{issuer: id, uri: is.JWKSURI, client: o.HTTP, now: o.Now}
	}
	return v, nil
}

// claims are the token claims the verifier reads.
type claims struct {
	jwt.RegisteredClaims
	ClientID string `json:"client_id,omitempty"`
	AZP      string `json:"azp,omitempty"`
	Scope    string `json:"scope,omitempty"`
	SCP      any    `json:"scp,omitempty"`
}

// Verify checks raw and returns its caller's identity.
func (v *Verifier) Verify(ctx context.Context, raw string) (Identity, error) {
	fail := func(format string, args ...any) (Identity, error) {
		return Identity{}, fmt.Errorf("%w: %s", ErrInvalidToken, fmt.Sprintf(format, args...))
	}
	var unverified jwt.MapClaims
	if _, _, err := jwt.NewParser().ParseUnverified(raw, &unverified); err != nil {
		return fail("it is not a JWT")
	}
	iss, _ := unverified["iss"].(string)
	iss = strings.TrimSuffix(iss, "/")
	src, ok := v.sources[iss]
	if !ok {
		return fail("its issuer %q is not one this resource trusts", iss)
	}
	var c claims
	var mapped jwt.MapClaims
	parser := jwt.NewParser(jwt.WithValidMethods(allowedAlgs), jwt.WithExpirationRequired(), jwt.WithLeeway(60*time.Second),
		jwt.WithIssuedAt(), jwt.WithTimeFunc(v.now))
	tok, err := parser.ParseWithClaims(raw, &mapped, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return src.key(ctx, kid)
	})
	if err != nil || !tok.Valid {
		return fail("%v", err)
	}
	if err = remarshal(mapped, &c); err != nil {
		return fail("its claims do not parse")
	}
	audience := v.cfg.Resource
	is, external := v.issuers[iss]
	if external && is.Audience != "" {
		audience = is.Audience
	}
	if !oauthex.MatchesResource(c.Audience, audience) {
		return fail("its audience %v is not this resource (%s)", []string(c.Audience), audience)
	}
	if c.Subject == "" {
		return fail("it names no subject")
	}
	if !external && (c.ID == "" || v.revoked(c.ID)) {
		return fail("it has been revoked, or carries no id")
	}
	id := Identity{Issuer: iss, Subject: c.Subject, TokenID: c.ID, Expiry: c.ExpiresAt.Time}
	id.Client = firstNonEmpty(claimString(mapped, is.ClientClaim), c.ClientID, c.AZP)
	var rawScopes []string
	switch {
	case external && is.ScopesClaim != "":
		rawScopes = claimStrings(mapped[is.ScopesClaim])
	case c.Scope != "":
		rawScopes = strings.Fields(c.Scope)
	default:
		rawScopes = claimStrings(c.SCP)
	}
	id.Scopes, _ = ParseScopes(rawScopes)
	if len(id.Scopes) == 0 {
		return fail("it carries none of this resource's scopes (%s)", strings.Join(Supported, ", "))
	}
	return id, nil
}

func remarshal(in jwt.MapClaims, out *claims) error {
	data, err := jsonMarshal(in)
	if err != nil {
		return err
	}
	return jsonUnmarshal(data, out)
}

func claimString(m jwt.MapClaims, name string) string {
	if name == "" {
		return ""
	}
	s, _ := m[name].(string)
	return s
}

func claimStrings(v any) []string {
	switch t := v.(type) {
	case string:
		return strings.Fields(t)
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
