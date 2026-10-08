package config

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// WebConfig is operator-owned console configuration. PublicURL names the
// HTTPS origin of a reverse proxy; the console itself still binds loopback.
type WebConfig struct {
	PublicURL string `yaml:"public_url,omitempty"`
}

// NormalizeWebPublicURL accepts an HTTPS DNS origin served at the root.
// Empty retains the local console default. Credentials, queries, fragments
// and subpaths are refused so login tokens only go to the configured origin.
func NormalizeWebPublicURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") ||
		net.ParseIP(u.Hostname()) != nil || strings.EqualFold(u.Hostname(), "localhost") {
		return "", redact.Guidance("web.public_url must be an HTTPS DNS origin at /, without credentials, query or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if len(host) > 253 || strings.HasSuffix(u.Host, ":") {
		return "", redact.Guidance("web.public_url has an invalid hostname or port")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", redact.Guidance("web.public_url has an invalid DNS hostname")
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
				return "", redact.Guidance("web.public_url has an invalid DNS hostname")
			}
		}
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", redact.Guidance("web.public_url has an invalid port")
		}
		if n != 443 {
			host = net.JoinHostPort(host, strconv.Itoa(n))
		}
	}
	return "https://" + host, nil
}
