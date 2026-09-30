// Package oauth makes mcp-http an OAuth 2.1 resource server (WP-S8): it
// verifies bearer tokens bound to this resource (RFC 8707), from Cerberus's
// own minimal issuer or from an external authorization server's JWKS, and
// maps the verified token onto scopes the gate enforces.
//
// Cerberus is not an identity provider. The built-in issuer authenticates
// nobody: the operator mints a scoped, short-lived, audience-bound token
// for a named client, on a terminal, and hands it to that client.
package oauth

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ConfigFilename is the mcp-http auth configuration beside the global config.
const ConfigFilename = "mcp-http.yaml"

// Config is mcp-http's auth configuration, read by mcp-http and by the
// daemon, which verifies every forwarded token again.
//
//	resource: https://cerberus.example:4785/mcp
//	builtin: true
//	issuers:
//	  - issuer: https://idp.example.com
//	    jwks_uri: https://idp.example.com/jwks   # else discovered
//	    client_claim: azp                         # default client_id, then azp
//	    scopes_claim: scope                       # default scope, then scp
//	tls:
//	  cert: /path/to/fullchain.pem
//	  key: /path/to/key.pem
type Config struct {
	// Resource is this resource's canonical URL: the audience every token
	// must carry.
	Resource string `yaml:"resource"`
	// Builtin enables Cerberus's own operator-issued tokens.
	Builtin bool           `yaml:"builtin"`
	Issuers []IssuerConfig `yaml:"issuers"`
	TLS     TLSConfig      `yaml:"tls"`
	// MaxTTL caps a built-in token's lifetime; default MaxBuiltinTTL.
	MaxTTL time.Duration `yaml:"max_ttl"`
}

// IssuerConfig is one external authorization server.
type IssuerConfig struct {
	Issuer string `yaml:"issuer"`
	// JWKSURI is the key set; empty discovers it from the issuer's RFC 8414
	// (or OpenID) metadata.
	JWKSURI string `yaml:"jwks_uri"`
	// Audience overrides the resource as the expected aud, for an issuer
	// that cannot emit RFC 8707 resource indicators. Leave it empty.
	Audience    string `yaml:"audience"`
	ClientClaim string `yaml:"client_claim"`
	ScopesClaim string `yaml:"scopes_claim"`
}

// TLSConfig is the certificate mcp-http serves off loopback.
type TLSConfig struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// Lifetimes of built-in tokens.
const (
	DefaultBuiltinTTL = 7 * 24 * time.Hour
	MaxBuiltinTTL     = 90 * 24 * time.Hour
)

// Configured reports whether any issuer is configured, which makes a token
// required on every mcp-http listener, loopback included.
func (c Config) Configured() bool { return c.Builtin || len(c.Issuers) > 0 }

// BuiltinIssuer is the built-in issuer's identifier: the resource's origin.
func (c Config) BuiltinIssuer() string {
	u, err := url.Parse(c.Resource)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// MaxLifetime is the longest built-in token the operator may mint.
func (c Config) MaxLifetime() time.Duration {
	if c.MaxTTL > 0 && c.MaxTTL < MaxBuiltinTTL {
		return c.MaxTTL
	}
	return MaxBuiltinTTL
}

// ErrNotConfigured is a config file that does not exist.
var ErrNotConfigured = errors.New("mcp-http auth is not configured")

// LoadConfig reads path. An absent file is ErrNotConfigured; a file that
// does not parse or validate is an error, and callers fail closed on it.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // an operator-owned path beside the global config
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotConfigured
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err = dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if problems := c.Problems(); len(problems) > 0 {
		return Config{}, fmt.Errorf("%s: %s", path, strings.Join(problems, "; "))
	}
	return c, nil
}

// Problems are why a config cannot be used.
func (c Config) Problems() []string {
	var out []string
	if !c.Configured() {
		return nil
	}
	u, err := url.Parse(c.Resource)
	switch {
	case c.Resource == "":
		out = append(out, "resource is required: the URL clients reach /mcp by, which every token's aud must name")
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		out = append(out, fmt.Sprintf("resource %q is not an absolute http(s) URL", c.Resource))
	case u.Scheme == "http" && !IsLoopbackHost(u.Hostname()):
		out = append(out, fmt.Sprintf("resource %q is plain http off loopback; a bearer token must not cross a network in plaintext, so use https with tls.cert and tls.key", c.Resource))
	case u.Fragment != "" || u.RawQuery != "":
		out = append(out, fmt.Sprintf("resource %q carries a query or fragment, which RFC 8707 forbids", c.Resource))
	}
	for i, is := range c.Issuers {
		iu, err := url.Parse(is.Issuer)
		if is.Issuer == "" || err != nil || iu.Scheme != "https" {
			out = append(out, fmt.Sprintf("issuers[%d].issuer %q must be an https URL", i, is.Issuer))
		}
		if is.JWKSURI != "" {
			if ju, err := url.Parse(is.JWKSURI); err != nil || ju.Scheme != "https" {
				out = append(out, fmt.Sprintf("issuers[%d].jwks_uri %q must be an https URL", i, is.JWKSURI))
			}
		}
		if c.Builtin && strings.TrimSuffix(is.Issuer, "/") == c.BuiltinIssuer() {
			out = append(out, fmt.Sprintf("issuers[%d].issuer is the built-in issuer's own identifier", i))
		}
	}
	if (c.TLS.Cert == "") != (c.TLS.Key == "") {
		out = append(out, "tls needs both cert and key")
	}
	return out
}

// IsLoopbackHost reports whether host names this machine only.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
