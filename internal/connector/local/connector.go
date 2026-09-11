package local

import (
	"context"
	"fmt"
	"sync"

	"github.com/chrispian/cerberus/internal/domain"
)

// Connector manages local OS processes through the v2 local runtime backends.
type Connector struct {
	mu            sync.RWMutex
	sessions      map[string]*devSession
	dev           runtimeBackend
	service       runtimeBackend
	mutationGuard func(*domain.Resource, ProcessSpec) error
}

// New creates a local connector.
func New() *Connector {
	return &Connector{
		sessions: make(map[string]*devSession),
		dev:      devSessionBackend{},
		service:  newOSServiceBackend(),
	}
}

// SetMutationGuard installs the serving runtime's policy for all local callers,
// including pipelines that use the connector without a transport client.
func (c *Connector) SetMutationGuard(guard func(*domain.Resource, ProcessSpec) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mutationGuard = guard
}

func (c *Connector) ValidateMutation(res *domain.Resource) error {
	spec, err := SpecFromResourceConfig(res.Config)
	if err != nil {
		return err
	}
	c.mu.RLock()
	guard := c.mutationGuard
	c.mu.RUnlock()
	if guard != nil {
		return guard(res, spec)
	}
	return nil
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

func (c *Connector) getOrCreateSession(res *domain.Resource, spec ProcessSpec) *devSession {
	c.mu.Lock()
	defer c.mu.Unlock()

	if session, ok := c.sessions[res.ID]; ok {
		return session
	}

	session := newDevSession(res, spec)
	c.sessions[res.ID] = session
	return session
}

func (c *Connector) Create(_ context.Context, _ *domain.Resource) error {
	return fmt.Errorf("local connector does not support Create — use Start instead")
}

func (c *Connector) Start(ctx context.Context, res *domain.Resource) error {
	_, err := c.Apply(ctx, res)
	return err
}

func (c *Connector) Apply(ctx context.Context, res *domain.Resource) (ApplyResult, error) {
	if err := c.ValidateMutation(res); err != nil {
		return ApplyResult{}, err
	}
	backend, spec, svc, err := c.runtimeFor(res)
	if err != nil {
		return ApplyResult{}, err
	}
	return backend.Apply(ctx, res, spec, svc)
}

func (c *Connector) Reload(ctx context.Context, res *domain.Resource) error {
	if err := c.ValidateMutation(res); err != nil {
		return err
	}
	backend, spec, svc, err := c.runtimeFor(res)
	if err != nil {
		return err
	}
	return backend.Reload(ctx, res, spec, svc)
}

func (c *Connector) Stop(ctx context.Context, res *domain.Resource) error {
	if err := c.ValidateMutation(res); err != nil {
		return err
	}
	backend, spec, svc, err := c.runtimeFor(res)
	if err != nil {
		return err
	}
	return backend.Stop(ctx, res, spec, svc)
}

func (c *Connector) Destroy(ctx context.Context, res *domain.Resource) error {
	if err := c.ValidateMutation(res); err != nil {
		return err
	}
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

func (c *Connector) runtimeFor(res *domain.Resource) (runtimeBackend, ProcessSpec, *devSession, error) {
	spec, err := SpecFromResourceConfig(res.Config)
	if err != nil {
		return nil, ProcessSpec{}, nil, fmt.Errorf("decode process spec for %q: %w", res.ID, err)
	}

	switch spec.Mode {
	case "", ProcessModeDevSession:
		return c.dev, spec, c.getOrCreateSession(res, spec), nil
	case ProcessModeOSService:
		return c.service, spec, nil, nil
	default:
		return nil, ProcessSpec{}, nil, fmt.Errorf("unsupported process mode %q for resource %q", spec.Mode, res.ID)
	}
}

// ActivateBuilt restarts a dev session after a build; Apply alone only launches
// its command. OS services already compare and activate installed artifacts.
func (c *Connector) ActivateBuilt(ctx context.Context, res *domain.Resource) (ApplyResult, error) {
	if err := c.ValidateMutation(res); err != nil {
		return ApplyResult{}, err
	}
	backend, spec, session, err := c.runtimeFor(res)
	if err != nil {
		return ApplyResult{}, err
	}
	if session == nil {
		return backend.Apply(ctx, res, spec, nil)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	session.Update(res, spec)
	state := session.Poll()
	if state == domain.StateUnknown {
		return ApplyResult{}, fmt.Errorf("%s", session.errMsg)
	}
	action := ApplyActionStarted
	if isActiveState(state) {
		if err := session.stopContext(ctx); err != nil {
			return ApplyResult{}, err
		}
		action = ApplyActionRestarted
	}
	if err := ctx.Err(); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{Action: action}, session.Start()
}
