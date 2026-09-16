package service

import (
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/config"
)

// helper to build a service with an ID and optional dependencies.
func makeSvc(id string, deps ...string) *ManagedService {
	return &ManagedService{
		Def: config.ServiceDef{
			ID:        id,
			Name:      id,
			Port:      0,
			DependsOn: deps,
		},
	}
}

// levelIDs extracts sorted service IDs from a topological level for comparison.
func levelIDs(level []*ManagedService) map[string]bool {
	m := make(map[string]bool, len(level))
	for _, s := range level {
		m[s.Def.ID] = true
	}
	return m
}

func TestLinearChain(t *testing.T) {
	// A -> B -> C  (C depends on B, B depends on A)
	a := makeSvc("A")
	b := makeSvc("B", "A")
	c := makeSvc("C", "B")

	dag, err := BuildDAG([]*ManagedService{a, b, c})
	if err != nil {
		t.Fatalf("BuildDAG failed: %v", err)
	}

	levels, err := dag.TopologicalOrder()
	if err != nil {
		t.Fatalf("TopologicalOrder failed: %v", err)
	}

	if len(levels) != 3 {
		t.Fatalf("expected 3 levels, got %d", len(levels))
	}

	// Level 0: A (no deps)
	if ids := levelIDs(levels[0]); !ids["A"] || len(ids) != 1 {
		t.Errorf("level 0: expected {A}, got %v", ids)
	}
	// Level 1: B
	if ids := levelIDs(levels[1]); !ids["B"] || len(ids) != 1 {
		t.Errorf("level 1: expected {B}, got %v", ids)
	}
	// Level 2: C
	if ids := levelIDs(levels[2]); !ids["C"] || len(ids) != 1 {
		t.Errorf("level 2: expected {C}, got %v", ids)
	}
}

func TestParallelDeps(t *testing.T) {
	// A and B have no deps; C depends on both A and B.
	a := makeSvc("A")
	b := makeSvc("B")
	c := makeSvc("C", "A", "B")

	dag, err := BuildDAG([]*ManagedService{a, b, c})
	if err != nil {
		t.Fatalf("BuildDAG failed: %v", err)
	}

	levels, err := dag.TopologicalOrder()
	if err != nil {
		t.Fatalf("TopologicalOrder failed: %v", err)
	}

	if len(levels) != 2 {
		t.Fatalf("expected 2 levels, got %d", len(levels))
	}

	// Level 0: A and B (parallel)
	ids0 := levelIDs(levels[0])
	if !ids0["A"] || !ids0["B"] || len(ids0) != 2 {
		t.Errorf("level 0: expected {A, B}, got %v", ids0)
	}

	// Level 1: C
	ids1 := levelIDs(levels[1])
	if !ids1["C"] || len(ids1) != 1 {
		t.Errorf("level 1: expected {C}, got %v", ids1)
	}
}

func TestCircularDependency(t *testing.T) {
	// A -> B -> A (cycle)
	a := makeSvc("A", "B")
	b := makeSvc("B", "A")

	_, err := BuildDAG([]*ManagedService{a, b})
	if err == nil {
		t.Fatal("expected error for circular dependency, got nil")
	}
	if !strings.Contains(err.Error(), "circular dependency") {
		t.Errorf("expected 'circular dependency' in error, got: %v", err)
	}
}

func TestMissingDependency(t *testing.T) {
	// A depends on "ghost" which doesn't exist.
	a := makeSvc("A", "ghost")

	_, err := BuildDAG([]*ManagedService{a})
	if err == nil {
		t.Fatal("expected error for missing dependency, got nil")
	}
	if !strings.Contains(err.Error(), "unknown service") {
		t.Errorf("expected 'unknown service' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("expected 'ghost' in error message, got: %v", err)
	}
}

func TestSingleServiceNoDeps(t *testing.T) {
	a := makeSvc("A")

	dag, err := BuildDAG([]*ManagedService{a})
	if err != nil {
		t.Fatalf("BuildDAG failed: %v", err)
	}

	levels, err := dag.TopologicalOrder()
	if err != nil {
		t.Fatalf("TopologicalOrder failed: %v", err)
	}

	if len(levels) != 1 {
		t.Fatalf("expected 1 level, got %d", len(levels))
	}
	if len(levels[0]) != 1 || levels[0][0].Def.ID != "A" {
		t.Errorf("expected single service A in level 0")
	}
}

func TestEmptyServices(t *testing.T) {
	dag, err := BuildDAG([]*ManagedService{})
	if err != nil {
		t.Fatalf("BuildDAG failed: %v", err)
	}

	levels, err := dag.TopologicalOrder()
	if err != nil {
		t.Fatalf("TopologicalOrder failed: %v", err)
	}

	if len(levels) != 0 {
		t.Errorf("expected 0 levels for empty services, got %d", len(levels))
	}
}

func TestDependenciesOf(t *testing.T) {
	a := makeSvc("A")
	b := makeSvc("B")
	c := makeSvc("C", "A", "B")

	dag, err := BuildDAG([]*ManagedService{a, b, c})
	if err != nil {
		t.Fatalf("BuildDAG failed: %v", err)
	}

	deps := dag.DependenciesOf("C")
	if len(deps) != 2 {
		t.Fatalf("expected 2 dependencies for C, got %d", len(deps))
	}

	ids := make(map[string]bool)
	for _, d := range deps {
		ids[d.Def.ID] = true
	}
	if !ids["A"] || !ids["B"] {
		t.Errorf("expected dependencies {A, B}, got %v", ids)
	}

	// A has no dependencies.
	if deps := dag.DependenciesOf("A"); len(deps) != 0 {
		t.Errorf("expected 0 dependencies for A, got %d", len(deps))
	}
}

func TestDependentsOf(t *testing.T) {
	a := makeSvc("A")
	b := makeSvc("B", "A")
	c := makeSvc("C", "A")

	dag, err := BuildDAG([]*ManagedService{a, b, c})
	if err != nil {
		t.Fatalf("BuildDAG failed: %v", err)
	}

	dependents := dag.DependentsOf("A")
	if len(dependents) != 2 {
		t.Fatalf("expected 2 dependents of A, got %d", len(dependents))
	}

	ids := make(map[string]bool)
	for _, d := range dependents {
		ids[d.Def.ID] = true
	}
	if !ids["B"] || !ids["C"] {
		t.Errorf("expected dependents {B, C}, got %v", ids)
	}
}

func TestThreeLevelCycle(t *testing.T) {
	// A -> B -> C -> A
	a := makeSvc("A", "C")
	b := makeSvc("B", "A")
	c := makeSvc("C", "B")

	_, err := BuildDAG([]*ManagedService{a, b, c})
	if err == nil {
		t.Fatal("expected error for 3-node cycle, got nil")
	}
	if !strings.Contains(err.Error(), "circular dependency") {
		t.Errorf("expected 'circular dependency' in error, got: %v", err)
	}
}

func TestManagerNewServiceManager(t *testing.T) {
	a := makeSvc("A")
	b := makeSvc("B", "A")

	mgr, err := NewServiceManager([]*ManagedService{a, b})
	if err != nil {
		t.Fatalf("NewServiceManager failed: %v", err)
	}

	if len(mgr.Services()) != 2 {
		t.Errorf("expected 2 services, got %d", len(mgr.Services()))
	}

	if mgr.DAG() == nil {
		t.Error("expected non-nil DAG")
	}
}

func TestManagerNewServiceManagerCycleError(t *testing.T) {
	a := makeSvc("A", "B")
	b := makeSvc("B", "A")

	_, err := NewServiceManager([]*ManagedService{a, b})
	if err == nil {
		t.Fatal("expected error for circular dependency in NewServiceManager")
	}
}

func TestDiamondDependency(t *testing.T) {
	// Diamond: D depends on B and C, both depend on A.
	//     A
	//    / \
	//   B   C
	//    \ /
	//     D
	a := makeSvc("A")
	b := makeSvc("B", "A")
	c := makeSvc("C", "A")
	d := makeSvc("D", "B", "C")

	dag, err := BuildDAG([]*ManagedService{a, b, c, d})
	if err != nil {
		t.Fatalf("BuildDAG failed: %v", err)
	}

	levels, err := dag.TopologicalOrder()
	if err != nil {
		t.Fatalf("TopologicalOrder failed: %v", err)
	}

	if len(levels) != 3 {
		t.Fatalf("expected 3 levels, got %d", len(levels))
	}

	// Level 0: A
	if ids := levelIDs(levels[0]); !ids["A"] || len(ids) != 1 {
		t.Errorf("level 0: expected {A}, got %v", ids)
	}
	// Level 1: B and C (parallel)
	if ids := levelIDs(levels[1]); !ids["B"] || !ids["C"] || len(ids) != 2 {
		t.Errorf("level 1: expected {B, C}, got %v", ids)
	}
	// Level 2: D
	if ids := levelIDs(levels[2]); !ids["D"] || len(ids) != 1 {
		t.Errorf("level 2: expected {D}, got %v", ids)
	}
}
