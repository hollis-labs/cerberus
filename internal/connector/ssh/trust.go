package ssh

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/chrispian/cerberus/internal/config"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostKeyConfig controls how the SSH connector verifies remote host keys.
type HostKeyConfig struct {
	KnownHostsFile       string
	AllowInsecureHostKey bool
}

func hostKeyConfigFromResourceConfig(cfg map[string]any) HostKeyConfig {
	out := HostKeyConfig{
		KnownHostsFile: defaultKnownHostsFile(),
	}
	if raw, ok := cfg["known_hosts_file"].(string); ok && raw != "" {
		out.KnownHostsFile = config.ExpandHomePath(raw)
	}
	if raw, ok := cfg["allow_insecure_host_key"].(bool); ok {
		out.AllowInsecureHostKey = raw
	}
	return out
}

func (c HostKeyConfig) callback(host string, port int) (gossh.HostKeyCallback, error) {
	if c.AllowInsecureHostKey {
		return gossh.InsecureIgnoreHostKey(), nil //nolint:gosec // explicit operator opt-out
	}

	if c.KnownHostsFile == "" {
		return nil, fmt.Errorf("ssh host key verification requires known_hosts_file or allow_insecure_host_key=true")
	}
	if _, err := os.Stat(c.KnownHostsFile); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("known_hosts file %q not found; create it, set known_hosts_file, or explicitly opt out with allow_insecure_host_key=true", c.KnownHostsFile)
		}
		return nil, fmt.Errorf("stat known_hosts file %q: %w", c.KnownHostsFile, err)
	}

	cb, err := knownhosts.New(c.KnownHostsFile)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts file %q: %w", c.KnownHostsFile, err)
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	return func(hostname string, remote net.Addr, key gossh.PublicKey) error {
		return cb(addr, remote, key)
	}, nil
}

func defaultKnownHostsFile() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".ssh", "known_hosts")
}
