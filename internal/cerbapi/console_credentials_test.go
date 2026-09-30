package cerbapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	"github.com/hollis-labs/cerberus/internal/connector"
	sshconn "github.com/hollis-labs/cerberus/internal/connector/ssh"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// pluginCredentialDaemon is a daemon whose only connector with a secret is an
// installed plugin, leaky, which declares one: token.
func pluginCredentialDaemon(t *testing.T, sink audit.Sink, store *consoleSecrets) *SocketClient {
	t.Helper()
	managed := leakyManagedService(t)
	client := NewInProcessClient(WithConfigPath(filepath.Join(t.TempDir(), "config.yaml")), WithConsoleSecretStore(store),
		WithExternalConnectorService(NewExternalConnectorService(sink, connector.NewRegistry(), managed)),
		WithResourceRuntimeService(NewResourceRuntimeService(sink, WithResourceRuntimeConfigV2(&config.ConfigV2{}))))
	socket := startConnectorSocket(t, client)
	socket.claim = func(context.Context) Principal { return WebSessionPrincipal("sess-1") }
	return socket
}

// A plugin's declared secrets are what the credential editor lists, as the
// console reads them over the socket, and what provider_save writes. A key
// or id nothing declares is refused over the socket, before anything is
// stored, with the declared names in the refusal.
func TestProviderSaveWritesOnlyDeclaredSecrets(t *testing.T) {
	withPDP(t, constantPDP{decision: policy.Allow})
	sink := audit.NewMemory()
	store := &consoleSecrets{values: map[string]string{}}
	client := pluginCredentialDaemon(t, sink, store)
	ctx := context.Background()

	defs, err := client.ListConnectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	catalog := CredentialCatalog(defs)
	if len(catalog) != 1 || catalog[0].ID != "leaky" || len(catalog[0].Secrets) != 1 ||
		catalog[0].Secrets[0].Name != "token" || catalog[0].Secrets[0].Kind != "credential" {
		t.Fatalf("catalog = %+v, want the plugin's one declared secret", catalog)
	}

	result, err := client.ConsoleWrite(ctx, ConsoleWriteRequest{Operation: ConsoleProviderSave, ID: "leaky", Secrets: map[string]string{"token": "tok-1"}})
	if err != nil || !result.SecretsChanged || store.values["leaky/token"] != "tok-1" {
		t.Fatalf("a declared secret: %+v %v %v", result, err, store.values)
	}

	for name, tc := range map[string]struct {
		req  ConsoleWriteRequest
		want []string
	}{
		"undeclared key": {ConsoleWriteRequest{Operation: ConsoleProviderSave, ID: "leaky", Secrets: map[string]string{"token": "tok-2", "api_key": "k"}},
			[]string{`connector "leaky" does not declare the secrets api_key (it declares token)`}},
		"undeclared clear": {ConsoleWriteRequest{Operation: ConsoleProviderSave, ID: "leaky", ClearSecrets: []string{"password"}},
			[]string{"does not declare the secrets password"}},
		"undeclared id": {ConsoleWriteRequest{Operation: ConsoleProviderSave, ID: "vercel", Secrets: map[string]string{"token": "t"}},
			[]string{`no installed connector "vercel" declares a credential`, "install the plugin first"}},
		"settings": {ConsoleWriteRequest{Operation: ConsoleProviderSave, ID: "leaky", Values: map[string]string{"scope": "team"}},
			[]string{"no longer saves settings", "connector-config.yaml"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := client.ConsoleWrite(ctx, tc.req)
			if status, _ := DaemonHTTPStatus(err); status != http.StatusBadRequest {
				t.Fatalf("status %d, err %v; want 400", status, err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not say %q", err.Error(), want)
				}
			}
			// The recovery instruction reaches the operator as written.
			if got := redact.ErrorText(err); got != err.Error() {
				t.Errorf("redaction rewrote the refusal: %q", got)
			}
			if _, perr := client.PlanConsoleWrite(ctx, tc.req); perr == nil {
				t.Error("the plan of a refused write was served")
			}
		})
	}
	if store.values["leaky/token"] != "tok-1" || len(store.values) != 1 {
		t.Fatalf("a refused save changed the store: %v", store.values)
	}
}

// A daemon with no connector catalog writes no credential: it fails closed.
func TestProviderSaveWithoutACatalogRefuses(t *testing.T) {
	store := &consoleSecrets{values: map[string]string{}}
	client := NewInProcessClient(WithConfigPath(filepath.Join(t.TempDir(), "config.yaml")), WithConsoleSecretStore(store))
	_, err := client.ConsoleWrite(context.Background(), ConsoleWriteRequest{Operation: ConsoleProviderSave, ID: "cloudflare", Secrets: map[string]string{"api_token": "t"}})
	var input ConsoleWriteInputError
	if !errors.As(err, &input) || len(store.values) != 0 {
		t.Fatalf("err %v, store %v", err, store.values)
	}
}

// The declared-secrets exemption covers names only, and only in Cerberus's
// own schema. Through the response writer every socket handler answers with,
// a definition's requirement names, kinds and envs arrive intact with no
// description beside them, while a credential the request resolved is
// removed even from a name, and a credential-shaped sibling stays hidden. A
// "secrets" list in an operation's result gets the ordinary walk.
func TestDeclaredSecretNamesAreExemptAndValuesAreNot(t *testing.T) {
	const resolved = "tok-resolved-sentinel-5e81a7" //nolint:gosec // a test sentinel, not a credential
	write := func(body any) string {
		rec := httptest.NewRecorder()
		w, r := BeginHTTPRequest(rec, httptest.NewRequest(http.MethodGet, "/connectors", nil), SurfaceSocket)
		redact.ScopeFrom(r.Context()).Add("leaky/token", resolved)
		writeJSON(w, http.StatusOK, body)
		return rec.Body.String()
	}
	body := write([]contract.Definition{{ID: "leaky", Config: contract.ConfigSchema{Secrets: []contract.SecretRequirement{
		{Name: "token", Env: "CERBERUS_LEAKY_TOKEN"},
		{Name: "api_user", Kind: contract.SecretKindName},
		{Name: resolved},
	}}}})
	for _, want := range []string{`"name":"token"`, `"env":"CERBERUS_LEAKY_TOKEN"`, `"name":"api_user"`, `"kind":"name"`} {
		if !strings.Contains(body, want) {
			t.Errorf("a declared name was redacted; want %s in\n%s", want, body)
		}
	}
	if strings.Contains(body, resolved) {
		t.Errorf("a resolved credential reached the response:\n%s", body)
	}
	sibling := write([]map[string]any{{"id": "x", "operations": []any{}, "config": map[string]any{
		"secrets": []map[string]any{{"name": "db", "description": "d", "password": "plainpw123"}}}}})
	if strings.Contains(sibling, "plainpw123") || !strings.Contains(sibling, `"name":"db"`) {
		t.Errorf("a credential-shaped sibling of a declared secret: %s", sibling)
	}
	result := write(ExternalConnectorOperationResult{Connector: "p", Operation: "o", Data: map[string]any{
		"secrets": []map[string]any{{"name": "ghp_tokenShaped123456"}}}})
	if strings.Contains(result, "ghp_tokenShaped123456") {
		t.Errorf("a secrets list in an operation's result was exempt: %s", result)
	}
}

// A secret a connector resolves per resource (ssh's key, read as
// ssh/<resource-id>/key) is not offered by the editor, and provider_save
// refuses it with the command that does set it: saved as ssh/key, nothing
// would read it.
func TestPerResourceSecretsAreNotEditable(t *testing.T) {
	defs := []contract.Definition{sshconn.Definition(), {ID: "cloudflare", Config: contract.ConfigSchema{Secrets: []contract.SecretRequirement{{Name: "api_token"}}}}}
	catalog := CredentialCatalog(defs)
	if len(catalog) != 1 || catalog[0].ID != "cloudflare" {
		t.Fatalf("catalog = %+v, want cloudflare alone", catalog)
	}
	registry := connector.NewRegistry()
	for _, def := range defs {
		registry.RegisterDefinition(def)
	}
	withPDP(t, constantPDP{decision: policy.Allow})
	store := &consoleSecrets{values: map[string]string{}}
	sink := audit.NewMemory()
	client := NewInProcessClient(WithConfigPath(filepath.Join(t.TempDir(), "config.yaml")), WithConsoleSecretStore(store),
		WithExternalConnectorService(NewExternalConnectorService(sink, registry)))
	_, err := client.ConsoleWrite(context.Background(), ConsoleWriteRequest{Operation: ConsoleProviderSave, ID: "ssh", Secrets: map[string]string{"key": "/Users/me/.ssh/id_ed25519"}})
	var input ConsoleWriteInputError
	if !errors.As(err, &input) || !strings.Contains(err.Error(), "cerberus secrets set ssh/<resource-id>/key") || len(store.values) != 0 {
		t.Fatalf("err %v, store %v", err, store.values)
	}
	if got := redact.ErrorText(err); got != err.Error() {
		t.Fatalf("redaction rewrote the refusal: %q", got)
	}
}
