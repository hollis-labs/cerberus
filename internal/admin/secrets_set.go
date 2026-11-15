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
// Old recorded it with beginAudit alone, and allowed it only from an interactive terminal. Here
// it is an admin write through the gate, recorded as connector "secrets", operation "set", config
// {name}, with old's operation contract: the brakes and policy decide it like any admin write,
// and the value never reaches a record (the plan binds it as a keyed digest). Setting it is the
// owner's (the lead's ruling (a), as for the brakes and plugin install): the owner signed in on
// the console, or a key naming with approval_id an out-of-band approval the owner decided on the
// console, bound to the plan and so to the value. A key's first call opens that approval (409
// approval_pending); an MCP client is refused (428) and opens none.

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
	// Acknowledged is the caller's acknowledgment that the write replaces what the store holds;
	// a write without it is refused (acknowledgment_required), as the other admin writes are.
	Acknowledged bool `json:"acknowledged"`
}

// SecretSetResult is what the write did. Existed says a value was replaced.
type SecretSetResult struct {
	Success bool   `json:"success"`
	Name    string `json:"name"`
	Length  int    `json:"length"`
	Existed bool   `json:"existed"`
	// Proof is what authorized the write: "console" (the owner on the console) or
	// "approval:<id>" (the owner's out-of-band approval).
	Proof string `json:"proof,omitempty"`
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

// SetStoredSecret writes one entry to the credential store through the gate, authorized by the
// owner (secretProof). A malformed name or an empty value is refused before the gate, as old
// refused it before the audit.
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
		Acknowledged: req.Acknowledged, Config: map[string]any{"name": name},
		// The approval named is the owner's (secretProof), never a policy approval for the call.
		PlanOnly: opts.Plan, DryRun: opts.Plan, ConfirmedPlanHash: opts.ConfirmedPlanHash,
		// The owner's approval also meets a policy approve on the write (the lead's ruling (a)).
		OwnerAuthorized: true,
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
	proof, err := s.secretProof(ctx, call, spec, name, req.Acknowledged, opts.ApprovalID)
	if err != nil {
		call.Finish(err)
		return nil, scopeError(scope, err)
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
	return &SecretSetResult{Success: true, Name: name, Length: len(req.Value), Existed: existed, Proof: proof}, nil
}

// secretProof is what authorizes the write: the acknowledgment, then the owner. The owner signed
// in on the console sets it directly; any other caller but an MCP client names the owner's
// out-of-band approval (asked for when none is named), bound to the plan, so to this name and
// value. An MCP client is refused and asks for nothing. Where policy also asks to approve the
// write, the owner's approval is that approval too, one approval consumed once
// (call.PolicyApproves); the console session alone is not an approval, so it does not meet it.
func (s *Service) secretProof(ctx context.Context, call *gate.Call, spec gate.Spec, name string, acknowledged bool, approvalID string) (string, error) {
	if !acknowledged {
		return "", gate.NewError(SecretsConnector, SecretsSet, gate.CodeAckRequired,
			redact.Guidance("storing %s replaces what the credential store holds under that name, so it needs an acknowledgment (\"acknowledged\": true); nothing was stored", name))
	}
	if gate.ConsolePerson(ctx) && approvalID == "" && !call.PolicyApproves() {
		return "console", nil
	}
	tgt, _ := gate.AuditTarget(spec)
	return call.OwnerApproval(ctx, gate.OwnerApprovalRequest{Connector: SecretsConnector, Operation: SecretsSet, Target: tgt,
		ArgsDigest: s.gate.Sink().Digest(spec.Config), ApprovalID: approvalID, What: "storing secret " + name,
		Rule: "secrets.set", Reason: "a stored secret is set by the owner on the console", Plan: spec.Plan})
}
