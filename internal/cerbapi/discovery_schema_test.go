package cerbapi

import (
	"sort"
	"testing"

	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Discovery must advertise exactly what enforcement accepts. For ssh and
// docker, every property in an operation's input schema must be a key the
// admin lane lets a socket/web/MCP caller send, and every key it lets through
// must appear in some operation's schema. An agent reading `connectors
// describe` then builds calls that are accepted, and a key added to either
// side without the other fails here.
func TestDiscoverySchemasMatchEnforcement(t *testing.T) {
	for _, tc := range []struct {
		def     contract.Definition
		allowed map[string]bool
	}{
		{sshconn.Definition(), sshOperationFields},
		{dockerconn.Definition(), dockerCallerFields()},
	} {
		advertised := map[string]bool{}
		for _, op := range tc.def.Operations {
			props, _ := op.InputSchema["properties"].(map[string]any)
			for key := range props {
				advertised[key] = true
				if !tc.allowed[key] {
					t.Errorf("%s.%s advertises %q, which enforcement refuses", tc.def.ID, op.Name, key)
				}
			}
		}
		var missing []string
		for key := range tc.allowed {
			if !advertised[key] {
				missing = append(missing, key)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s: enforcement accepts %v, which no operation schema advertises", tc.def.ID, missing)
		}
	}
}

// Ad-hoc targets are never advertised, on any ssh or docker operation.
func TestDiscoverySchemasOfferNoConnectionFields(t *testing.T) {
	forbidden := map[string][]string{
		"ssh":    {"host", "port", "user", "key_file", "known_hosts_file", "allow_insecure_host_key", "name"},
		"docker": dockerconn.TargetKeys(),
	}
	for _, def := range []contract.Definition{sshconn.Definition(), dockerconn.Definition()} {
		for _, op := range def.Operations {
			props, _ := op.InputSchema["properties"].(map[string]any)
			for _, key := range forbidden[def.ID] {
				if _, ok := props[key]; ok {
					t.Errorf("%s.%s advertises connection field %q", def.ID, op.Name, key)
				}
			}
		}
	}
}

// Every ssh operation needs the resource id; the schema says so.
func TestSSHSchemasRequireTheResourceID(t *testing.T) {
	for _, op := range sshconn.Definition().Operations {
		required, _ := op.InputSchema["required"].([]string)
		found := false
		for _, key := range required {
			found = found || key == sshconn.FieldID
		}
		if !found {
			t.Errorf("ssh.%s does not require id", op.Name)
		}
	}
}

// Plugin manifests generated from a Definition carry the same schemas.
func TestManifestFromDefinitionCarriesTheDiscoverySchemas(t *testing.T) {
	for _, def := range []contract.Definition{sshconn.Definition(), dockerconn.Definition()} {
		manifest := contract.ManifestFromDefinition(def)
		for i, op := range manifest.Operations {
			want, _ := def.Operations[i].InputSchema["properties"].(map[string]any)
			got, _ := op.InputSchema["properties"].(map[string]any)
			if len(got) != len(want) {
				t.Errorf("%s.%s: manifest schema has %d properties, definition %d", def.ID, op.Name, len(got), len(want))
			}
		}
	}
}
