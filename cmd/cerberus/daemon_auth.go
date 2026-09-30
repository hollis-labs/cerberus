package main

import (
	"context"
	"errors"

	"github.com/hollis-labs/cerberus/internal/app"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/cerbapi"
	"github.com/hollis-labs/cerberus/internal/oauth"
)

// mcpHTTPAuthConfig is mcp-http's auth config, or ErrNotConfigured. Tests
// swap it.
var mcpHTTPAuthConfig = func() (oauth.Config, error) {
	path, err := app.MCPHTTPConfigPath()
	if err != nil {
		return oauth.Config{}, err
	}
	return oauth.LoadConfig(path)
}

// newAuth builds the daemon's (or an in-process CLI's) mcp-http auth from
// its config: nil when none is configured. The built-in issuer's key lives
// in keys (the Keychain); its token records under ~/.cerberus/oauth.
func newAuth(ctx context.Context, sink audit.Sink, keys oauth.KeyStore) (*cerbapi.Auth, error) {
	cfg, err := mcpHTTPAuthConfig()
	if errors.Is(err, oauth.ErrNotConfigured) || (err == nil && !cfg.Configured()) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a := &cerbapi.Auth{Config: cfg, Sink: sink}
	var opts oauth.VerifierOptions
	if cfg.Builtin {
		key, kerr := oauth.LoadOrCreateKey(ctx, keys)
		if kerr != nil {
			return nil, kerr
		}
		dir, derr := app.OAuthDir()
		if derr != nil {
			return nil, derr
		}
		iss := &oauth.Issuer{Config: cfg, Key: key, Store: oauth.TokenStore{Dir: dir}}
		set := iss.JWKS()
		opts.Builtin, opts.Revoked, a.Issuer = &set, iss.Store.Revoked, iss
	}
	v, err := oauth.NewVerifier(cfg, opts)
	if err != nil {
		return nil, err
	}
	a.Verifier = v
	return a, nil
}
