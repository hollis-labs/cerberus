package admin

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/gate"
	"github.com/hollis-labs/cerberus/internal/plan"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// `cerberus secrets set <service>/<key>` (the old cerbapi.SetStoredSecret): one entry written to
// the OS credential store, service "cerberus", account "<service>/<key>", the store every
// keyring:// reference and every connector's last fallback reads. It is how an operator stores a
// secret backend's own credential (a Keeper configuration, a 1Password service account token),
// which must not come from another vault, and it is done before the plugin that reads it is
// installed: any well-formed name is accepted, declared by an installed connector or not. That
// is what sets it apart from provider_save, which writes only what an installed connector
// declares.
//
// Old recorded it with beginAudit alone. Here it is an admin write through the gate, recorded
// as connector "secrets", operation "set", config {name}, with old's operation contract: the
// brakes, policy and approvals decide it like any admin write, and the value never reaches a
// record (the plan binds it as a keyed digest).

// SecretsConnector and SecretsSet are the names a stored-secret write is recorded under.
const (
	SecretsConnector = "secrets"
	SecretsSet       = "set"
)

// secretNamePart is one half of a stored secret's name: a connector or plugin id, or a secret
// name (old's regex).
var secretNamePart = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// secretStoreOperation is old's contract for the write: admin, target a secret named by "name".
func secretStoreOperation() contract.Operation {
	return contract.Operation{
		Name: SecretsSet, Effect: contract.EffectAdmin,
		Target:  contract.TargetDescriptor{Kind: "secret", From: []string{"name"}},
		Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone,
	}.Finalize()
}

// SecretSetRequest is the write's body: the name as <service>/<key> and the value.
type SecretSetRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SecretSetResult is what the write did. Existed says a value was replaced.
type SecretSetResult struct {
	Success bool   `json:"success"`
	Name    string `json:"name"`
	Length  int    `json:"length"`
	Existed bool   `json:"existed"`
	// Plan is the write's plan, for a plan request; nothing was written.
	Plan *gate.ConnectorPlan `json:"-"`
}

// SecretExists reports whether the credential store holds a value for name (a well-formed
// <service>/<key>), without returning it: the CLI warns before it replaces one.
func (s *Service) SecretExists(ctx context.Context, name string) (bool, error) {
	service, key, err := splitSecretName(name)
	if err != nil {
		return false, err
	}
	v, err := s.keyring.Get(ctx, service, key)
	return err == nil && v != "", nil
}

// splitSecretName is old's check: each half of <service>/<key> matches secretNamePart.
func splitSecretName(name string) (string, string, error) {
	service, key, ok := strings.Cut(strings.TrimSpace(name), "/")
	if !ok || !secretNamePart.MatchString(service) || !secretNamePart.MatchString(key) {
		return "", "", InputError{fmt.Errorf("%q is not a secret name: use <connector or plugin id>/<secret name>, lowercase, for example keeper/ksm_config", name)}
	}
	return service, key, nil
}

// SetStoredSecret writes one entry to the credential store through the gate. A malformed name or
// an empty value is refused before the gate, as old refused it before the audit.
func (s *Service) SetStoredSecret(ctx context.Context, req SecretSetRequest, opts GateOpts) (*SecretSetResult, error) {
	ctx, scope := redact.EnsureScope(ctx)
	service, key, err := splitSecretName(req.Name)
	if err != nil {
		return nil, err
	}
	if req.Value == "" {
		return nil, InputError{errors.New("the value is empty; nothing was stored")}
	}
	name := service + "/" + key
	spec := gate.Spec{Connector: SecretsConnector, Operation: SecretsSet, Op: secretStoreOperation(), Known: true,
		// Typing the value at the operator's own terminal is the act; the CLI sends nothing else.
		Acknowledged: true, Config: map[string]any{"name": name},
		ApprovalID: opts.ApprovalID, PlanOnly: opts.Plan, DryRun: opts.Plan, ConfirmedPlanHash: opts.ConfirmedPlanHash,
		// The target is the secret's name, so an approver types what is being set; it has no
		// labels of its own.
		Resources: func(id string) (*config.ResourceDef, bool) {
			if id != name {
				return nil, false
			}
			return &config.ResourceDef{ID: name}, true
		}}
	planSpec := spec
	digest := s.gate.Sink().Digest
	spec.Plan = func(context.Context) (plan.Plan, error) {
		tgt, _ := gate.AuditTarget(planSpec)
		// The value is bound by its keyed digest: an approval is for this value, and a retry
		// with another is plan_stale.
		return plan.Plan{Lane: plan.LaneAdmin, Connector: SecretsConnector, Operation: SecretsSet, Effect: string(contract.EffectAdmin),
			Target: tgt, ArgsDigest: digest(planSpec.Config), Digests: map[string]string{"value": digest(req.Value)}}, nil
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
		return &SecretSetResult{Success: true, Name: name, Plan: shown}, nil
	}
	existed := false
	if v, getErr := s.keyring.Get(ctx, service, key); getErr == nil && v != "" {
		existed = true
	}
	err = s.keyring.Set(ctx, service, key, req.Value)
	call.Finish(err)
	if err != nil {
		return nil, scopeError(scope, fmt.Errorf("store credential %s: %w", name, err))
	}
	return &SecretSetResult{Success: true, Name: name, Length: len(req.Value), Existed: existed}, nil
}
