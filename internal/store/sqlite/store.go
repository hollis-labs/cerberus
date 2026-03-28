package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chrispian/cerberus/internal/domain"
	_ "modernc.org/sqlite" // SQLite driver registration
)

// Store implements domain.Store backed by SQLite.
type Store struct {
	db *sql.DB
}

// Open creates a new SQLite store at the given path, enables WAL mode and
// foreign keys, and runs any pending migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close() //nolint:errcheck
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close() //nolint:errcheck
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close() //nolint:errcheck
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// --- Projects ---

func (s *Store) SaveProject(ctx context.Context, p *domain.Project) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	p.UpdatedAt = time.Now().UTC()

	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO projects (id, name, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
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
		`INSERT OR REPLACE INTO resources
		 (id, name, type, project_id, connector, config, tags, depends_on, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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

// --- Pipeline Runs ---

func (s *Store) SavePipelineRun(ctx context.Context, run *domain.PipelineRun) error {
	var finishedAt string
	if !run.FinishedAt.IsZero() {
		finishedAt = run.FinishedAt.UTC().Format(time.RFC3339)
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO pipeline_runs (id, pipeline_id, status, started_at, finished_at, error)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		run.ID, run.PipelineID, string(run.Status),
		run.StartedAt.UTC().Format(time.RFC3339), finishedAt, run.Error,
	)
	if err != nil {
		return fmt.Errorf("save pipeline run: %w", err)
	}
	return nil
}

func (s *Store) GetPipelineRun(ctx context.Context, id string) (*domain.PipelineRun, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT id, pipeline_id, status, started_at, finished_at, error FROM pipeline_runs WHERE id = ?", id)

	run := &domain.PipelineRun{}
	var status, startedAt, finishedAt string
	if err := row.Scan(&run.ID, &run.PipelineID, &status, &startedAt, &finishedAt, &run.Error); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("pipeline run not found: %s", id)
		}
		return nil, fmt.Errorf("get pipeline run: %w", err)
	}
	run.Status = domain.State(status)

	var err error
	run.StartedAt, err = time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return nil, fmt.Errorf("parse started_at: %w", err)
	}
	if finishedAt != "" {
		run.FinishedAt, err = time.Parse(time.RFC3339, finishedAt)
		if err != nil {
			return nil, fmt.Errorf("parse finished_at: %w", err)
		}
	}
	return run, nil
}

func (s *Store) ListPipelineRuns(ctx context.Context, pipelineID string) ([]*domain.PipelineRun, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, pipeline_id, status, started_at, finished_at, error FROM pipeline_runs WHERE pipeline_id = ? ORDER BY started_at DESC",
		pipelineID)
	if err != nil {
		return nil, fmt.Errorf("list pipeline runs: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var runs []*domain.PipelineRun
	for rows.Next() {
		run := &domain.PipelineRun{}
		var status, startedAt, finishedAt string
		if err := rows.Scan(&run.ID, &run.PipelineID, &status, &startedAt, &finishedAt, &run.Error); err != nil {
			return nil, fmt.Errorf("scan pipeline run: %w", err)
		}
		run.Status = domain.State(status)
		run.StartedAt, _ = time.Parse(time.RFC3339, startedAt)
		if finishedAt != "" {
			run.FinishedAt, _ = time.Parse(time.RFC3339, finishedAt)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// Compile-time check that Store implements domain.Store.
var _ domain.Store = (*Store)(nil)
