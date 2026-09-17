package app

import (
	"testing"

	"github.com/hollis-labs/cerberus/internal/connector"
)

func TestRegisterBuiltInConnectorsRegistersDiscoveryMetadata(t *testing.T) {
	registry := connector.NewRegistry()
	registerBuiltInConnectors(registry, nil)

	defs := registry.Definitions()
	var hasCloudflare, hasDocker, hasForge, hasGitHub, hasNamecheap, hasSSH bool
	for _, def := range defs {
		switch def.ID {
		case "cloudflare":
			hasCloudflare = true
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
	if !hasCloudflare || !hasDocker || !hasForge || !hasGitHub || !hasNamecheap || !hasSSH {
		t.Fatalf("definitions missing expected built-ins: %#v", defs)
	}
}
