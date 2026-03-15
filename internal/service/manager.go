package service

import (
	"fmt"
	"sync"
)

// BulkStopThreshold is the number of running services above which a bulk stop
// operation will log a warning. This guards against accidental mass termination
// when the caller did not explicitly request a full shutdown.
const BulkStopThreshold = 2

// ServiceManager orchestrates service lifecycle using dependency ordering from the DAG.
type ServiceManager struct {
	services []*ManagedService
	dag      *DAG
	byID     map[string]*ManagedService
}

// NewServiceManager creates a ServiceManager and builds the dependency DAG from the provided services.
func NewServiceManager(services []*ManagedService) (*ServiceManager, error) {
	dag, err := BuildDAG(services)
	if err != nil {
		return nil, fmt.Errorf("build dependency graph: %w", err)
	}

	byID := make(map[string]*ManagedService, len(services))
	for _, svc := range services {
		byID[svc.Def.ID] = svc
	}

	return &ServiceManager{
		services: services,
		dag:      dag,
		byID:     byID,
	}, nil
}

// DAG returns the underlying dependency graph.
func (m *ServiceManager) DAG() *DAG {
	return m.dag
}

// Services returns all managed services.
func (m *ServiceManager) Services() []*ManagedService {
	return m.services
}

// StartAll starts all services in topological order, parallelizing within each level.
// Services in level 0 (no dependencies) start first, then level 1, etc.
func (m *ServiceManager) StartAll() []error {
	levels, err := m.dag.TopologicalOrder()
	if err != nil {
		return []error{err}
	}

	var allErrors []error
	for _, level := range levels {
		errs := m.startLevel(level)
		allErrors = append(allErrors, errs...)
	}
	return allErrors
}

// startLevel starts all services in a level concurrently using goroutines.
func (m *ServiceManager) startLevel(services []*ManagedService) []error {
	if len(services) == 0 {
		return nil
	}
	if len(services) == 1 {
		if err := services[0].Start(); err != nil {
			return []error{fmt.Errorf("start %s: %w", services[0].Def.ID, err)}
		}
		return nil
	}

	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)

	wg.Add(len(services))
	for _, svc := range services {
		go func(s *ManagedService) {
			defer wg.Done()
			if err := s.Start(); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("start %s: %w", s.Def.ID, err))
				mu.Unlock()
			}
		}(svc)
	}
	wg.Wait()

	return errs
}

// StartService starts a single service by ID. If autoStartDeps is true,
// any unstarted dependencies are started first (recursively, in correct order).
func (m *ServiceManager) StartService(id string, autoStartDeps bool) []error {
	svc, ok := m.byID[id]
	if !ok {
		return []error{fmt.Errorf("unknown service: %s", id)}
	}

	if !autoStartDeps {
		if err := svc.Start(); err != nil {
			return []error{fmt.Errorf("start %s: %w", id, err)}
		}
		return nil
	}

	// Collect all transitive dependencies in topological order.
	needed := m.collectDeps(id)

	// Filter to only services that are not already running.
	var toStart []*ManagedService
	for _, s := range needed {
		if s.Status != StatusRunning && s.Status != StatusHealthy && s.Status != StatusUnhealthy {
			toStart = append(toStart, s)
		}
	}

	// Build a sub-DAG from the needed services to get proper level ordering.
	if len(toStart) == 0 {
		// All deps running — just start the target.
		if svc.Status != StatusRunning {
			if err := svc.Start(); err != nil {
				return []error{fmt.Errorf("start %s: %w", id, err)}
			}
		}
		return nil
	}

	// Include the target service itself.
	if svc.Status != StatusRunning {
		toStart = append(toStart, svc)
	}

	subDAG, err := BuildDAG(toStart)
	if err != nil {
		return []error{err}
	}

	levels, err := subDAG.TopologicalOrder()
	if err != nil {
		return []error{err}
	}

	var allErrors []error
	for _, level := range levels {
		errs := m.startLevel(level)
		allErrors = append(allErrors, errs...)
	}
	return allErrors
}

// collectDeps returns all transitive dependencies of the given service (not
// including the service itself), in no particular order.
func (m *ServiceManager) collectDeps(id string) []*ManagedService {
	visited := make(map[string]bool)
	var result []*ManagedService

	var walk func(string)
	walk = func(current string) {
		for _, dep := range m.dag.DependenciesOf(current) {
			if !visited[dep.Def.ID] {
				visited[dep.Def.ID] = true
				walk(dep.Def.ID)
				result = append(result, dep)
			}
		}
	}
	walk(id)
	return result
}

// StopAll stops all services in reverse topological order (dependents first,
// then their dependencies). Services within a level are stopped concurrently.
// This is the explicit "stop everything" path and always proceeds.
func (m *ServiceManager) StopAll() []error {
	levels, err := m.dag.TopologicalOrder()
	if err != nil {
		return []error{err}
	}

	// Reverse the levels so dependents stop before their dependencies.
	var allErrors []error
	for i := len(levels) - 1; i >= 0; i-- {
		errs := m.stopLevel(levels[i])
		allErrors = append(allErrors, errs...)
	}
	return allErrors
}

// StopSubset stops the given services in reverse dependency order. If the
// number of running services to stop exceeds BulkStopThreshold and
// explicitBulk is false, a warning is logged. This guards against accidental
// mass termination from automated callers.
func (m *ServiceManager) StopSubset(targets []*ManagedService, explicitBulk bool) []error {
	// Count how many of the targets are actually running.
	var running int
	for _, svc := range targets {
		svc.Poll()
		if svc.Status == StatusRunning || svc.Status == StatusHealthy || svc.Status == StatusUnhealthy {
			running++
		}
	}

	if running > BulkStopThreshold && !explicitBulk {
		llog().Warn("lifecycle.bulk_stop_guard",
			"running_count", running,
			"threshold", BulkStopThreshold,
			"message", fmt.Sprintf("Stopping %d running services at once — this exceeds the bulk threshold of %d. If this is intentional, use explicit bulk stop.", running, BulkStopThreshold),
		)
	}

	// Build a sub-DAG for proper reverse ordering.
	subDAG, err := BuildDAG(targets)
	if err != nil {
		// Fallback: stop sequentially without ordering.
		var errs []error
		for _, svc := range targets {
			if stopErr := svc.Stop(); stopErr != nil {
				errs = append(errs, fmt.Errorf("stop %s: %w", svc.Def.ID, stopErr))
			}
		}
		return errs
	}

	levels, err := subDAG.TopologicalOrder()
	if err != nil {
		return []error{err}
	}

	// Reverse order: dependents first.
	var allErrors []error
	for i := len(levels) - 1; i >= 0; i-- {
		errs := m.stopLevel(levels[i])
		allErrors = append(allErrors, errs...)
	}
	return allErrors
}

// stopLevel stops all services in a level concurrently.
func (m *ServiceManager) stopLevel(services []*ManagedService) []error {
	if len(services) == 0 {
		return nil
	}
	if len(services) == 1 {
		if err := services[0].Stop(); err != nil {
			return []error{fmt.Errorf("stop %s: %w", services[0].Def.ID, err)}
		}
		return nil
	}

	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)

	wg.Add(len(services))
	for _, svc := range services {
		go func(s *ManagedService) {
			defer wg.Done()
			if err := s.Stop(); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("stop %s: %w", s.Def.ID, err))
				mu.Unlock()
			}
		}(svc)
	}
	wg.Wait()

	return errs
}
