package plugin

import (
	"strings"
	"testing"
)

func TestSecretBackendValidation(t *testing.T) {
	for _, ok := range []SecretBackend{
		{Scheme: "op", Reference: "op://<vault>/<item>/<field>"},
		{Scheme: "keeper"},
		{Scheme: "vault-kv"},
	} {
		if problems := ok.Validate(); len(problems) != 0 {
			t.Errorf("%+v: %v", ok, problems)
		}
	}
	for _, bad := range []SecretBackend{
		{Scheme: ""},
		{Scheme: "o"},
		{Scheme: "OP"},
		{Scheme: "op://"},
		{Scheme: "keychain"},
		{Scheme: "keyring"},
		{Scheme: "helper"},
		{Scheme: "https"},
		{Scheme: "op", Reference: "keeper://x"},
	} {
		if problems := bad.Validate(); len(problems) == 0 {
			t.Errorf("%+v was accepted", bad)
		}
	}
}

// The declaration is validated with the rest of the cerberus block.
func TestPluginYAMLValidatesItsSecretBackend(t *testing.T) {
	block := CerberusPluginBlock{SecretBackend: &SecretBackend{Scheme: "keychain"}}
	problems := strings.Join(block.validateDeclarations(), "; ")
	if !strings.Contains(problems, "reserved") {
		t.Fatalf("a reserved scheme passed: %q", problems)
	}
}

func TestResolveFailureReadsTheCodedPayload(t *testing.T) {
	content := string(ErrorResult(ErrorCredentialMissing, "store the token").Content)
	code, msg, ok := ResolveFailure(content)
	if !ok || code != ErrorCredentialMissing || msg != "store the token" {
		t.Fatalf("got %q %q %v", code, msg, ok)
	}
	if _, _, ok := ResolveFailure("plain text"); ok {
		t.Fatal("plain text read as a coded payload")
	}
}
