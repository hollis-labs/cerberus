package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/approval"
	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/connector"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// consoleSecrets is a credential store for console writes to write.
type consoleSecrets struct{ values map[string]string }

func (m *consoleSecrets) Get(_ context.Context, service, key string) (string, error) {
	return m.values[service+"/"+key], nil
}
func (m *consoleSecrets) Set(_ context.Context, service, key, value string) error {
	m.values[service+"/"+key] = value
	return nil
}
func (m *consoleSecrets) Delete(_ context.Context, service, key string) error {
	delete(m.values, service+"/"+key)
	return nil
}

// consoleDaemon is a daemon serving console writes over cfgPath, and the
// console's claim to it: a signed-in session.
func consoleDaemon(t *testing.T, sink audit.Sink, cfgPath string, store *consoleSecrets) *SocketClient {
	t.Helper()
	// The daemon's connectors: cloudflare as its plugin declares it, the
	// one provider_save may write for here.
	registry := connector.NewRegistry()
	registry.RegisterDefinition(contract.Definition{ID: "cloudflare", Version: "0.2.0", Config: contract.ConfigSchema{
		Secrets: []contract.SecretRequirement{{Name: "api_token", Description: "Cloudflare API token."}}}})
	client := NewInProcessClient(WithConfigPath(cfgPath), WithConsoleSecretStore(store),
		WithExternalConnectorService(NewExternalConnectorService(sink, registry)),
		WithResourceRuntimeService(NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(&config.ConfigV2{}))))
	socket := startConnectorSocket(t, client)
	socket.claim = func(context.Context) Principal { return WebSessionPrincipal("sess-1") }
	return socket
}

func approvalOf(t *testing.T, err error) *ApprovalRef {
	t.Helper()
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Approval == nil {
		t.Fatalf("want an approval to meet, got %v", err)
	}
	return coded.Approval
}

// A console write is asked for, approved and made in the daemon, where
// the broker holds its approval. A console target has no labels of its own,
// so it is out of band; a write that changed after the approval was made is
// refused and stores nothing, and the approved write runs once.
func TestAConsoleWriteIsApprovedAndMadeInTheDaemon(t *testing.T) {
	sink := audit.NewMemory()
	broker := enforcedBroker(t, sink)
	// Out of band: a test verifier stands in for the presence proof.
	broker.SetPresenceVerifier(acceptPresence{})
	store := &consoleSecrets{values: map[string]string{}}
	client := consoleDaemon(t, sink, filepath.Join(t.TempDir(), "config.yaml"), store)
	ctx := context.Background()
	req := ConsoleWriteRequest{Operation: ConsoleProviderSave, ID: "cloudflare", Secrets: map[string]string{"api_token": "tok-approved"}}

	_, err := client.ConsoleWrite(ctx, req)
	ref := approvalOf(t, err)
	if ref.Channel != approval.ChannelOutOfBand || ref.ID == "" {
		t.Fatalf("a console write should ask for an out-of-band approval the daemon holds: %+v", ref)
	}
	shown, err := client.PlanConsoleWrite(ctx, req)
	if err != nil || !strings.HasPrefix(shown.PlanHash, "sha256:") || shown.Plan.Target.Resource != "cloudflare" {
		t.Fatalf("plan: %+v %v", shown, err)
	}
	if _, err = broker.Decide(ctx, ref.ID, approval.Decision{Approve: true, By: audit.Principal{Kind: "human", Via: "cli"}}); err != nil {
		t.Fatal(err)
	}

	// The same write with another credential is another plan.
	changed := ConsoleWriteRequest{Operation: ConsoleProviderSave, ID: "cloudflare", Secrets: map[string]string{"api_token": "tok-swapped"}}
	if _, stale := client.ConsoleWrite(ctx, changed, WithApprovalID(ref.ID)); stale == nil {
		t.Fatal("a changed write ran under the approval of another")
	}
	if len(store.values) != 0 {
		t.Fatalf("a refused write stored %v", store.values)
	}

	result, err := client.ConsoleWrite(ctx, req, WithApprovalID(ref.ID))
	if err != nil || !result.Success || store.values["cloudflare/api_token"] != "tok-approved" {
		t.Fatalf("approved write: %+v %v %v", result, err, store.values)
	}
	if a, _ := broker.Get(ref.ID); a.Status != approval.Consumed {
		t.Fatalf("approval %+v", a)
	}
}

// Registry, credential and restore targets carry no labels of their own, so
// each is approved as an unlabeled target is: out of band.
func TestConsoleWritesNeedOutOfBand(t *testing.T) {
	sink := audit.NewMemory()
	enforcedBroker(t, sink)
	client := consoleDaemon(t, sink, filepath.Join(t.TempDir(), "config.yaml"), &consoleSecrets{values: map[string]string{}})
	for _, req := range []ConsoleWriteRequest{
		{Operation: ConsoleRegistryDeregister, ID: "someone"},
		{Operation: ConsoleConfigRestore},
		{Operation: ConsoleProviderSave, ID: "cloudflare", Secrets: map[string]string{"api_token": "a"}},
	} {
		_, err := client.ConsoleWrite(context.Background(), req)
		if ref := approvalOf(t, err); ref.Channel != approval.ChannelOutOfBand {
			t.Fatalf("%s: channel %s, want out of band", req.Operation, ref.Channel)
		}
		// Named by what it changes, for the approver to type.
		if shown, perr := client.PlanConsoleWrite(context.Background(), req); perr != nil || shown.Plan.Target.Resource == "" || shown.Plan.Target.Env == string(target.EnvDev) {
			t.Fatalf("%s: plan target %+v %v", req.Operation, shown, perr)
		}
	}
}

// A provider's credentials travel to the daemon and into its store, and
// never into a record, a plan or an approval: the plan binds them by keyed
// digest.
func TestProviderCredentialsReachOnlyTheStore(t *testing.T) {
	sink := audit.NewMemory()
	broker := enforcedBroker(t, sink)
	store := &consoleSecrets{values: map[string]string{}}
	client := consoleDaemon(t, sink, filepath.Join(t.TempDir(), "config.yaml"), store)
	const sentinel = "cf-token-sentinel-0123456789"
	req := ConsoleWriteRequest{Operation: ConsoleProviderSave, ID: "cloudflare", Secrets: map[string]string{"api_token": sentinel}}
	_, err := client.ConsoleWrite(context.Background(), req)
	ref := approvalOf(t, err)
	shown, err := client.PlanConsoleWrite(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := broker.Get(ref.ID)
	for what, v := range map[string]any{"records": sink.Records(), "plan": shown, "approval": a} {
		data, _ := json.Marshal(v)
		if strings.Contains(string(data), sentinel) {
			t.Fatalf("the credential is in the %s", what)
		}
	}
	if store.values["cloudflare/api_token"] != "" {
		t.Fatal("a write awaiting approval stored the credential")
	}
}

// With policy allowing it, a write runs; its refusals keep their status
// over the socket: a refused input 400.
func TestConsoleWriteRefusalsOverTheSocket(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Allow})
	sink := audit.NewMemory()
	store := &consoleSecrets{values: map[string]string{}}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	client := consoleDaemon(t, sink, cfgPath, store)
	ctx := context.Background()

	result, err := client.ConsoleWrite(ctx, ConsoleWriteRequest{Operation: ConsoleProviderSave, ID: "cloudflare", Secrets: map[string]string{"api_token": "tok"}})
	if err != nil || !result.Success || !result.SecretsChanged || store.values["cloudflare/api_token"] != "tok" {
		t.Fatalf("provider save: %+v %v %v", result, err, store.values)
	}
	_, err = client.ConsoleWrite(ctx, ConsoleWriteRequest{Operation: ConsoleRegistryRegister})
	if status, _ := DaemonHTTPStatus(err); status != http.StatusBadRequest || !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("a register with no path: %d %v", status, err)
	}
	restored, err := client.ConsoleWrite(ctx, ConsoleWriteRequest{Operation: ConsoleConfigRestore, Path: "/nonexistent/backup.yaml"})
	if err != nil || restored.Success || restored.Error == "" {
		t.Fatalf("a failed restore answers success false: %+v %v", restored, err)
	}
	if o := outcome(sink.Records()); o.Operation != ConsoleConfigRestore || o.OutcomeCode == audit.OutcomeOK {
		t.Fatalf("the failed restore is not on the record: %+v", o)
	}
	err = client.doJSON(ctx, http.MethodPost, "/console/registry_deregister/confirm", map[string]any{"request": map[string]any{"id": "someone"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "needs confirmed_plan_hash") {
		t.Fatalf("confirm without a hash: %v", err)
	}
}
