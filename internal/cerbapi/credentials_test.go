package cerbapi

import (
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/secretref"
	"github.com/hollis-labs/cerberus/internal/secrets"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// A call's credentials resolve for its access and target (I9): a read
// reads, a change writes, a dry run reads, and the target is the call's.
func TestCallsCarryTheirCredentialScope(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Allow})
	svc, backend := dockerLane(t, audit.NewMemory())
	ctx := callerAs(confirmHuman, SurfaceSocket)
	if _, err := svc.Execute(ctx, ExternalConnectorOperationArgs{Connector: "docker", Operation: "list_containers", Config: map[string]any{"resource": "dev-box"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Execute(ctx, devStop()); err != nil {
		t.Fatal(err)
	}
	if len(backend.scopes) != 2 || backend.scopes[0].Access != secrets.AccessRead || backend.scopes[1].Access != secrets.AccessWrite || backend.scopes[1].Target.ID != "dev-box" {
		t.Fatalf("scopes: %+v", backend.scopes)
	}
	spec := auditSpec{connector: "docker", operation: "stop", op: contract.Operation{Effect: contract.EffectLifecycle}, known: true, dryRun: true}
	if credentialScope(spec).Access != secrets.AccessRead {
		t.Fatal("a dry run resolves for a write")
	}
}

// Records name the binding a credential would use; a binding with no
// credential for the call's access is credential_missing, as guidance.
func TestCredentialLabelsAndRefusal(t *testing.T) {
	file, err := secrets.ParseBindings([]byte("fake:\n  read: { token: keychain://fake/ro }\n  write: { token: keychain://fake/rw }\n  targets:\n    - match: { env: prod }\n      read: { token: keychain://fake/prod-ro }\n      write: { token: null }\n"), "f", secretref.IsRef)
	if err != nil {
		t.Fatal(err)
	}
	SetCredentialBindings(func() (secrets.BindingFile, error) { return file, nil })
	t.Cleanup(func() { SetCredentialBindings(nil) })
	def := contract.Definition{ID: "fake", Config: contract.ConfigSchema{Secrets: []contract.SecretRequirement{{Name: "token"}}},
		Operations: []contract.Operation{{Name: "get", Effect: contract.EffectRead}, {Name: "set", Effect: contract.EffectWrite}}}
	prod := target.Target{Kind: "fake.thing", ID: "p1", Labels: target.Labels{Env: target.EnvProd}}
	if got := labeledCredentialNames(def, secrets.CredentialScope{Access: secrets.AccessWrite, Target: target.Target{Kind: "fake.thing", ID: "d1", Labels: target.Labels{Env: target.EnvDev}}}); strings.Join(got, ",") != "fake/token@write" {
		t.Fatalf("dev write: %v", got)
	}
	if got := labeledCredentialNames(def, secrets.CredentialScope{Access: secrets.AccessRead, Target: prod}); strings.Join(got, ",") != "fake/token@targets[0].read" {
		t.Fatalf("prod read: %v", got)
	}
	views := ExplainCredentials(def, "set", false, prod)
	if len(views) != 1 || !views[0].None || views[0].Label != "targets[0].write" {
		t.Fatalf("explain a prod write: %+v", views)
	}
	err = noCredentialRefusal(ExternalConnectorOperationArgs{Connector: "fake", Operation: "set"},
		&secrets.NoCredentialError{Connector: "fake", Key: "token", Access: secrets.AccessWrite, Target: "p1", Label: "targets[0].write"})
	if connectorErrorCode(err) != ExternalConnectorCredentialMissing || !strings.Contains(err.Error(), "fake writes on p1 have no write credential") {
		t.Fatalf("refusal: %v", err)
	}
	if redact.Text(err.Error()) != err.Error() {
		t.Fatalf("redaction rewrote the refusal: %q", redact.Text(err.Error()))
	}
}
