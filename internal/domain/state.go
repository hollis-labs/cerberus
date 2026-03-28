package domain

// State represents the current known state of a Resource.
type State string

const (
	StateStopped   State = "stopped"
	StateStarting  State = "starting"
	StateRunning   State = "running"
	StateHealthy   State = "healthy"
	StateUnhealthy State = "unhealthy"
	StateBuilding  State = "building"
	StateFailed    State = "failed"
	StateDestroyed State = "destroyed"
	StateUnknown   State = "unknown"
)
