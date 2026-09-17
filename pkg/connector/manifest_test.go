package connector

import (
	"strings"
	"testing"
)

func TestManifestFromDefinitionValidates(t *testing.T) {
	def := Definition{
		ID:            "docker",
		Version:       "builtin",
		ResourceTypes: []string{"container"},
		Capabilities:  Capabilities{CanDestroy: true},
		Config: ConfigSchema{
			Fields: []ConfigField{{Name: "container", Type: "string"}},
		},
		Operations: []Operation{
			{Name: "start", InputSchema: ObjectSchema(map[string]any{})},
			{Name: "destroy", InputSchema: ObjectSchema(map[string]any{}), Destructive: true},
		},
	}

	manifest := ManifestFromDefinition(def)
	if manifest.APIVersion != ManifestAPIVersion {
		t.Fatalf("APIVersion = %q, want %q", manifest.APIVersion, ManifestAPIVersion)
	}
	if !manifest.Operations[1].RequiresAck {
		t.Fatal("destructive operation should require ack")
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestManifestValidateRejectsMissingRequiredFields(t *testing.T) {
	err := (Manifest{}).Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"api_version", "kind", "id", "version", "resource_type", "operation"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("validation error missing %q: %v", want, err)
		}
	}
}

func TestManifestValidateRejectsUnsafeDestructiveOperation(t *testing.T) {
	manifest := Manifest{
		APIVersion:    ManifestAPIVersion,
		Kind:          "Connector",
		ID:            "cloudflare",
		Version:       "builtin",
		ResourceTypes: []string{"domain"},
		Operations: []ManifestOperation{
			{Name: "delete_dns_record", InputSchema: ObjectSchema(map[string]any{}), Destructive: true},
		},
	}

	err := manifest.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "requires_ack") {
		t.Fatalf("validation error missing requires_ack: %v", err)
	}
}

func TestDefinitionFromManifestRoundTripsDiscoveryFields(t *testing.T) {
	def := Definition{
		ID:            "docker",
		Version:       "1.2.3",
		ResourceTypes: []string{"container"},
		Capabilities:  Capabilities{CanDestroy: true, CanLogs: true},
		Config: ConfigSchema{
			Fields: []ConfigField{{Name: "socket", Type: "string", Required: true}},
		},
		Operations: []Operation{
			{
				Name:        "logs",
				Description: "tail logs",
				InputSchema: ObjectSchema(map[string]any{"container": StringSchema("container")}, "container"),
			},
		},
	}

	roundTrip := DefinitionFromManifest(ManifestFromDefinition(def))
	if roundTrip.ID != def.ID || roundTrip.Version != def.Version {
		t.Fatalf("roundTrip = %#v", roundTrip)
	}
	if len(roundTrip.Operations) != 1 || roundTrip.Operations[0].Name != "logs" {
		t.Fatalf("Operations = %#v", roundTrip.Operations)
	}
	if len(roundTrip.Config.Fields) != 1 || roundTrip.Config.Fields[0].Name != "socket" {
		t.Fatalf("Config = %#v", roundTrip.Config)
	}
}

// The host keys resolved secrets into the Init config map by secret name, so a
// config field of the same name would make which value wins depend on map
// ordering. Reject it at the manifest instead.
func TestManifestRejectsSecretCollidingWithConfigField(t *testing.T) {
	manifest := Manifest{
		APIVersion:    ManifestAPIVersion,
		Kind:          "Connector",
		ID:            "contextforge",
		Version:       "dev",
		ResourceTypes: []string{"gateway"},
		Config: ConfigSchema{
			Fields:  []ConfigField{{Name: "token", Type: "string"}},
			Secrets: []SecretRequirement{{Name: "token", Required: true}},
		},
		Operations: []ManifestOperation{{Name: "list_gateways"}},
	}

	err := manifest.Validate()
	if err == nil {
		t.Fatal("expected a validation error for a secret colliding with a config field")
	}
	if !strings.Contains(err.Error(), "collides with a config field") {
		t.Fatalf("error %q should explain the collision", err.Error())
	}
}
