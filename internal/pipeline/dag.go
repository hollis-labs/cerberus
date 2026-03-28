package pipeline

import (
	"fmt"
	"strings"
)

// stageDAG is a dependency graph for pipeline stages.
type stageDAG struct {
	stages  map[string]*Stage
	edges   map[string][]string // stage -> its dependencies
	reverse map[string][]string // stage -> stages that depend on it
}

// buildDAG constructs a DAG from a pipeline's stages.
func buildDAG(stages []*Stage) (*stageDAG, error) {
	d := &stageDAG{
		stages:  make(map[string]*Stage, len(stages)),
		edges:   make(map[string][]string, len(stages)),
		reverse: make(map[string][]string, len(stages)),
	}

	for _, s := range stages {
		d.stages[s.Name] = s
	}

	for _, s := range stages {
		for _, dep := range s.DependsOn {
			if _, ok := d.stages[dep]; !ok {
				return nil, fmt.Errorf("stage %q depends on unknown stage %q", s.Name, dep)
			}
			d.edges[s.Name] = append(d.edges[s.Name], dep)
			d.reverse[dep] = append(d.reverse[dep], s.Name)
		}
	}

	if cycle := d.detectCycle(); cycle != nil {
		return nil, fmt.Errorf("circular dependency: %s", strings.Join(cycle, " -> "))
	}

	return d, nil
}

// detectCycle performs DFS to find cycles. Same algorithm as service/dag.go.
func (d *stageDAG) detectCycle() []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)

	color := make(map[string]int, len(d.stages))
	parent := make(map[string]string, len(d.stages))

	var dfs func(id string) []string
	dfs = func(id string) []string {
		color[id] = gray
		for _, dep := range d.edges[id] {
			if color[dep] == gray {
				cycle := []string{dep, id}
				cur := id
				for cur != dep {
					cur = parent[cur]
					cycle = append(cycle, cur)
				}
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

	for id := range d.stages {
		if color[id] == white {
			if c := dfs(id); c != nil {
				return c
			}
		}
	}
	return nil
}

// levels returns stages grouped by dependency level using Kahn's algorithm.
// Level 0 has no dependencies, level 1 depends only on level 0, etc.
// Stages within a level can run in parallel.
func (d *stageDAG) levels() ([][]*Stage, error) {
	inDegree := make(map[string]int, len(d.stages))
	for id := range d.stages {
		inDegree[id] = len(d.edges[id])
	}

	var current []string
	for id, deg := range inDegree {
		if deg == 0 {
			current = append(current, id)
		}
	}

	var result [][]*Stage
	visited := 0

	for len(current) > 0 {
		level := make([]*Stage, len(current))
		for i, id := range current {
			level[i] = d.stages[id]
		}
		result = append(result, level)
		visited += len(current)

		var next []string
		for _, id := range current {
			for _, dependent := range d.reverse[id] {
				inDegree[dependent]--
				if inDegree[dependent] == 0 {
					next = append(next, dependent)
				}
			}
		}
		current = next
	}

	if visited != len(d.stages) {
		return nil, fmt.Errorf("cycle detected in stage graph")
	}

	return result, nil
}
