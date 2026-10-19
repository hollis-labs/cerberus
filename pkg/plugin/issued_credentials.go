package plugin

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// CredentialIssuanceCapability must be declared by a plugin that has issued
// credentials. Older hosts reject the unknown capability at install instead
// of silently ignoring issuance semantics. It grants no store access.
const CredentialIssuanceCapability = "credential_issuance_v1" //nolint:gosec // capability name, not a credential
const IssuanceConfigKey = "cerberus.issuance.v1"
const IssuedValuePrefix = "cerberus.issued."

// Issuance is the names-only metadata delivered beside private Init values.
// A plugin must check Operation and ID against the call before enabling a login.
type Issuance struct {
	ID            string            `json:"id"`
	Operation     string            `json:"operation"`
	BindingDigest string            `json:"binding_digest"`
	References    map[string]string `json:"references"`
}

// IssuanceFromConfig reads the host-owned private handoff; it does not resolve
// any secret. Values are under IssuedValuePrefix + the manifest-declared name.
// Use ConfigReader.Secret for each one so the SDK logger knows to redact it.
func IssuanceFromConfig(config map[string]string) (Issuance, error) {
	var i Issuance
	if json.Unmarshal([]byte(config[IssuanceConfigKey]), &i) != nil || i.ID == "" || i.Operation == "" || i.BindingDigest == "" || len(i.References) == 0 {
		return Issuance{}, errors.New("host credential issuance handoff is required")
	}
	return i, nil
}

// validateIssuance refuses incomplete declarations at install, including a
// missing capability (the version-skew guard) and a missing request identity.
func (p PluginYAML) validateIssuance() []string {
	var problems []string
	capable := false
	for _, c := range p.Capabilities {
		if c.Name == CredentialIssuanceCapability {
			capable = true
		}
	}
	for _, field := range p.Cerberus.Connector.Config.Fields {
		if strings.HasPrefix(field.Name, "cerberus.") {
			problems = append(problems, "cerberus. config keys are reserved for private host handoffs")
		}
	}
	for _, secret := range p.Cerberus.Connector.Config.Secrets {
		if strings.HasPrefix(secret.Name, "cerberus.") {
			problems = append(problems, "cerberus. secret names are reserved for private host handoffs")
		}
	}
	for _, op := range p.Cerberus.Connector.Operations {
		if len(op.IssuedSecrets) == 0 {
			continue
		}
		if !capable {
			problems = append(problems, "issued_secrets requires capability "+CredentialIssuanceCapability)
		}
		if op.EffectiveEffect() != contract.EffectWrite && op.EffectiveEffect() != contract.EffectAdmin {
			problems = append(problems, "issued_secrets requires a write or admin operation")
		}
		if op.EffectivePreview() != contract.PreviewPlugin || op.LocalFS != contract.LocalFSWrites {
			problems = append(problems, "issued_secrets requires a plugin preview and local_fs writes")
		}
		fields := op.Operation().Inputs
		validID := false
		for _, f := range fields {
			if f.Name == "issuance_id" && f.Required && f.Schema["type"] == "string" {
				validID = true
			}
		}
		if !validID || op.Operation().InputsOpen {
			problems = append(problems, "issued_secrets requires closed inputs and required string issuance_id")
		}
		if len(op.IssuedSecrets) > 8 {
			problems = append(problems, "issued_secrets permits at most eight names")
		}
		seen := map[string]bool{}
		for _, name := range op.IssuedSecrets {
			if !issuedName.MatchString(name) || seen[name] {
				problems = append(problems, "issued_secrets contains an invalid or duplicate name")
			}
			seen[name] = true
		}
	}
	return problems
}

var issuedName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
