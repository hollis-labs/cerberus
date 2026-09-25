package app

import (
	"testing"

	"github.com/hollis-labs/cerberus/internal/connector"
)

func TestRegisterBuiltInConnectorsRegistersDiscoveryMetadata(t *testing.T) {
	registry := connector.NewRegistry()
	registerBuiltInConnectors(registry, nil)

	defs := registry.Definitions()
	var hasDocker, hasForge, hasGitHub, hasNamecheap, hasSSH bool
	for _, def := range defs {
		switch def.ID {
		case "cloudflare":
			// A plugin now (hollis-labs/cerberus-plugins). Registering it
			// here again would also reserve its id and refuse the plugin.
			t.Fatalf("cloudflare is registered as a built-in; it moved to a plugin")
		case "docker":
			hasDocker = true
		case "forge":
			hasForge = true
		case "github":
			hasGitHub = true
		case "namecheap":
			hasNamecheap = true
		case "ssh":
			hasSSH = true
		}
	}
	if !hasDocker || !hasForge || !hasGitHub || !hasNamecheap || !hasSSH {
		t.Fatalf("definitions missing expected built-ins: %#v", defs)
	}
}
