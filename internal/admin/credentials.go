package admin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/secrets"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// The console's credential editor (the old console_credentials.go and webui/credentials.go)
// and its write, provider_save (the old console_writes.go): a connector's declared secrets
// saved to, or cleared from, the keyring under service "cerberus", account "<id>/<name>".

// CredentialsResponse is the credential editor: every connector that declares a secret, each
// secret with whether a value is stored. Names and presence, never a value.
type CredentialsResponse struct {
	Providers []CredentialProvider `json:"providers"`
	Error     string               `json:"error,omitempty"`
}

// CredentialProvider is a connector whose declared secrets the editor can set.
type CredentialProvider struct {
	ID      string             `json:"id"`
	Version string             `json:"version,omitempty"`
	Secrets []CredentialSecret `json:"secrets"`
}

// CredentialSecret is one declared secret, by name, and where its value stands.
type CredentialSecret struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Env         string `json:"env,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Kind        string `json:"kind"`
	// Present is Stored other than "missing".
	Present bool `json:"present"`
	// Stored is "missing", "stored" or "reference", found without resolving anything.
	Stored string `json:"stored"`
}

// CredentialSaveRequest is provider_save's body: secrets to set and names to clear.
type CredentialSaveRequest struct {
	Secrets      map[string]string `json:"secrets"`
	ClearSecrets []string          `json:"clear_secrets"`
	// Values were provider settings the console once saved to a file nothing read; refused.
	Values map[string]string `json:"values,omitempty"`
}

// CredentialSaveResult is what a save did.
type CredentialSaveResult struct {
	Success           bool   `json:"success"`
	PluginReloaded    bool   `json:"plugin_reloaded,omitempty"`
	PluginReloadError string `json:"plugin_reload_error,omitempty"`
	// Plan is the save's plan, for a plan request; nothing was written.
	Plan *gate.ConnectorPlan `json:"-"`
}

// Credentials is the editor's list: every connector, built-in or installed plugin, with a
// secret it declares connector-wide, and whether each has a value.
func (s *Service) Credentials(ctx context.Context) CredentialsResponse {
	resp := CredentialsResponse{Providers: []CredentialProvider{}}
	for _, def := range s.Definitions() {
		provider := CredentialProvider{ID: def.ID, Version: def.Version, Secrets: []CredentialSecret{}}
		for _, sec := range def.Config.Secrets {
			if sec.PerResource {
				// Read as <id>/<resource-id>/<name>: saved connector-wide, nothing would read it.
				continue
			}
			kind := sec.Kind
			if kind == "" {
				kind = contract.SecretKindCredential
			}
			stored, err := s.refs.Presence(ctx, def.ID, sec.Name)
			if err != nil {
				stored = secrets.PresenceMissing
			}
			provider.Secrets = append(provider.Secrets, CredentialSecret{Name: sec.Name, Description: sec.Description, Env: sec.Env,
				Required: sec.Required, Kind: string(kind), Present: stored != secrets.PresenceMissing, Stored: string(stored)})
		}
		if len(provider.Secrets) > 0 {
			resp.Providers = append(resp.Providers, provider)
		}
	}
	sort.Slice(resp.Providers, func(i, j int) bool { return resp.Providers[i].ID < resp.Providers[j].ID })
	return resp
}

// consoleConnector is the connector console writes are recorded under.
const consoleConnector = "console"

// ProviderSave is the console write that saves a connector's credentials.
const ProviderSave = "provider_save"

// consoleWriteOperation is a console write's contract: admin, local files.
func consoleWriteOperation(name string) contract.Operation {
	return contract.Operation{
		Name: name, Effect: contract.EffectAdmin,
		Target:  contract.TargetDescriptor{Kind: "console", From: []string{"id", "config_path", "backup_path"}},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSWrites,
	}.Finalize()
}

// InputError is a write refused for its input, before the gate.
type InputError struct{ Err error }

func (e InputError) Error() string { return e.Err.Error() }
func (e InputError) Unwrap() error { return e.Err }

func inputGuidance(format string, args ...any) error {
	return InputError{redact.Guidance(format, args...)}
}

// SaveCredentials is provider_save through the gate: checked against what the connector
// declares, recorded by name (which credentials changed, never a value), bound in its plan by
// the values' keyed digest, then written to the keyring. A loaded plugin with that id is
// reloaded so it picks the change up.
func (s *Service) SaveCredentials(ctx context.Context, id string, req CredentialSaveRequest, opts GateOpts) (*CredentialSaveResult, error) {
	ctx, scope := redact.EnsureScope(ctx)
	id = strings.TrimSpace(id)
	setSecrets, cleared, err := s.checkProviderSave(id, req)
	if err != nil {
		return nil, err
	}
	spec := gate.Spec{Connector: consoleConnector, Operation: ProviderSave, Op: consoleWriteOperation(ProviderSave), Known: true,
		// The console's own form is the acknowledgment: the operator pressed Save.
		Acknowledged: true, Config: map[string]any{"id": id, "secrets_set": setSecrets, "secrets_cleared": cleared},
		ApprovalID: opts.ApprovalID, PlanOnly: opts.Plan, DryRun: opts.Plan, ConfirmedPlanHash: opts.ConfirmedPlanHash,
		// The target is named by its id, so the approver types what is being changed; it has no
		// labels of its own.
		Resources: func(name string) (*config.ResourceDef, bool) {
			if name != id {
				return nil, false
			}
			return &config.ResourceDef{ID: id}, true
		}}
	planSpec := spec
	digest := s.gate.Sink().Digest
	spec.Plan = func(context.Context) (plan.Plan, error) {
		tgt, _ := gate.AuditTarget(planSpec)
		return plan.Plan{Lane: plan.LaneConsole, Connector: consoleConnector, Operation: ProviderSave, Effect: string(contract.EffectAdmin),
			Target: tgt, ArgsDigest: digest(planSpec.Config), Digests: map[string]string{"secrets": digest(req.Secrets)}}, nil
	}
	call, err := s.gate.Begin(ctx, spec)
	if err != nil {
		return nil, err
	}
	if opts.Plan {
		shown, planErr := call.ShowPlan(ctx)
		call.Finish(planErr)
		if planErr != nil {
			return nil, scopeError(scope, planErr)
		}
		return &CredentialSaveResult{Success: true, Plan: shown}, nil
	}
	changed, err := s.saveProvider(ctx, id, req)
	call.Finish(err)
	if err != nil {
		return nil, scopeError(scope, err)
	}
	out := &CredentialSaveResult{Success: true}
	if changed && s.plugins.Loaded(id) {
		// A plugin receives its credentials at load, so it is reloaded to pick the change up.
		// The secret is saved either way.
		if _, _, err := s.plugins.Unload(ctx, id, GateOpts{}); err != nil {
			out.PluginReloadError = redact.Text(err.Error())
		} else if _, _, err := s.plugins.Load(ctx, id, GateOpts{}); err != nil {
			out.PluginReloadError = redact.Text(err.Error())
		} else {
			out.PluginReloaded = true
		}
	}
	return out, nil
}

// checkProviderSave refuses what provider_save never writes: an undeclared connector or key, a
// per-resource secret, settings. It returns the names set and cleared, sorted.
func (s *Service) checkProviderSave(id string, req CredentialSaveRequest) (set, cleared []string, err error) {
	if id == "" {
		return nil, nil, InputError{errors.New("provider_save needs a provider id")}
	}
	defs := s.Definitions()
	perResource, names := map[string]bool{}, map[string]bool{}
	for _, def := range defs {
		if def.ID != id {
			continue
		}
		for _, sec := range def.Config.Secrets {
			if sec.PerResource {
				perResource[sec.Name] = true
			} else {
				names[sec.Name] = true
			}
		}
	}
	for key := range req.Secrets {
		if perResource[key] {
			return nil, nil, inputGuidance("connector %q reads %s per resource, as %s/<resource-id>/%s, which the console cannot set", id, key, id, key)
		}
	}
	for _, key := range req.ClearSecrets {
		if perResource[key] {
			return nil, nil, inputGuidance("connector %q reads %s per resource, as %s/<resource-id>/%s, which the console cannot clear", id, key, id, key)
		}
	}
	if len(names) == 0 {
		return nil, nil, inputGuidance("no installed connector %q declares a credential, so there is nothing to save for it; install the plugin first", id)
	}
	if len(req.Values) > 0 {
		return nil, nil, inputGuidance("provider_save does not save settings; a plugin's settings are cerberus.connectors in the config")
	}
	var undeclared []string
	for key, value := range req.Secrets {
		if !names[key] {
			undeclared = append(undeclared, key)
			continue
		}
		if strings.TrimSpace(value) != "" {
			set = append(set, key)
		}
	}
	for _, key := range req.ClearSecrets {
		if !names[key] {
			undeclared = append(undeclared, key)
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		declared := make([]string, 0, len(names))
		for name := range names {
			declared = append(declared, name)
		}
		sort.Strings(declared)
		return nil, nil, inputGuidance("connector %q does not declare the secrets %s (it declares %s); nothing was saved",
			id, strings.Join(undeclared, ", "), strings.Join(declared, ", "))
	}
	sort.Strings(set)
	cleared = append([]string{}, req.ClearSecrets...)
	sort.Strings(cleared)
	if set == nil {
		set = []string{}
	}
	return set, cleared, nil
}

// saveProvider writes the keyring: each value under service "cerberus", account
// "<id>/<name>", and each cleared name removed.
func (s *Service) saveProvider(ctx context.Context, id string, req CredentialSaveRequest) (bool, error) {
	changed := false
	for key, value := range req.Secrets {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := s.keyring.Set(ctx, id, key, value); err != nil {
			return changed, fmt.Errorf("store credential %s/%s: %w", id, key, err)
		}
		changed = true
	}
	for _, key := range req.ClearSecrets {
		if err := s.keyring.Delete(ctx, id, key); err != nil {
			return changed, fmt.Errorf("clear credential %s/%s: %w", id, key, err)
		}
		changed = true
	}
	return changed, nil
}
