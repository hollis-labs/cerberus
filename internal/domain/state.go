package domain

import "github.com/chrispian/cerberus/pkg/resource"

// State represents the current known state of a Resource.
type State = resource.State

const (
	StateStopped   = resource.StateStopped
	StateStarting  = resource.StateStarting
	StateRunning   = resource.StateRunning
	StateHealthy   = resource.StateHealthy
	StateUnhealthy = resource.StateUnhealthy
	StateBuilding  = resource.StateBuilding
	StateFailed    = resource.StateFailed
	StateDestroyed = resource.StateDestroyed
	StateUnknown   = resource.StateUnknown
)
