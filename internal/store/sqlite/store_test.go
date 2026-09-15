package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/domain"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenAndMigrate(t *testing.T) {
	s := openTestStore(t)

	// Verify tables exist by running simple queries.
	ctx := context.Background()
	if _, err := s.ListProjects(ctx); err != nil {
		t.Fatalf("list projects after migration: %v", err)
	}
	if _, err := s.ListResources(ctx, "nonexistent"); err != nil {
		t.Fatalf("list resources after migration: %v", err)
	}
}

func TestOpenIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	s1.Close()

	// Opening again should not fail (migrations already applied).
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	s2.Close()
}

func TestProjectCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	p := &domain.Project{
		ID:          "proj-1",
		Name:        "Test Project",
		Description: "A test project",
	}

	// Save
	if err := s.SaveProject(ctx, p); err != nil {
		t.Fatalf("save project: %v", err)
	}

	// Get
	got, err := s.GetProject(ctx, "proj-1")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if got.Name != "Test Project" {
		t.Errorf("got name %q, want %q", got.Name, "Test Project")
	}
	if got.Description != "A test project" {
		t.Errorf("got description %q, want %q", got.Description, "A test project")
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at should not be zero")
	}

	// Update (upsert)
	p.Name = "Updated Project"
	if err := s.SaveProject(ctx, p); err != nil { //nolint:govet
		t.Fatalf("update project: %v", err)
	}
	got, _ = s.GetProject(ctx, "proj-1")
	if got.Name != "Updated Project" {
		t.Errorf("got name %q, want %q", got.Name, "Updated Project")
	}

	// List
	p2 := &domain.Project{ID: "proj-2", Name: "Another Project"}
	s.SaveProject(ctx, p2)

	list, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d projects, want 2", len(list))
	}

	// Delete
	if err := s.DeleteProject(ctx, "proj-1"); err != nil { //nolint:govet
		t.Fatalf("delete project: %v", err)
	}
	list, _ = s.ListProjects(ctx)
	if len(list) != 1 {
		t.Fatalf("got %d projects after delete, want 1", len(list))
	}

	// Get non-existent
	_, err = s.GetProject(ctx, "proj-1")
	if err == nil {
		t.Fatal("expected error getting deleted project")
	}
}

func TestResourceCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Create parent project first (foreign key).
	s.SaveProject(ctx, &domain.Project{ID: "proj-1", Name: "P1"})

	res := &domain.Resource{
		ID:        "res-1",
		Name:      "My Server",
		Type:      domain.ResourceServer,
		ProjectID: "proj-1",
		Connector: "ssh",
		Config:    map[string]any{"host": "10.0.0.1", "port": float64(22)},
		Tags:      []string{"production", "web"},
		DependsOn: []string{"res-0"},
	}

	// Save
	if err := s.SaveResource(ctx, res); err != nil {
		t.Fatalf("save resource: %v", err)
	}

	// Get
	got, err := s.GetResource(ctx, "res-1")
	if err != nil {
		t.Fatalf("get resource: %v", err)
	}
	if got.Name != "My Server" {
		t.Errorf("got name %q, want %q", got.Name, "My Server")
	}
	if got.Type != domain.ResourceServer {
		t.Errorf("got type %q, want %q", got.Type, domain.ResourceServer)
	}
	if got.Connector != "ssh" {
		t.Errorf("got connector %q, want %q", got.Connector, "ssh")
	}
	if got.Config["host"] != "10.0.0.1" {
		t.Errorf("got config host %v, want 10.0.0.1", got.Config["host"])
	}
	if len(got.Tags) != 2 || got.Tags[0] != "production" {
		t.Errorf("got tags %v, want [production web]", got.Tags)
	}
	if len(got.DependsOn) != 1 || got.DependsOn[0] != "res-0" {
		t.Errorf("got depends_on %v, want [res-0]", got.DependsOn)
	}

	// List by project
	res2 := &domain.Resource{
		ID: "res-2", Name: "Another", Type: domain.ResourceProcess,
		ProjectID: "proj-1", Connector: "local",
		Config: map[string]any{},
	}
	s.SaveResource(ctx, res2)

	list, err := s.ListResources(ctx, "proj-1")
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d resources, want 2", len(list))
	}

	// List by different project returns empty
	list, _ = s.ListResources(ctx, "proj-other")
	if len(list) != 0 {
		t.Fatalf("got %d resources for other project, want 0", len(list))
	}

	// Delete
	if err := s.DeleteResource(ctx, "res-1"); err != nil { //nolint:govet
		t.Fatalf("delete resource: %v", err)
	}
	_, err = s.GetResource(ctx, "res-1")
	if err == nil {
		t.Fatal("expected error getting deleted resource")
	}
}
