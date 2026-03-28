package connector

import (
	"context"
	"testing"

	"github.com/chrispian/cerberus/internal/domain"
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
