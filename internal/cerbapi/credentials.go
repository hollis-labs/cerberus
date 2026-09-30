package cerbapi

import (
	"context"
	"errors"
	"sort"
	"sync/atomic"

	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// Read and write credential bindings (I9). A call's credentials are
// resolved for its access, which its effect decides (a dry run or plan
// reads), and its resolved target. The scope rides on the context into the
// credential chain, so a built-in connector's own lookups choose the
// binding without knowing bindings exist.

var bindingsPoint atomic.Pointer[func() (secrets.BindingFile, error)]

// SetCredentialBindings installs the reader of connector-secrets.yaml's
// bindings, for records and explain; nil removes it.
func SetCredentialBindings(read func() (secrets.BindingFile, error)) {
	if read == nil {
		bindingsPoint.Store(nil)
		return
	}
	bindingsPoint.Store(&read)
}

func credentialBindings() secrets.BindingFile {
	if f := bindingsPoint.Load(); f != nil {
		if file, err := (*f)(); err == nil {
			return file
		}
	}
	return secrets.BindingFile{}
}

// credentialScope is the scope a call's credentials resolve under.
func credentialScope(spec auditSpec) secrets.CredentialScope {
	effect := spec.op.Effect
	if !spec.known {
		effect = contract.EffectExec
	}
	_, resolved := auditTarget(spec)
	return secrets.CredentialScope{Access: secrets.AccessFor(effect, spec.dryRun || spec.planOnly), Target: resolved}
}

// withCredentialScope marks ctx with spec's credential scope. It is set
// before the gate: a plan the gate hashes resolves the credentials the run
// then uses, so it must see the run's access.
func withCredentialScope(ctx context.Context, spec auditSpec) context.Context {
	return secrets.WithCredentialScope(ctx, credentialScope(spec))
}

// labeledCredentialNames are a definition's declared secrets as records
// name them: with the binding each would use for this call.
func labeledCredentialNames(def contract.Definition, scope secrets.CredentialScope) []string {
	file := credentialBindings()
	names := make([]string, 0, len(def.Config.Secrets))
	for _, s := range def.Config.Secrets {
		names = append(names, secrets.CredentialName(def.ID, s.Name, file[def.ID].Resolve(def.ID, s.Name, &scope)))
	}
	sort.Strings(names)
	return names
}

// CredentialBindingView is one declared secret's binding for a call, for
// policy explain.
type CredentialBindingView struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
	// None is a binding that refuses the call: no credential for its access.
	None bool `json:"none,omitempty"`
}

// ExplainCredentials is which binding each of def's secrets would use for
// an operation with effect on t.
func ExplainCredentials(def contract.Definition, operation string, preview bool, resolved target.Target) []CredentialBindingView {
	op, known := def.Operation(operation)
	spec := auditSpec{connector: def.ID, operation: operation, op: op, known: known, dryRun: preview}
	scope := credentialScope(spec)
	scope.Target = resolved
	file := credentialBindings()
	var out []CredentialBindingView
	for _, s := range def.Config.Secrets {
		b := file[def.ID].Resolve(def.ID, s.Name, &scope)
		out = append(out, CredentialBindingView{Name: secrets.CredentialName(def.ID, s.Name, b), Label: b.Label, None: b.None})
	}
	return out
}

// noCredentialRefusal codes a call its binding gave no credential as
// credential_missing: the host composed the text from names, so it is
// guidance.
func noCredentialRefusal(args ExternalConnectorOperationArgs, err error) error {
	var none *secrets.NoCredentialError
	var coded *ExternalConnectorError
	if err == nil || errors.As(err, &coded) || !errors.As(err, &none) {
		return err
	}
	return externalConnectorError(args, ExternalConnectorCredentialMissing, redact.Guidance("%s", none.Error()))
}
