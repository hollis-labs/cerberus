package app

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

	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/dataaccess"
	auth "github.com/hollis-labs/libs/security/auth"
)

// DataService is opt-in. No grants, listener or DB credentials are provisioned
// by loading it. The daemon reauthorizes every call, including forwarded MCP.
func DataService(ctx context.Context, external *cerbapi.ExternalConnectorService, configPath string) (*dataaccess.Service, error) {
	path := filepath.Join(filepath.Dir(configPath), "data-access.json")
	b, err := os.ReadFile(path) //nolint:gosec // fixed sibling of the operator-selected registry config
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm()&0022 != 0 || len(b) > 65536 {
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
	rev := dataaccess.NewRevocations(cfg.DenyList)
	verifier, err := auth.NewJWTVerifier(auth.JWTConfig{Issuer: cfg.Issuer, Audience: dataaccess.Audience, JWKSURL: cfg.JWKS, DenyList: rev.List, Client: &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}})
	if err != nil {
		return nil, err
	}
	d := &dataaccess.Service{Issuer: cfg.Issuer, Verifier: verifier, Revocations: rev, Grants: func() ([]dataaccess.Grant, error) { return dataaccess.ReadGrants(cfg.Grants) }}
	external.BindDataService(d)
	go rev.Run(ctx)
	go verifier.Run(ctx)
	return d, nil
}
