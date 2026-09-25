package app

import (
	"testing"

	"github.com/hollis-labs/cerberus/internal/connector"
)

func TestRegisterBuiltInConnectorsRegistersDiscoveryMetadata(t *testing.T) {
	registry := connector.NewRegistry()
	registerBuiltInConnectors(registry, nil)

	defs := registry.Definitions()
	var hasDocker, hasForge, hasGitHub, hasSSH bool
	for _, def := range defs {
		switch def.ID {
		case "cloudflare", "digitalocean", "namecheap":
			// Plugins now (hollis-labs/cerberus-plugins). Registering one
			// here again would also reserve its id and refuse the plugin.
			t.Fatalf("%s is registered as a built-in; it moved to a plugin", def.ID)
		case "docker":
			hasDocker = true
		case "forge":
			hasForge = true
		case "github":
			hasGitHub = true
		case "ssh":
			hasSSH = true
		}
	}
	if !hasDocker || !hasForge || !hasGitHub || !hasSSH {
		t.Fatalf("definitions missing expected built-ins: %#v", defs)
	}
}
