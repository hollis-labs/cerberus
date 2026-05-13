package connector

import (
	"sort"
	"sync"

	contract "github.com/chrispian/cerberus/pkg/connector"
)

// Registry holds all registered connectors, keyed by their ID.
type Registry struct {
	mu          sync.RWMutex
	connectors  map[string]contract.Connector
	definitions map[string]contract.Definition
	unavailable map[string]error
}

// NewRegistry creates an empty connector registry.
func NewRegistry() *Registry {
	return &Registry{
		connectors:  make(map[string]contract.Connector),
		definitions: make(map[string]contract.Definition),
		unavailable: make(map[string]error),
	}
}

// Register adds a connector to the registry. If a connector with the same ID
// already exists, it is replaced.
func (r *Registry) Register(c contract.Connector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connectors[c.ID()] = c
	delete(r.unavailable, c.ID())
	if describer, ok := c.(contract.Describer); ok {
		def := describer.Definition()
		r.definitions[def.ID] = def
	}
}

// RegisterDefinition adds discovery metadata for a connector that may not be
// currently available as a live connector instance.
func (r *Registry) RegisterDefinition(def contract.Definition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.definitions[def.ID] = def
}

// RegisterUnavailable records why a connector with registered metadata is not
// available as a live connector instance.
func (r *Registry) RegisterUnavailable(id string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.unavailable[id] = err
	}
}

// UnavailableError returns the recorded construction error for a connector.
func (r *Registry) UnavailableError(id string) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.unavailable[id]
}

// Get returns the connector with the given ID, or nil and false if not found.
func (r *Registry) Get(id string) (contract.Connector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.connectors[id]
	return c, ok
}

// List returns all registered connectors.
func (r *Registry) List() []contract.Connector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]contract.Connector, 0, len(r.connectors))
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

// Definitions returns discovery metadata for connectors that expose it.
func (r *Registry) Definitions() []contract.Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]contract.Definition, 0, len(r.definitions))
	for _, def := range r.definitions {
		out = append(out, def)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}
