package connector

import (
	"context"
	"errors"
	"testing"

	"github.com/chrispian/cerberus/internal/domain"
	contract "github.com/chrispian/cerberus/pkg/connector"
)

// stubConnector is a minimal Connector for testing the registry.
type stubConnector struct {
	id    string
	types []string
}

func (s *stubConnector) ID() string                                          { return s.id }
func (s *stubConnector) ResourceTypes() []string                             { return s.types }
func (s *stubConnector) Create(_ context.Context, _ *domain.Resource) error  { return nil }
func (s *stubConnector) Start(_ context.Context, _ *domain.Resource) error   { return nil }
func (s *stubConnector) Stop(_ context.Context, _ *domain.Resource) error    { return nil }
func (s *stubConnector) Destroy(_ context.Context, _ *domain.Resource) error { return nil }
func (s *stubConnector) Status(_ context.Context, _ *domain.Resource) (domain.State, error) {
	return domain.StateRunning, nil
}
func (s *stubConnector) Capabilities() domain.ConnectorCapabilities {
	return domain.ConnectorCapabilities{}
}

type describingStubConnector struct {
	stubConnector
}

func (s *describingStubConnector) Definition() contract.Definition {
	return contract.Definition{
		ID:            s.ID(),
		ResourceTypes: s.ResourceTypes(),
	}
}

func TestRegistryRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	c := &stubConnector{id: "local", types: []string{"process"}}
	r.Register(c)

	got, ok := r.Get("local")
	if !ok {
		t.Fatal("expected to find connector 'local'")
	}
	if got.ID() != "local" {
		t.Errorf("got ID %q, want %q", got.ID(), "local")
	}
}

func TestRegistryGetMissing(t *testing.T) {
	r := NewRegistry()
	_, ok := r.Get("nonexistent")
	if ok {
		t.Error("expected not to find nonexistent connector")
	}
}

func TestRegistryList(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubConnector{id: "local", types: []string{"process"}})
	r.Register(&stubConnector{id: "digitalocean", types: []string{"server"}})

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("got %d connectors, want 2", len(list))
	}
}

func TestRegistryReplacesDuplicate(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubConnector{id: "local", types: []string{"process"}})
	r.Register(&stubConnector{id: "local", types: []string{"process", "container"}})

	got, ok := r.Get("local")
	if !ok {
		t.Fatal("expected to find connector 'local'")
	}
	if len(got.ResourceTypes()) != 2 {
		t.Errorf("got %d resource types, want 2 (replaced connector)", len(got.ResourceTypes()))
	}
}

func TestRegistryIDs(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubConnector{id: "local"})
	r.Register(&stubConnector{id: "docker"})

	ids := r.IDs()
	if len(ids) != 2 {
		t.Fatalf("got %d IDs, want 2", len(ids))
	}
}

func TestRegistryDefinitionsReturnsDescribingConnectorsSorted(t *testing.T) {
	r := NewRegistry()
	r.Register(&describingStubConnector{stubConnector: stubConnector{id: "github", types: []string{"repository"}}})
	r.Register(&stubConnector{id: "local", types: []string{"process"}})
	r.Register(&describingStubConnector{stubConnector: stubConnector{id: "docker", types: []string{"container"}}})

	defs := r.Definitions()
	if len(defs) != 2 {
		t.Fatalf("got %d definitions, want 2", len(defs))
	}
	if defs[0].ID != "docker" || defs[1].ID != "github" {
		t.Fatalf("definition order = [%s %s], want [docker github]", defs[0].ID, defs[1].ID)
	}
}

func TestRegistryDefinitionsIncludeStaticDefinitions(t *testing.T) {
	r := NewRegistry()
	r.RegisterDefinition(contract.Definition{ID: "github", ResourceTypes: []string{"repository"}})
	r.Register(&describingStubConnector{stubConnector: stubConnector{id: "docker", types: []string{"container"}}})

	defs := r.Definitions()
	if len(defs) != 2 {
		t.Fatalf("got %d definitions, want 2", len(defs))
	}
	if defs[0].ID != "docker" || defs[1].ID != "github" {
		t.Fatalf("definition order = [%s %s], want [docker github]", defs[0].ID, defs[1].ID)
	}
}

func TestRegistryUnavailableErrorClearsOnRegister(t *testing.T) {
	r := NewRegistry()
	unavailable := errors.New("missing token")
	r.RegisterUnavailable("github", unavailable)
	if got := r.UnavailableError("github"); !errors.Is(got, unavailable) {
		t.Fatalf("UnavailableError = %v, want %v", got, unavailable)
	}

	r.Register(&stubConnector{id: "github"})
	if got := r.UnavailableError("github"); got != nil {
		t.Fatalf("UnavailableError after Register = %v, want nil", got)
	}
}

func TestConnectorFactoryDoesNotCacheMissingOrRotatedCredentials(t *testing.T) {
	registry := NewRegistry()
	var available contract.Connector
	registry.RegisterFactory(contract.Definition{ID: "late"}, func(ctx context.Context) (contract.Connector, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if available == nil {
			return nil, errors.New("credential missing")
		}
		return available, nil
	})
	if _, err := registry.Resolve(context.Background(), "late"); err == nil {
		t.Fatal("missing credential accepted")
	}
	available = &stubConnector{id: "late"}
	first, err := registry.Resolve(context.Background(), "late")
	if err != nil || first != available {
		t.Fatalf("credential added after startup not picked up: %v", err)
	}
	available = &stubConnector{id: "late"}
	second, err := registry.Resolve(context.Background(), "late")
	if err != nil || second != available || second == first {
		t.Fatalf("rotated credential not picked up: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = registry.Resolve(ctx, "late"); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost operation context: %v", err)
	}
}
