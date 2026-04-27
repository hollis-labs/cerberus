package local

import (
	"context"
	"fmt"
	"sync"

	"github.com/chrispian/cerberus/internal/domain"
	"github.com/chrispian/cerberus/internal/service"
)

// Connector manages local OS processes. It wraps the existing service.ManagedService
// code behind the domain.Connector interface.
type Connector struct {
	mu       sync.RWMutex
	services map[string]*service.ManagedService
	dev      runtimeBackend
	service  runtimeBackend
}

// New creates a local connector.
func New() *Connector {
	return &Connector{
		services: make(map[string]*service.ManagedService),
		dev:      devSessionBackend{},
		service:  newOSServiceBackend(),
	}
}

func (c *Connector) ID() string              { return "local" }
func (c *Connector) ResourceTypes() []string { return []string{"process"} }

func (c *Connector) Capabilities() domain.ConnectorCapabilities {
	return domain.ConnectorCapabilities{
		CanCreate:  false, // local processes aren't "created" — they're started
		CanDestroy: true,
		CanBuild:   true,
		CanLogs:    true,
		CanHealth:  true,
	}
}

// getOrCreate returns the ManagedService for a resource, creating it if needed.
func (c *Connector) getOrCreate(res *domain.Resource) *service.ManagedService {
	c.mu.Lock()
	defer c.mu.Unlock()

	if svc, ok := c.services[res.ID]; ok {
		return svc
	}

	def := ResourceToServiceDef(res)
	svc := &service.ManagedService{Def: def}
	c.services[res.ID] = svc
	return svc
}

// Get returns the ManagedService for a resource ID, if it exists.
func (c *Connector) Get(id string) (*service.ManagedService, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	svc, ok := c.services[id]
	return svc, ok
}

// All returns all tracked ManagedServices.
func (c *Connector) All() []*service.ManagedService {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*service.ManagedService, 0, len(c.services))
	for _, svc := range c.services {
		out = append(out, svc)
	}
	return out
}

// Register adds a ManagedService directly (used during initialization
// from config to preserve the existing startup path).
func (c *Connector) Register(svc *service.ManagedService) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.services[svc.Def.ID] = svc
}

func (c *Connector) Create(_ context.Context, _ *domain.Resource) error {
	return fmt.Errorf("local connector does not support Create — use Start instead")
}

func (c *Connector) Start(ctx context.Context, res *domain.Resource) error {
	_, err := c.Apply(ctx, res)
	return err
}

func (c *Connector) Apply(ctx context.Context, res *domain.Resource) (ApplyResult, error) {
	backend, spec, svc, err := c.runtimeFor(res)
	if err != nil {
		return ApplyResult{}, err
	}
	return backend.Apply(ctx, res, spec, svc)
}

func (c *Connector) Reload(ctx context.Context, res *domain.Resource) error {
	backend, spec, svc, err := c.runtimeFor(res)
	if err != nil {
		return err
	}
	return backend.Reload(ctx, res, spec, svc)
}

func (c *Connector) Stop(ctx context.Context, res *domain.Resource) error {
	backend, spec, svc, err := c.runtimeFor(res)
	if err != nil {
		return err
	}
	return backend.Stop(ctx, res, spec, svc)
}

func (c *Connector) Destroy(ctx context.Context, res *domain.Resource) error {
	backend, spec, svc, err := c.runtimeFor(res)
	if err != nil {
		return err
	}
	return backend.Destroy(ctx, res, spec, svc)
}

func (c *Connector) Status(ctx context.Context, res *domain.Resource) (domain.State, error) {
	backend, spec, svc, err := c.runtimeFor(res)
	if err != nil {
		return domain.StateUnknown, err
	}
	return backend.Status(ctx, res, spec, svc)
}

func (c *Connector) runtimeFor(res *domain.Resource) (runtimeBackend, ProcessSpec, *service.ManagedService, error) {
	spec, err := SpecFromResourceConfig(res.Config)
	if err != nil {
		return nil, ProcessSpec{}, nil, fmt.Errorf("decode process spec for %q: %w", res.ID, err)
	}

	switch spec.Mode {
	case "", ProcessModeDevSession:
		return c.dev, spec, c.getOrCreate(res), nil
	case ProcessModeOSService:
		return c.service, spec, nil, nil
	default:
		return nil, ProcessSpec{}, nil, fmt.Errorf("unsupported process mode %q for resource %q", spec.Mode, res.ID)
	}
}

// mapStatus converts the existing service.Status enum to the domain.State enum.
func mapStatus(s service.Status) domain.State {
	switch s {
	case service.StatusRunning:
		return domain.StateRunning
	case service.StatusHealthy:
		return domain.StateHealthy
	case service.StatusUnhealthy:
		return domain.StateUnhealthy
	case service.StatusStarting:
		return domain.StateStarting
	case service.StatusBuilding:
		return domain.StateBuilding
	case service.StatusFailed:
		return domain.StateFailed
	default:
		return domain.StateStopped
	}
}
