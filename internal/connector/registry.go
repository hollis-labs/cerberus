package connector

import (
	"sync"

	"github.com/chrispian/cerberus/internal/domain"
)

// Registry holds all registered connectors, keyed by their ID.
type Registry struct {
	mu         sync.RWMutex
	connectors map[string]domain.Connector
}

// NewRegistry creates an empty connector registry.
func NewRegistry() *Registry {
	return &Registry{
		connectors: make(map[string]domain.Connector),
	}
}

// Register adds a connector to the registry. If a connector with the same ID
// already exists, it is replaced.
func (r *Registry) Register(c domain.Connector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connectors[c.ID()] = c
}

// Get returns the connector with the given ID, or nil and false if not found.
func (r *Registry) Get(id string) (domain.Connector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.connectors[id]
	return c, ok
}

// List returns all registered connectors.
func (r *Registry) List() []domain.Connector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]domain.Connector, 0, len(r.connectors))
	for _, c := range r.connectors {
		out = append(out, c)
	}
	return out
}

// IDs returns the IDs of all registered connectors.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.connectors))
	for id := range r.connectors {
		out = append(out, id)
	}
	return out
}
