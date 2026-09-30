package oauth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// JWK is one JSON Web Key, as far as verification needs it.
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid,omitempty"`
	Use string `json:"use,omitempty"`
	Alg string `json:"alg,omitempty"`
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
	N   string `json:"n,omitempty"`
	E   string `json:"e,omitempty"`
}

// JWKS is a key set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// PublicKey is the key a JWK describes: RSA, EC P-256 or Ed25519.
func (k JWK) PublicKey() (crypto.PublicKey, error) {
	b := func(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "=")) }
	switch k.Kty {
	case "RSA":
		n, err := b(k.N)
		if err != nil {
			return nil, err
		}
		e, err := b(k.E)
		if err != nil {
			return nil, err
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if pub.N.BitLen() < 2048 {
			return nil, errors.New("RSA key shorter than 2048 bits")
		}
		return pub, nil
	case "EC":
		if k.Crv != "P-256" {
			return nil, fmt.Errorf("EC curve %q is not P-256", k.Crv)
		}
		x, err := b(k.X)
		if err != nil {
			return nil, err
		}
		y, err := b(k.Y)
		if err != nil {
			return nil, err
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !pub.Curve.IsOnCurve(pub.X, pub.Y) { //nolint:staticcheck // the point check a JWK needs
			return nil, errors.New("EC point is not on P-256")
		}
		return pub, nil
	case "OKP":
		if k.Crv != "Ed25519" {
			return nil, fmt.Errorf("OKP curve %q is not Ed25519", k.Crv)
		}
		x, err := b(k.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			return nil, errors.New("bad Ed25519 key")
		}
		return ed25519.PublicKey(x), nil
	}
	return nil, fmt.Errorf("key type %q is not supported", k.Kty)
}

// Ed25519JWK is the JWK of an Ed25519 public key.
func Ed25519JWK(pub ed25519.PublicKey, kid string) JWK {
	return JWK{Kty: "OKP", Crv: "Ed25519", X: base64.RawURLEncoding.EncodeToString(pub), Kid: kid, Use: "sig", Alg: "EdDSA"}
}

// keySource finds an issuer's verification key by kid.
type keySource interface {
	key(ctx context.Context, kid string) (crypto.PublicKey, error)
}

// staticKeys is a fixed key set (the built-in issuer's).
type staticKeys map[string]crypto.PublicKey

func (s staticKeys) key(_ context.Context, kid string) (crypto.PublicKey, error) {
	if k, ok := s[kid]; ok {
		return k, nil
	}
	if len(s) == 1 && kid == "" {
		for _, k := range s {
			return k, nil
		}
	}
	return nil, fmt.Errorf("no key %q", kid)
}

// remoteKeys is an external issuer's JWKS: discovered from its metadata
// when no jwks_uri is configured, cached, and fetched again for an unknown
// kid at most once a minute, so a stranger's kid cannot make the daemon
// hammer the issuer.
type remoteKeys struct {
	issuer, uri string
	client      *http.Client
	now         func() time.Time

	mu      sync.Mutex
	keys    map[string]crypto.PublicKey
	fetched time.Time
	tried   time.Time
}

const (
	jwksTTL        = 10 * time.Minute
	jwksMinRefetch = time.Minute
	maxMetadata    = 1 << 20
)

func (r *remoteKeys) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if r.keys == nil || now.Sub(r.fetched) > jwksTTL {
		_ = r.refresh(ctx, now)
	}
	if k, ok := r.keys[kid]; ok {
		return k, nil
	}
	if now.Sub(r.tried) >= jwksMinRefetch {
		if err := r.refresh(ctx, now); err != nil {
			return nil, err
		}
		if k, ok := r.keys[kid]; ok {
			return k, nil
		}
	}
	return nil, fmt.Errorf("issuer %s has no key %q", r.issuer, kid)
}

func (r *remoteKeys) refresh(ctx context.Context, now time.Time) error {
	r.tried = now
	uri := r.uri
	if uri == "" {
		var err error
		if uri, err = discoverJWKS(ctx, r.client, r.issuer); err != nil {
			return err
		}
	}
	var set JWKS
	if err := getJSON(ctx, r.client, uri, &set); err != nil {
		return fmt.Errorf("fetch the key set of %s: %w", r.issuer, err)
	}
	keys := map[string]crypto.PublicKey{}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		if pub, err := k.PublicKey(); err == nil {
			keys[k.Kid] = pub
		}
	}
	r.keys, r.fetched = keys, now
	return nil
}

// discoverJWKS reads jwks_uri from the issuer's RFC 8414 metadata, or its
// OpenID configuration, and checks the metadata names the same issuer.
func discoverJWKS(ctx context.Context, c *http.Client, issuer string) (string, error) {
	base := strings.TrimSuffix(issuer, "/")
	var lastErr error
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/openid-configuration"} {
		var meta struct {
			Issuer  string `json:"issuer"`
			JWKSURI string `json:"jwks_uri"`
		}
		if err := getJSON(ctx, c, base+path, &meta); err != nil {
			lastErr = err
			continue
		}
		if strings.TrimSuffix(meta.Issuer, "/") != base {
			return "", fmt.Errorf("the metadata at %s names issuer %q, not %s", base+path, meta.Issuer, issuer)
		}
		if !strings.HasPrefix(meta.JWKSURI, "https://") {
			return "", fmt.Errorf("issuer %s publishes no https jwks_uri", issuer)
		}
		return meta.JWKSURI, nil
	}
	return "", fmt.Errorf("discover the key set of %s: %w", issuer, lastErr)
}

func getJSON(ctx context.Context, c *http.Client, uri string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req) //nolint:gosec // an operator-configured issuer URL
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", uri, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxMetadata)).Decode(out)
}
