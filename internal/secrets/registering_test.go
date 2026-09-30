package secrets

import (
	"context"
	"errors"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/pkg/secret"
)

const resolvedSentinel = "q7Zr2mXv9pLw" //nolint:gosec // a test sentinel, not a credential

type mapProvider struct {
	values map[string]string
	err    error
}

func (m *mapProvider) Get(_ context.Context, service, key string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.values[service+"/"+key], nil
}

func TestRegisteringProviderRegistersWhatItResolves(t *testing.T) {
	base := &mapProvider{values: map[string]string{"github/token": resolvedSentinel, "ssh/box/key": "/Users/op/.ssh/id_ed25519"}}
	p := Registering(base, func(service, key string) bool { return service == "ssh/box" && key == "key" })
	ctx, scope := redact.EnsureScope(context.Background())

	if got, err := p.Get(ctx, "github", "token"); err != nil || got != resolvedSentinel {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if got := scope.Text("401: " + resolvedSentinel + " is not valid"); got != "401: "+redact.Marker+" is not valid" {
		t.Fatalf("resolved value not registered: %q", got)
	}

	// A path is guidance, not a credential: it stays in the error that
	// needs it.
	path, _ := p.Get(ctx, "ssh/box", "key")
	msg := "reading SSH key " + path + ": no such file or directory"
	if got := scope.Text(msg); got != msg {
		t.Fatalf("a path secret was registered: %q", got)
	}
}

func TestRegisteringProviderWithoutAScopeOnlyResolves(t *testing.T) {
	p := Registering(&mapProvider{values: map[string]string{"github/token": resolvedSentinel}}, nil)
	if got, err := p.Get(context.Background(), "github", "token"); err != nil || got != resolvedSentinel {
		t.Fatalf("Get = %q, %v", got, err)
	}
}

func TestRegisteringProviderPassesFailures(t *testing.T) {
	base := &mapProvider{err: errors.New("keychain locked")}
	p := Registering(base, nil)
	ctx, scope := redact.EnsureScope(context.Background())
	if _, err := p.Get(ctx, "github", "token"); err == nil || err.Error() != "keychain locked" {
		t.Fatalf("err = %v, want the provider's", err)
	}
	if scope.String() != "redact.Scope(0 values)" {
		t.Fatalf("a failed lookup registered something: %s", scope)
	}
	if _, writable := p.(secret.ReadWriter); writable {
		t.Fatal("the registering wrapper exposes a writer; resolution is read-only")
	}
	if Registering(nil, nil) != nil {
		t.Fatal("Registering(nil) should be nil, so a missing provider stays missing")
	}
}
