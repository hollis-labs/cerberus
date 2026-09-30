package oauth

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/hollis-labs/cerberus/internal/audit"
)

var (
	jsonMarshal   = json.Marshal
	jsonUnmarshal = json.Unmarshal
)

// KeyStore holds the built-in issuer's signing key: the Keychain in the
// daemon, through the secrets writer, never a file.
type KeyStore interface {
	Get(ctx context.Context, service, key string) (string, error)
	Set(ctx context.Context, service, key, value string) error
}

const (
	keyService = "cerberus-oauth"
	keyName    = "issuer-ed25519"
)

// LoadOrCreateKey is the issuer's key, created on first use. Anything
// running as the operator's uid can read its own keychain item: the key
// raises the bar over a file, it is not a boundary against that uid.
func LoadOrCreateKey(ctx context.Context, ks KeyStore) (ed25519.PrivateKey, error) {
	if v, err := ks.Get(ctx, keyService, keyName); err == nil && v != "" {
		seed, derr := base64.StdEncoding.DecodeString(v)
		if derr != nil || len(seed) != ed25519.SeedSize {
			return nil, errors.New("the built-in issuer's key in the keychain is damaged; delete the cerberus-oauth/issuer-ed25519 item to mint a new one (every token issued with it stops working)")
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err = ks.Set(ctx, keyService, keyName, base64.StdEncoding.EncodeToString(priv.Seed())); err != nil {
		return nil, fmt.Errorf("store the built-in issuer's key: %w", err)
	}
	return priv, nil
}

// KeyID names a public key in the JWKS: a digest of it.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// Issuer mints the operator's tokens.
type Issuer struct {
	Config Config
	Key    ed25519.PrivateKey
	Store  TokenStore
	Now    func() time.Time
}

// JWKS is the issuer's public key set, for the verifier and mcp-http.
func (i Issuer) JWKS() JWKS {
	pub := i.Key.Public().(ed25519.PublicKey)
	return JWKS{Keys: []JWK{Ed25519JWK(pub, KeyID(pub))}}
}

func (i Issuer) now() time.Time {
	if i.Now != nil {
		return i.Now().UTC()
	}
	return time.Now().UTC()
}

// Issue mints a token for client with scopes, valid for ttl (capped), and
// records it. The token text is returned once and never stored.
func (i Issuer) Issue(client string, scopes []string, ttl time.Duration, by audit.Principal) (string, TokenRecord, error) {
	client = strings.TrimSpace(client)
	if client == "" || len(client) > 128 || strings.ContainsAny(client, "\n\r\t") {
		return "", TokenRecord{}, errors.New("name the client the token is for (--client), in at most 128 characters")
	}
	scopes, unknown := ParseScopes(scopes)
	if len(unknown) > 0 {
		return "", TokenRecord{}, fmt.Errorf("unknown scopes %s; the scopes are %s", strings.Join(unknown, ", "), strings.Join(Supported, ", "))
	}
	if len(scopes) == 0 {
		return "", TokenRecord{}, fmt.Errorf("name at least one scope (--scope): %s", strings.Join(Supported, ", "))
	}
	if ttl <= 0 {
		ttl = DefaultBuiltinTTL
	}
	if longest := i.Config.MaxLifetime(); ttl > longest {
		return "", TokenRecord{}, fmt.Errorf("a built-in token lives at most %s; ask for less", longest)
	}
	now := i.now()
	rec := TokenRecord{ID: newTokenID(), Client: client, Scopes: scopes, IssuedAt: now, ExpiresAt: now.Add(ttl), By: by}
	pub := i.Key.Public().(ed25519.PublicKey)
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"iss":       i.Config.BuiltinIssuer(),
		"sub":       "client:" + client,
		"aud":       i.Config.Resource,
		"client_id": client,
		"scope":     strings.Join(scopes, " "),
		"jti":       rec.ID,
		"iat":       now.Unix(),
		"nbf":       now.Unix(),
		"exp":       rec.ExpiresAt.Unix(),
	})
	tok.Header["kid"] = KeyID(pub)
	signed, err := tok.SignedString(i.Key)
	if err != nil {
		return "", TokenRecord{}, err
	}
	if err = i.Store.append(tokenEvent{Type: "issued", Token: &rec}); err != nil {
		return "", TokenRecord{}, err
	}
	return signed, rec, nil
}

func newTokenID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "tok_" + hex.EncodeToString(b[:])
}

// TokenRecord is an issued token, without its text.
type TokenRecord struct {
	ID        string          `json:"id"`
	Client    string          `json:"client"`
	Scopes    []string        `json:"scopes"`
	IssuedAt  time.Time       `json:"issued_at"`
	ExpiresAt time.Time       `json:"expires_at"`
	By        audit.Principal `json:"by"`
	RevokedAt *time.Time      `json:"revoked_at,omitempty"`
}

type tokenEvent struct {
	Type  string       `json:"type"`
	Token *TokenRecord `json:"token,omitempty"`
	ID    string       `json:"id,omitempty"`
	At    time.Time    `json:"at"`
}

// TokenStore is the record of issued and revoked built-in tokens,
// ~/.cerberus/oauth/tokens.jsonl: append-only, written under an exclusive
// lock so the daemon and an in-process CLI chain.
type TokenStore struct{ Dir string }

// FileName is the store in its directory.
const FileName = "tokens.jsonl"

// ErrUnknownToken is a revoke of a token id nobody issued.
var ErrUnknownToken = errors.New("no built-in token has that id; `cerberus mcp-http token list` shows them")

// List is every issued token, newest first.
func (s TokenStore) List() ([]TokenRecord, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, FileName)) //nolint:gosec // the store's own file
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	byID := map[string]*TokenRecord{}
	var order []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		var ev tokenEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "issued":
			if ev.Token != nil && byID[ev.Token.ID] == nil {
				t := *ev.Token
				byID[t.ID] = &t
				order = append(order, t.ID)
			}
		case "revoked":
			if t := byID[ev.ID]; t != nil && t.RevokedAt == nil {
				at := ev.At
				t.RevokedAt = &at
			}
		}
	}
	out := make([]TokenRecord, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- {
		out = append(out, *byID[order[i]])
	}
	return out, nil
}

// Revoked reports whether a built-in token id is revoked, or was never
// issued here, which a verifier reads the same way.
func (s TokenStore) Revoked(id string) bool {
	list, err := s.List()
	if err != nil {
		return true
	}
	for _, t := range list {
		if t.ID == id {
			return t.RevokedAt != nil
		}
	}
	return true
}

// Revoke revokes a token by id.
func (s TokenStore) Revoke(id string) (TokenRecord, error) {
	list, err := s.List()
	if err != nil {
		return TokenRecord{}, err
	}
	for _, t := range list {
		if t.ID == id {
			if t.RevokedAt != nil {
				return t, nil
			}
			now := time.Now().UTC()
			if err = s.append(tokenEvent{Type: "revoked", ID: id, At: now}); err != nil {
				return t, err
			}
			t.RevokedAt = &now
			return t, nil
		}
	}
	return TokenRecord{}, ErrUnknownToken
}

func (s TokenStore) append(ev tokenEvent) error {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, FileName), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) //nolint:gosec // the store's own file
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // a file descriptor fits an int
		return err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }() //nolint:gosec // as above
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(line, '\n')); err != nil {
		return err
	}
	return f.Sync()
}
