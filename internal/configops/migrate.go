package configops

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/registry"
	"gopkg.in/yaml.v3"
)

const migrateFallbackOwner = "unassigned"

type MigrationPreview struct {
	ConfigPath       string
	ProjectsDir      string
	BackupPath       string
	ProjectConfigs   []*registry.ProjectConfig
	Warnings         []string
	ValidationErrors []MigrationValidationError
	TotalResources   int
}

type MigrationValidationError struct {
	Owner   string
	Field   string
	Message string
}

type MigrationResult struct {
	ProjectsDir      string
	BackupPath       string
	WrittenPaths     []string
	Warnings         []string
	TotalResources   int
	RegisteredOwners []string
}

func PreviewMigration(cfgPath string) (*MigrationPreview, error) {
	cfg, err := config.LoadUnified(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", cfgPath, err)
	}

	configs, warnings := BuildProjectConfigs(cfg)
	if len(configs) == 0 {
		return nil, fmt.Errorf("nothing to migrate: %s declares no projects or resources", cfgPath)
	}

	preview := &MigrationPreview{
		ConfigPath:     cfgPath,
		ProjectsDir:    filepath.Join(filepath.Dir(cfgPath), "projects"),
		BackupPath:     cfgPath + ".bak",
		ProjectConfigs: configs,
		Warnings:       warnings,
	}
	for _, pc := range configs {
		preview.TotalResources += len(pc.Resources)
		result := registry.ValidateProjectConfig(pc)
		for _, issue := range result.Errors() {
			preview.ValidationErrors = append(preview.ValidationErrors, MigrationValidationError{
				Owner:   pc.Owner,
				Field:   issue.Field,
				Message: issue.Message,
			})
		}
	}
	return preview, nil
}

func MigrateConfig(cfgPath string) (*MigrationResult, error) {
	preview, err := PreviewMigration(cfgPath)
	if err != nil {
		return nil, err
	}
	if len(preview.ValidationErrors) > 0 {
		return nil, fmt.Errorf("migration aborted: %d error(s) in the generated configs", len(preview.ValidationErrors))
	}
	if _, statErr := os.Stat(preview.BackupPath); statErr == nil {
		return nil, fmt.Errorf("backup %s already exists; move it aside before migrating", preview.BackupPath)
	}
	if err := os.MkdirAll(preview.ProjectsDir, 0o750); err != nil {
		return nil, fmt.Errorf("create %s: %w", preview.ProjectsDir, err)
	}

	reg, err := registry.ForConfig(cfgPath)
	if err != nil {
		return nil, err
	}

	written := make([]string, 0, len(preview.ProjectConfigs))
	owners := make([]string, 0, len(preview.ProjectConfigs))
	for _, pc := range preview.ProjectConfigs {
		dest := filepath.Join(preview.ProjectsDir, pc.Owner+registry.FileSuffix)
		data, marshalErr := yaml.Marshal(pc)
		if marshalErr != nil {
			return nil, fmt.Errorf("marshal %s: %w", pc.Owner, marshalErr)
		}
		if err := os.WriteFile(dest, data, 0o600); err != nil {
			return nil, fmt.Errorf("write %s: %w", dest, err)
		}
		written = append(written, dest)
		owners = append(owners, pc.Owner)
	}
	for _, dest := range written {
		if _, err := reg.Register(dest); err != nil {
			return nil, fmt.Errorf("register %s: %w", dest, err)
		}
	}
	if err := os.Rename(cfgPath, preview.BackupPath); err != nil {
		return nil, fmt.Errorf("back up %s: %w", cfgPath, err)
	}

	return &MigrationResult{
		ProjectsDir:      preview.ProjectsDir,
		BackupPath:       preview.BackupPath,
		WrittenPaths:     written,
		Warnings:         preview.Warnings,
		TotalResources:   preview.TotalResources,
		RegisteredOwners: owners,
	}, nil
}

func BuildProjectConfigs(cfg *config.ConfigV2) ([]*registry.ProjectConfig, []string) {
	var warnings []string
	byOwner := map[string]*registry.ProjectConfig{}
	order := make([]string, 0, len(cfg.Projects)+1)

	add := func(id, name string) *registry.ProjectConfig {
		pc := &registry.ProjectConfig{
			Kind:      registry.ProjectConfigKind,
			Owner:     id,
			Namespace: registry.DefaultNamespace,
			Project:   config.ProjectDef{ID: id, Name: name},
		}
		byOwner[id] = pc
		order = append(order, id)
		return pc
	}

	for _, p := range cfg.Projects {
		pc := add(p.ID, p.Name)
		pc.Project = p
	}

	fallback := func() *registry.ProjectConfig {
		if pc, ok := byOwner[migrateFallbackOwner]; ok {
			return pc
		}
		return add(migrateFallbackOwner, "Unassigned (migrated)")
	}

	resources := make([]config.ResourceDef, 0, len(cfg.Resources))
	seen := map[string]config.ResourceDef{}
	for _, r := range cfg.Resources {
		if prev, dup := seen[r.ID]; dup {
			if reflect.DeepEqual(prev, r) {
				warnings = append(warnings, fmt.Sprintf(
					"resource %q has duplicate identical entries in config.yaml; collapsed to one", r.ID))
			} else {
				warnings = append(warnings, fmt.Sprintf(
					"resource %q has duplicate entries with differing fields in config.yaml; kept the first", r.ID))
			}
			continue
		}
		seen[r.ID] = r
		resources = append(resources, r)
	}

	for _, r := range resources {
		if pc, ok := byOwner[r.Project]; ok && r.Project != "" {
			pc.Resources = append(pc.Resources, r)
			continue
		}
		warnings = append(warnings, fmt.Sprintf(
			"resource %q has no known project (project=%q); assigned to owner %q",
			r.ID, r.Project, migrateFallbackOwner))
		orphan := r
		orphan.Project = migrateFallbackOwner
		fb := fallback()
		fb.Resources = append(fb.Resources, orphan)
	}

	for _, pl := range cfg.Pipelines {
		owner := PipelineProjectOwner(pl, cfg)
		pc, ok := byOwner[owner]
		if !ok {
			if len(order) == 0 {
				warnings = append(warnings, fmt.Sprintf("pipeline %q dropped: no project to attach it to", pl.ID))
				continue
			}
			warnings = append(warnings, fmt.Sprintf(
				"pipeline %q has no resolvable project; assigned to owner %q", pl.ID, order[0]))
			pc = byOwner[order[0]]
		}
		pc.Pipelines = append(pc.Pipelines, pl)
	}

	out := make([]*registry.ProjectConfig, 0, len(order))
	for _, id := range order {
		out = append(out, byOwner[id])
	}
	return out, warnings
}

func PipelineProjectOwner(pl config.PipelineDef, cfg *config.ConfigV2) string {
	for _, stage := range pl.Stages {
		for _, action := range stage.Actions {
			if action.Resource == "" {
				continue
			}
			for _, r := range cfg.Resources {
				if r.ID == action.Resource {
					return r.Project
				}
			}
		}
	}
	return ""
}
