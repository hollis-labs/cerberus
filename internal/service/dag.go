package service

import (
	"fmt"
	"strings"
)

// DAG represents a directed acyclic graph of service dependencies.
type DAG struct {
	services map[string]*Service
	// edges maps a service ID to the IDs it depends on (prerequisites).
	edges map[string][]string
	// reverse maps a service ID to IDs that depend on it (dependents).
	reverse map[string][]string
}

// BuildDAG constructs a DAG from a slice of services using their DependsOn fields.
// It returns an error if any dependency references an unknown service ID or if
// there is a circular dependency.
func BuildDAG(services []*Service) (*DAG, error) {
	d := &DAG{
		services: make(map[string]*Service, len(services)),
		edges:    make(map[string][]string, len(services)),
		reverse:  make(map[string][]string, len(services)),
	}

	// Index services by ID.
	for _, svc := range services {
		if svc.Def.ID == "" {
			return nil, fmt.Errorf("service has empty ID")
		}
		d.services[svc.Def.ID] = svc
	}

	// Build edge maps.
	for _, svc := range services {
		for _, dep := range svc.Def.DependsOn {
			if _, ok := d.services[dep]; !ok {
				return nil, fmt.Errorf("service %q depends on unknown service %q", svc.Def.ID, dep)
			}
			d.edges[svc.Def.ID] = append(d.edges[svc.Def.ID], dep)
			d.reverse[dep] = append(d.reverse[dep], svc.Def.ID)
		}
	}

	// Validate: detect cycles using DFS.
	if cycle := d.detectCycle(); cycle != nil {
		return nil, fmt.Errorf("circular dependency detected: %s", strings.Join(cycle, " -> "))
	}

	return d, nil
}

// detectCycle performs a DFS to find a cycle. Returns the cycle path if found,
// or nil if the graph is acyclic.
func (d *DAG) detectCycle() []string {
	const (
		white = 0 // unvisited
		gray  = 1 // in current path
		black = 2 // fully processed
	)

	color := make(map[string]int, len(d.services))
	parent := make(map[string]string, len(d.services))

	var dfs func(id string) []string
	dfs = func(id string) []string {
		color[id] = gray
		for _, dep := range d.edges[id] {
			if color[dep] == gray {
				// Found a cycle — reconstruct path.
				cycle := []string{dep, id}
				cur := id
				for cur != dep {
					cur = parent[cur]
					cycle = append(cycle, cur)
				}
				// Reverse to get forward order.
				for i, j := 0, len(cycle)-1; i < j; i, j = i+1, j-1 {
					cycle[i], cycle[j] = cycle[j], cycle[i]
				}
				return cycle
			}
			if color[dep] == white {
				parent[dep] = id
				if c := dfs(dep); c != nil {
					return c
				}
			}
		}
		color[id] = black
		return nil
	}

	for id := range d.services {
		if color[id] == white {
			if c := dfs(id); c != nil {
				return c
			}
		}
	}
	return nil
}

// TopologicalOrder returns services grouped into levels. Level 0 contains
// services with no dependencies. Level 1 contains services whose dependencies
// are all in level 0, and so on. Services within a level can be started in
// parallel.
func (d *DAG) TopologicalOrder() ([][]*Service, error) {
	// Kahn's algorithm.
	inDegree := make(map[string]int, len(d.services))
	for id := range d.services {
		inDegree[id] = len(d.edges[id])
	}

	// Seed level 0 with nodes that have no dependencies.
	var currentLevel []string
	for id, deg := range inDegree {
		if deg == 0 {
			currentLevel = append(currentLevel, id)
		}
	}

	var levels [][]*Service
	visited := 0

	for len(currentLevel) > 0 {
		// Convert current level IDs to services.
		level := make([]*Service, len(currentLevel))
		for i, id := range currentLevel {
			level[i] = d.services[id]
		}
		levels = append(levels, level)
		visited += len(currentLevel)

		// Find next level.
		var nextLevel []string
		for _, id := range currentLevel {
			for _, dependent := range d.reverse[id] {
				inDegree[dependent]--
				if inDegree[dependent] == 0 {
					nextLevel = append(nextLevel, dependent)
				}
			}
		}
		currentLevel = nextLevel
	}

	if visited != len(d.services) {
		return nil, fmt.Errorf("cycle detected in dependency graph")
	}

	return levels, nil
}

// DependenciesOf returns the direct dependencies (prerequisites) of the given service.
func (d *DAG) DependenciesOf(serviceID string) []*Service {
	deps := d.edges[serviceID]
	result := make([]*Service, 0, len(deps))
	for _, dep := range deps {
		if svc, ok := d.services[dep]; ok {
			result = append(result, svc)
		}
	}
	return result
}

// DependentsOf returns the services that directly depend on the given service.
func (d *DAG) DependentsOf(serviceID string) []*Service {
	deps := d.reverse[serviceID]
	result := make([]*Service, 0, len(deps))
	for _, dep := range deps {
		if svc, ok := d.services[dep]; ok {
			result = append(result, svc)
		}
	}
	return result
}

// Service returns the service with the given ID, or nil if not found.
func (d *DAG) Service(id string) *Service {
	return d.services[id]
}
