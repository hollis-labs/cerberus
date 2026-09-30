package cerbapi

import (
	"sort"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// CredentialProvider is a connector whose declared secrets the console's
// credential editor can set: a built-in, or an installed plugin, as its
// definition declares them. The editor lists nothing else, and provider_save
// writes nothing else (checkProviderSave).
type CredentialProvider struct {
	ID      string             `json:"id"`
	Version string             `json:"version,omitempty"`
	Secrets []CredentialSecret `json:"secrets"`
}

// CredentialSecret is one declared secret, by name. Kind says whether its
// value is a credential or a name or path the secret chain carries.
//
// It travels under "secrets", a credential-shaped key: the response walk
// keeps its name, kind and env as names by schema (walkSecretRequirement in
// internal/redact), and removes any value the request resolved.
type CredentialSecret struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Env         string              `json:"env,omitempty"`
	Required    bool                `json:"required,omitempty"`
	Kind        contract.SecretKind `json:"kind"`
}

// CredentialCatalog is every connector in defs that declares a secret the
// editor can write, by id, each secret in its declared order. A secret
// resolved per resource (ssh's key, read as ssh/<resource-id>/key) is left
// out: saved as <connector>/<name>, nothing would read it. A connector with
// nothing else is left out too.
func CredentialCatalog(defs []contract.Definition) []CredentialProvider {
	out := []CredentialProvider{}
	for _, def := range defs {
		provider := CredentialProvider{ID: def.ID, Version: def.Version, Secrets: []CredentialSecret{}}
		for _, s := range def.Config.Secrets {
			if s.PerResource {
				continue
			}
			kind := s.Kind
			if kind == "" {
				kind = contract.SecretKindCredential
			}
			provider.Secrets = append(provider.Secrets, CredentialSecret{Name: s.Name, Description: s.Description, Env: s.Env, Required: s.Required, Kind: kind})
		}
		if len(provider.Secrets) > 0 {
			out = append(out, provider)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// declaredSecretNames is the set of connector-wide secret names connector id
// declares, and whether any connector in defs has that id and declares one.
// A per-resource secret is not in it (CredentialCatalog).
func declaredSecretNames(defs []contract.Definition, id string) (map[string]bool, bool) {
	for _, def := range defs {
		if def.ID != id {
			continue
		}
		names := map[string]bool{}
		for _, s := range def.Config.Secrets {
			if !s.PerResource {
				names[s.Name] = true
			}
		}
		if len(names) > 0 {
			return names, true
		}
	}
	return nil, false
}

// connectorDefinitions is every connector this process serves, built-in or
// installed plugin, with its declared config: what the credential editor
// lists and provider_save checks against.
func (c *InProcessClient) connectorDefinitions() []contract.Definition {
	if c.external == nil {
		return nil
	}
	return c.external.Definitions()
}

// perResourceSecretNames is the secrets connector id resolves per resource.
func perResourceSecretNames(defs []contract.Definition, id string) map[string]bool {
	names := map[string]bool{}
	for _, def := range defs {
		if def.ID != id {
			continue
		}
		for _, s := range def.Config.Secrets {
			if s.PerResource {
				names[s.Name] = true
			}
		}
	}
	return names
}
