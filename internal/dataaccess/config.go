package dataaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	auth "github.com/hollis-labs/libs/security/auth"
)

// ConfigFileName is the operator's opt-in file, in the config directory.
const ConfigFileName = "data-access.json"

// Open reads the operator's data-access.json at path and returns the service it configures, or
// nil when the file is absent: data access is opt-in, and loading it provisions no grant,
// listener or database credential (the old build's app.DataService). The caller binds its
// Authorize, Execute and Audit before serving it. The revocation poller and the JWKS refresher
// run until ctx ends.
func Open(ctx context.Context, path string) (*Service, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the operator's fixed file in the config dir
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm()&0o022 != 0 || len(b) > 65536 {
		return nil, errors.New("data access configuration is not a bounded operator-owned file")
	}
	var cfg struct {
		Issuer   string `json:"issuer"`
		JWKS     string `json:"jwks_url"`
		DenyList string `json:"denylist_url"`
		Grants   string `json:"grants_file"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&cfg) != nil || dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid data access configuration")
	}
	issuer, e := url.Parse(cfg.Issuer)
	if e != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return nil, errors.New("data issuer requires HTTPS")
	}
	for _, s := range []string{cfg.JWKS, cfg.DenyList} {
		u, e := url.Parse(s)
		if e != nil || u.Scheme != "https" || u.Host != issuer.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("data authorization endpoints must use the issuer's HTTPS origin")
		}
	}
	if !filepath.IsAbs(cfg.Grants) {
		return nil, errors.New("data grants_file requires an absolute private path")
	}
	rev := NewRevocations(cfg.DenyList)
	verifier, err := auth.NewJWTVerifier(auth.JWTConfig{Issuer: cfg.Issuer, Audience: Audience, JWKSURL: cfg.JWKS, DenyList: rev.List,
		Client: &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}})
	if err != nil {
		return nil, err
	}
	go rev.Run(ctx)
	go verifier.Run(ctx)
	return &Service{Issuer: cfg.Issuer, Verifier: verifier, Revocations: rev, Grants: func() ([]Grant, error) { return ReadGrants(cfg.Grants) }}, nil
}
