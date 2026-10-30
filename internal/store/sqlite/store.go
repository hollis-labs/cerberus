package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hollis-labs/cerberus/internal/domain"
	corestore "github.com/hollis-labs/libs/core/store"
)

// Store implements domain.Store backed by SQLite, opened through libs
// core/store: WAL, a busy timeout, foreign keys, and a single write connection
// (the application database has several writers: scheduling, host monitoring).
type Store struct {
	db   *sql.DB
	core *corestore.Store
}

// busyTimeout is how long a connection waits for another process's lock.
const busyTimeout = 5 * time.Second

// Open opens the database at path through core/store and runs any pending
// numbered migrations. The directory is created when it is missing.
func Open(path string) (*Store, error) {
	cs, err := corestore.Open(context.Background(), corestore.Config{
		Driver: corestore.DriverSQLite,
		SQLite: corestore.SQLiteConfig{Path: path, Journal: "wal", BusyTimeout: busyTimeout, CreateParentDir: true},
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}
	if err := migrate(cs.DB()); err != nil {
		cs.Close() //nolint:errcheck
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	return &Store{db: cs.DB(), core: cs}, nil
}

// DB borrows the application-owned write connection, which the scheduling
// library uses with its own transactions. Nothing running inside a Core().WriteTx
// callback may use it: the write connection is the one the transaction holds.
func (s *Store) DB() *sql.DB { return s.db }

// Core is the core/store handle: WriteTx and ReadTx for code that does its own
// transactions (the host samples, status, rollups and events).
func (s *Store) Core() *corestore.Store { return s.core }

// Close closes the underlying database connections.
func (s *Store) Close() error {
	return s.core.Close()
}

// --- Projects ---

func (s *Store) SaveProject(ctx context.Context, p *domain.Project) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	p.UpdatedAt = time.Now().UTC()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO projects (id, name, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name = excluded.name, description = excluded.description,
		   created_at = excluded.created_at, updated_at = excluded.updated_at`,
		p.ID, p.Name, p.Description,
		p.CreatedAt.UTC().Format(time.RFC3339), now,
	)
	if err != nil {
		return fmt.Errorf("save project: %w", err)
	}
	return nil
}

func (s *Store) GetProject(ctx context.Context, id string) (*domain.Project, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT id, name, description, created_at, updated_at FROM projects WHERE id = ?", id)

	p := &domain.Project{}
	var createdAt, updatedAt string
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &createdAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("project not found: %s", id)
		}
		return nil, fmt.Errorf("get project: %w", err)
	}

	var err error
	p.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	p.UpdatedAt, err = time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return p, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]*domain.Project, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, name, description, created_at, updated_at FROM projects ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var projects []*domain.Project
	for rows.Next() {
		p := &domain.Project{}
		var createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (s *Store) DeleteProject(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	return nil
}

// --- Resources ---

func (s *Store) SaveResource(ctx context.Context, res *domain.Resource) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if res.CreatedAt.IsZero() {
		res.CreatedAt = time.Now().UTC()
	}
	res.UpdatedAt = time.Now().UTC()

	configJSON, err := json.Marshal(res.Config)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tagsJSON, err := json.Marshal(res.Tags)
	if err != nil {
		return fmt.Errorf("marshal tags: %w", err)
	}
	depsJSON, err := json.Marshal(res.DependsOn)
	if err != nil {
		return fmt.Errorf("marshal depends_on: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO resources
		 (id, name, type, project_id, connector, config, tags, depends_on, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name = excluded.name, type = excluded.type, project_id = excluded.project_id,
		   connector = excluded.connector, config = excluded.config, tags = excluded.tags,
		   depends_on = excluded.depends_on, created_at = excluded.created_at, updated_at = excluded.updated_at`,
		res.ID, res.Name, string(res.Type), res.ProjectID, res.Connector,
		string(configJSON), string(tagsJSON), string(depsJSON),
		res.CreatedAt.UTC().Format(time.RFC3339), now,
	)
	if err != nil {
		return fmt.Errorf("save resource: %w", err)
	}
	return nil
}

func (s *Store) GetResource(ctx context.Context, id string) (*domain.Resource, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, type, project_id, connector, config, tags, depends_on, created_at, updated_at
		 FROM resources WHERE id = ?`, id)

	return scanResource(row)
}

func (s *Store) ListResources(ctx context.Context, projectID string) ([]*domain.Resource, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, type, project_id, connector, config, tags, depends_on, created_at, updated_at
		 FROM resources WHERE project_id = ? ORDER BY name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var resources []*domain.Resource
	for rows.Next() {
		res := &domain.Resource{}
		var resType, configStr, tagsStr, depsStr, createdAt, updatedAt string
		if err := rows.Scan(&res.ID, &res.Name, &resType, &res.ProjectID, &res.Connector,
			&configStr, &tagsStr, &depsStr, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan resource: %w", err)
		}
		res.Type = domain.ResourceType(resType)
		json.Unmarshal([]byte(configStr), &res.Config)  //nolint:errcheck
		json.Unmarshal([]byte(tagsStr), &res.Tags)      //nolint:errcheck
		json.Unmarshal([]byte(depsStr), &res.DependsOn) //nolint:errcheck
		res.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		res.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		resources = append(resources, res)
	}
	return resources, rows.Err()
}

func (s *Store) DeleteResource(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM resources WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete resource: %w", err)
	}
	return nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanResource(row scanner) (*domain.Resource, error) {
	res := &domain.Resource{}
	var resType, configStr, tagsStr, depsStr, createdAt, updatedAt string
	if err := row.Scan(&res.ID, &res.Name, &resType, &res.ProjectID, &res.Connector,
		&configStr, &tagsStr, &depsStr, &createdAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows { //nolint:errorlint
			return nil, fmt.Errorf("resource not found")
		}
		return nil, fmt.Errorf("get resource: %w", err)
	}
	res.Type = domain.ResourceType(resType)
	json.Unmarshal([]byte(configStr), &res.Config)  //nolint:errcheck
	json.Unmarshal([]byte(tagsStr), &res.Tags)      //nolint:errcheck
	json.Unmarshal([]byte(depsStr), &res.DependsOn) //nolint:errcheck

	var err error
	res.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	res.UpdatedAt, err = time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return res, nil
}

// Compile-time check that Store implements domain.Store.
var _ domain.Store = (*Store)(nil)
