package webui

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chrispian/cerberus/internal/cerbapi"
	"github.com/chrispian/cerberus/internal/config"
	"github.com/chrispian/cerberus/internal/configops"
	"github.com/chrispian/cerberus/internal/registry"
)

type overviewResponse struct {
	Daemon    overviewDaemonDTO   `json:"daemon"`
	Inventory overviewCountsDTO   `json:"inventory"`
	Runtime   overviewRuntimeDTO  `json:"runtime"`
	Registry  overviewRegistryDTO `json:"registry"`
	Error     string              `json:"error,omitempty"`
}

type overviewDaemonDTO struct {
	Running           bool   `json:"running"`
	SocketPath        string `json:"socket_path,omitempty"`
	SocketExists      bool   `json:"socket_exists"`
	ServicesFailed    int    `json:"services_failed"`
	ServicesProtected int    `json:"services_protected"`
}

type overviewCountsDTO struct {
	Projects   int `json:"projects"`
	Resources  int `json:"resources"`
	Pipelines  int `json:"pipelines"`
	Connectors int `json:"connectors"`
	Plugins    int `json:"plugins"`
}

type overviewRuntimeDTO struct {
	Running   int `json:"running"`
	Attention int `json:"attention"`
	Stopped   int `json:"stopped"`
}

type overviewRegistryDTO struct {
	Entries   int `json:"entries"`
	Healthy   int `json:"healthy"`
	Unhealthy int `json:"unhealthy"`
}

type systemResponse struct {
	ConfigPath        string                `json:"config_path,omitempty"`
	ConfigExists      bool                  `json:"config_exists"`
	RegistryPath      string                `json:"registry_path,omitempty"`
	RegistryExists    bool                  `json:"registry_exists"`
	SocketPath        string                `json:"socket_path,omitempty"`
	SocketExists      bool                  `json:"socket_exists"`
	DaemonRunning     bool                  `json:"daemon_running"`
	ResolvedProjects  int                   `json:"resolved_projects"`
	ResolvedResources int                   `json:"resolved_resources"`
	ResolvedPipelines int                   `json:"resolved_pipelines"`
	ResolveWarnings   []string              `json:"resolve_warnings,omitempty"`
	Health            *cerbapi.DaemonHealth `json:"health,omitempty"`
	Error             string                `json:"error,omitempty"`
}

type settingsResponse struct {
	ConfigPath               string `json:"config_path,omitempty"`
	ConfigExists             bool   `json:"config_exists"`
	RegistryPath             string `json:"registry_path,omitempty"`
	RegistryExists           bool   `json:"registry_exists"`
	InstallAfterBuildDefault bool   `json:"install_after_build_default"`
	Version                  int    `json:"version"`
	HasGlobalBuildConfig     bool   `json:"has_global_build_config"`
	BackupCount              int    `json:"backup_count"`
	ResolvedProjects         int    `json:"resolved_projects"`
	ResolvedResources        int    `json:"resolved_resources"`
	ResolvedPipelines        int    `json:"resolved_pipelines"`
	Error                    string `json:"error,omitempty"`
}

type registryCollectionResponse struct {
	ConfigPath      string               `json:"config_path,omitempty"`
	ConfigExists    bool                 `json:"config_exists"`
	IndexPath       string               `json:"index_path,omitempty"`
	IndexExists     bool                 `json:"index_exists"`
	Summary         registryAuditDTO     `json:"summary"`
	ResolveWarnings []string             `json:"resolve_warnings,omitempty"`
	Skipped         []registryHealthDTO  `json:"skipped,omitempty"`
	Entries         []registryEntryDTO   `json:"entries"`
	Error           string               `json:"error,omitempty"`
}

type registryHealthResponse struct {
	Summary overviewRegistryDTO `json:"summary"`
	Reports []registryHealthDTO `json:"reports"`
	Error   string              `json:"error,omitempty"`
}

type registryEntryDTO struct {
	Owner         string `json:"owner"`
	Namespace     string `json:"namespace"`
	Kind          string `json:"kind"`
	Path          string `json:"path"`
	RegisteredAt  string `json:"registered_at,omitempty"`
	Via           string `json:"via,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	ProjectName   string `json:"project_name,omitempty"`
	ResourceCount int    `json:"resource_count"`
	PipelineCount int    `json:"pipeline_count"`
	RegistryURN   string `json:"registry_urn,omitempty"`
	SharedIdentity bool  `json:"shared_identity"`
	HealthStatus  string `json:"health_status,omitempty"`
	HealthDetail  string `json:"health_detail,omitempty"`
}

type registryAuditDTO struct {
	Entries      int `json:"entries"`
	Healthy      int `json:"healthy"`
	Unhealthy    int `json:"unhealthy"`
	Shared       int `json:"shared"`
	LocalOnly    int `json:"local_only"`
	ResolveSkips int `json:"resolve_skips"`
}

type registryHealthDTO struct {
	Owner  string `json:"owner"`
	Path   string `json:"path"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type configValidationResponse struct {
	Summary    configValidationSummaryDTO `json:"summary"`
	Global     *configValidationFileDTO   `json:"global,omitempty"`
	Registered []configValidationFileDTO  `json:"registered"`
	Error      string                     `json:"error,omitempty"`
}

type configValidationSummaryDTO struct {
	Files    int `json:"files"`
	Valid    int `json:"valid"`
	Invalid  int `json:"invalid"`
	Warnings int `json:"warnings"`
	Errors   int `json:"errors"`
}

type configValidationFileDTO struct {
	Path     string   `json:"path"`
	Kind     string   `json:"kind"`
	Owner    string   `json:"owner,omitempty"`
	OK       bool     `json:"ok"`
	Warnings []string `json:"warnings,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

type configResolveResponse struct {
	ConfigPath string                   `json:"config_path,omitempty"`
	IndexPath  string                   `json:"index_path,omitempty"`
	Projects   int                      `json:"projects"`
	Resources  int                      `json:"resources"`
	Pipelines  int                      `json:"pipelines"`
	Warnings   []string                 `json:"warnings,omitempty"`
	Skipped    []registryHealthDTO      `json:"skipped,omitempty"`
	Global     *configValidationFileDTO `json:"global,omitempty"`
	Error      string                   `json:"error,omitempty"`
}

type configBackupResponse struct {
	Backups []configBackupDTO `json:"backups"`
	Error   string            `json:"error,omitempty"`
}

type configBackupDTO struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

type configMigratePreviewResponse struct {
	ConfigPath       string                               `json:"config_path,omitempty"`
	ProjectsDir      string                               `json:"projects_dir,omitempty"`
	BackupPath       string                               `json:"backup_path,omitempty"`
	ProjectCount     int                                  `json:"project_count"`
	TotalResources   int                                  `json:"total_resources"`
	Warnings         []string                             `json:"warnings,omitempty"`
	ValidationErrors []configops.MigrationValidationError `json:"validation_errors,omitempty"`
	Entries          []configMigrateEntryDTO              `json:"entries"`
	Error            string                               `json:"error,omitempty"`
}

type configMigrateEntryDTO struct {
	Owner         string `json:"owner"`
	ProjectID     string `json:"project_id"`
	ProjectName   string `json:"project_name,omitempty"`
	ResourceCount int    `json:"resource_count"`
	PipelineCount int    `json:"pipeline_count"`
	Destination   string `json:"destination"`
}

type configMigrateResponse struct {
	Success          bool                                 `json:"success"`
	ProjectsDir      string                               `json:"projects_dir,omitempty"`
	BackupPath       string                               `json:"backup_path,omitempty"`
	Warnings         []string                             `json:"warnings,omitempty"`
	WrittenPaths     []string                             `json:"written_paths,omitempty"`
	RegisteredOwners []string                             `json:"registered_owners,omitempty"`
	TotalResources   int                                  `json:"total_resources"`
	ValidationErrors []configops.MigrationValidationError `json:"validation_errors,omitempty"`
	Error            string                               `json:"error,omitempty"`
}

type configRestoreRequest struct {
	BackupPath string `json:"backup_path"`
}

type registryRegisterRequest struct {
	Path string `json:"path"`
}

type registryDeregisterRequest struct {
	Owner string `json:"owner"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	resp := overviewResponse{}
	var errs []string

	socketPath, sockErr := cerbapi.SocketPath()
	if sockErr == nil {
		resp.Daemon.SocketPath = socketPath
		resp.Daemon.SocketExists = fileExists(socketPath)
	} else {
		errs = append(errs, sockErr.Error())
	}

	if health, err := s.client.Health(r.Context(), ""); err == nil {
		resp.Daemon.Running = health.DaemonRunning
		resp.Daemon.ServicesFailed = health.ServicesFailed
		resp.Daemon.ServicesProtected = health.ServicesProtected
		resp.Runtime = summarizeRuntime(health.Resources)
	} else {
		errs = append(errs, err.Error())
	}

	if projects, err := s.client.ListProjects(r.Context()); err == nil {
		resp.Inventory.Projects = len(projects)
	} else {
		errs = append(errs, err.Error())
	}
	if resources, err := s.client.ListResources(r.Context(), cerbapi.ResourceListArgs{}); err == nil {
		resp.Inventory.Resources = len(resources)
		if resp.Runtime == (overviewRuntimeDTO{}) {
			resp.Runtime = summarizeResourceInfos(resources)
		}
	} else {
		errs = append(errs, err.Error())
	}
	if pipelines, err := s.client.ListPipelines(r.Context()); err == nil {
		resp.Inventory.Pipelines = len(pipelines)
	} else {
		errs = append(errs, err.Error())
	}
	if connectors, err := s.client.ListConnectors(r.Context()); err == nil {
		resp.Inventory.Connectors = len(connectors)
	} else {
		errs = append(errs, err.Error())
	}
	if plugins, err := s.client.ListManagedPlugins(r.Context()); err == nil {
		resp.Inventory.Plugins = len(plugins)
	} else {
		errs = append(errs, err.Error())
	}

	if summary, err := s.registrySummary(); err == nil {
		resp.Registry = summary
	} else {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		resp.Error = strings.Join(uniqueStrings(errs), " | ")
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	resp := settingsResponse{
		ConfigPath:   s.configPath,
		ConfigExists: s.configPath != "" && fileExists(s.configPath),
	}
	var errs []string

	if registryPath, err := registry.IndexPathFor(s.configPath); err == nil {
		resp.RegistryPath = registryPath
		resp.RegistryExists = fileExists(registryPath)
	} else {
		errs = append(errs, err.Error())
	}
	if cfg, err := config.LoadUnified(s.configPath); err == nil {
		resp.Version = cfg.Version
		resp.InstallAfterBuildDefault = cfg.InstallAfterBuildDefault()
		resp.HasGlobalBuildConfig = cfg.Build != nil
	} else if s.configPath != "" && fileExists(s.configPath) {
		errs = append(errs, err.Error())
	}
	if backups, err := configops.ListConfigBackups(s.configPath); err == nil {
		resp.BackupCount = len(backups)
	} else {
		errs = append(errs, err.Error())
	}
	if resolved, err := registry.ResolveConfig(s.configPath); err == nil {
		resp.ResolvedProjects = len(resolved.Projects)
		resp.ResolvedResources = len(resolved.Resources)
		resp.ResolvedPipelines = len(resolved.Pipelines)
	} else if s.configPath != "" {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		resp.Error = strings.Join(uniqueStrings(errs), " | ")
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	resp := systemResponse{
		ConfigPath:   s.configPath,
		ConfigExists: s.configPath != "" && fileExists(s.configPath),
	}
	var errs []string

	if registryPath, err := registry.IndexPathFor(s.configPath); err == nil {
		resp.RegistryPath = registryPath
		resp.RegistryExists = fileExists(registryPath)
	} else {
		errs = append(errs, err.Error())
	}
	if socketPath, err := cerbapi.SocketPath(); err == nil {
		resp.SocketPath = socketPath
		resp.SocketExists = fileExists(socketPath)
	} else {
		errs = append(errs, err.Error())
	}
	if health, err := s.client.Health(r.Context(), ""); err == nil {
		resp.DaemonRunning = health.DaemonRunning
		resp.Health = health
	} else {
		errs = append(errs, err.Error())
	}
	if s.configPath != "" {
		if resolved, err := registry.ResolveConfig(s.configPath); err == nil {
			resp.ResolvedProjects = len(resolved.Projects)
			resp.ResolvedResources = len(resolved.Resources)
			resp.ResolvedPipelines = len(resolved.Pipelines)
		} else {
			errs = append(errs, err.Error())
		}
		if registryPath, err := registry.IndexPathFor(s.configPath); err == nil {
			if resolved, err := registry.Resolve(registry.ResolveOptions{IndexPath: registryPath, GlobalPath: s.configPath}); err == nil {
				resp.ResolveWarnings = resolved.Warnings
			} else {
				errs = append(errs, err.Error())
			}
		}
	}

	if len(errs) > 0 {
		resp.Error = strings.Join(uniqueStrings(errs), " | ")
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRegistry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	reg, err := registry.ForConfig(s.configPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, registryCollectionResponse{Error: err.Error()})
		return
	}
	entries, err := reg.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, registryCollectionResponse{Error: err.Error()})
		return
	}
	reports, err := reg.Health()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, registryCollectionResponse{Error: err.Error()})
		return
	}
	healthByOwner := make(map[string]registry.HealthReport, len(reports))
	for _, report := range reports {
		healthByOwner[report.Owner] = report
	}

	resp := registryCollectionResponse{
		ConfigPath:   s.configPath,
		ConfigExists: s.configPath != "" && fileExists(s.configPath),
		Entries:      make([]registryEntryDTO, 0, len(entries)),
	}
	if indexPath, err := registry.IndexPathFor(s.configPath); err == nil {
		resp.IndexPath = indexPath
		resp.IndexExists = fileExists(indexPath)
	} else {
		resp.Error = err.Error()
	}
	if resolved, err := registry.Resolve(registry.ResolveOptions{IndexPath: reg.IndexPath(), GlobalPath: s.configPath}); err == nil {
		resp.ResolveWarnings = resolved.Warnings
		resp.Skipped = make([]registryHealthDTO, 0, len(resolved.Skipped))
		for _, skipped := range resolved.Skipped {
			resp.Skipped = append(resp.Skipped, registryHealthDTO{
				Owner:  skipped.Owner,
				Path:   skipped.Path,
				Status: skipped.Status,
				Detail: skipped.Detail,
			})
		}
		resp.Summary.ResolveSkips = len(resolved.Skipped)
	} else if resp.Error == "" {
		resp.Error = err.Error()
	}

	for _, entry := range entries {
		resp.Summary.Entries++
		dto := registryEntryDTO{
			Owner:        entry.Owner,
			Namespace:    entry.Namespace,
			Kind:         entry.Kind,
			Path:         entry.Path,
			RegisteredAt: entry.RegisteredAt,
			Via:          entry.Via,
		}
		if report, ok := healthByOwner[entry.Owner]; ok {
			dto.HealthStatus = report.Status
			dto.HealthDetail = report.Detail
			if report.Healthy() {
				resp.Summary.Healthy++
			} else {
				resp.Summary.Unhealthy++
			}
		}
		if pc, err := registry.LoadProjectConfig(entry.Path); err == nil {
			dto.ProjectID = pc.Project.ID
			dto.ProjectName = pc.Project.Name
			dto.ResourceCount = len(pc.Resources)
			dto.PipelineCount = len(pc.Pipelines)
			dto.RegistryURN = pc.RegistryURN
			dto.SharedIdentity = strings.TrimSpace(pc.RegistryURN) != ""
			if dto.SharedIdentity {
				resp.Summary.Shared++
			} else {
				resp.Summary.LocalOnly++
			}
		}
		resp.Entries = append(resp.Entries, dto)
	}
	sort.Slice(resp.Entries, func(i, j int) bool { return resp.Entries[i].Owner < resp.Entries[j].Owner })
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRegistryHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	reg, err := registry.ForConfig(s.configPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, registryHealthResponse{Error: err.Error()})
		return
	}
	reports, err := reg.Health()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, registryHealthResponse{Error: err.Error()})
		return
	}

	resp := registryHealthResponse{
		Reports: make([]registryHealthDTO, 0, len(reports)),
	}
	for _, report := range reports {
		resp.Summary.Entries++
		if report.Healthy() {
			resp.Summary.Healthy++
		} else {
			resp.Summary.Unhealthy++
		}
		resp.Reports = append(resp.Reports, registryHealthDTO{
			Owner:  report.Owner,
			Path:   report.Path,
			Status: report.Status,
			Detail: report.Detail,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRegistryRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.allowStateChangingRequest(r) {
		writeError(w, http.StatusForbidden, "state-changing request rejected")
		return
	}
	var req registryRegisterRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	reg, err := registry.ForConfig(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	entries, err := reg.Register(strings.TrimSpace(req.Path))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"count":   len(entries),
	})
}

func (s *Server) handleRegistryDeregister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.allowStateChangingRequest(r) {
		writeError(w, http.StatusForbidden, "state-changing request rejected")
		return
	}
	var req registryDeregisterRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	owner := strings.TrimSpace(req.Owner)
	if owner == "" {
		writeError(w, http.StatusBadRequest, "owner is required")
		return
	}
	reg, err := registry.ForConfig(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := reg.Deregister(owner); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Server) handleConfigValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	resp, err := s.configValidationResponse()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, configValidationResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleConfigResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	indexPath, err := registry.IndexPathFor(s.configPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, configResolveResponse{Error: err.Error()})
		return
	}
	resp := configResolveResponse{
		ConfigPath: s.configPath,
		IndexPath:  indexPath,
	}
	if global := validateGlobalConfig(s.configPath); global != nil {
		resp.Global = global
	}
	resolved, err := registry.Resolve(registry.ResolveOptions{IndexPath: indexPath, GlobalPath: s.configPath})
	if err != nil {
		resp.Error = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Projects = len(resolved.Config.Projects)
	resp.Resources = len(resolved.Config.Resources)
	resp.Pipelines = len(resolved.Config.Pipelines)
	resp.Warnings = resolved.Warnings
	resp.Skipped = make([]registryHealthDTO, 0, len(resolved.Skipped))
	for _, skipped := range resolved.Skipped {
		resp.Skipped = append(resp.Skipped, registryHealthDTO{
			Owner:  skipped.Owner,
			Path:   skipped.Path,
			Status: skipped.Status,
			Detail: skipped.Detail,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleConfigMigratePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	preview, err := configops.PreviewMigration(s.configPath)
	if err != nil {
		writeJSON(w, http.StatusOK, configMigratePreviewResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, previewResponse(preview))
}

func (s *Server) handleConfigMigrate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.allowStateChangingRequest(r) {
		writeError(w, http.StatusForbidden, "state-changing request rejected")
		return
	}

	preview, err := configops.PreviewMigration(s.configPath)
	if err != nil {
		writeJSON(w, http.StatusOK, configMigrateResponse{Success: false, Error: err.Error()})
		return
	}
	if len(preview.ValidationErrors) > 0 {
		writeJSON(w, http.StatusOK, configMigrateResponse{
			Success:          false,
			Error:            "migration preview has validation errors",
			ValidationErrors: preview.ValidationErrors,
			TotalResources:   preview.TotalResources,
		})
		return
	}
	result, err := configops.MigrateConfig(s.configPath)
	if err != nil {
		writeJSON(w, http.StatusOK, configMigrateResponse{Success: false, Error: err.Error(), TotalResources: preview.TotalResources})
		return
	}
	writeJSON(w, http.StatusOK, configMigrateResponse{
		Success:          true,
		ProjectsDir:      result.ProjectsDir,
		BackupPath:       result.BackupPath,
		Warnings:         result.Warnings,
		WrittenPaths:     result.WrittenPaths,
		RegisteredOwners: result.RegisteredOwners,
		TotalResources:   result.TotalResources,
	})
}

func (s *Server) handleConfigBackups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	backups, err := configops.ListConfigBackups(s.configPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, configBackupResponse{Error: err.Error()})
		return
	}
	resp := configBackupResponse{Backups: make([]configBackupDTO, 0, len(backups))}
	for _, backup := range backups {
		resp.Backups = append(resp.Backups, configBackupDTO{
			Name:     backup.Name,
			Path:     backup.Path,
			Size:     backup.Size,
			Modified: backup.ModTime.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleConfigRestoreBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.allowStateChangingRequest(r) {
		writeError(w, http.StatusForbidden, "state-changing request rejected")
		return
	}
	var req configRestoreRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := configops.RestoreConfigBackup(s.configPath, strings.TrimSpace(req.BackupPath))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":          true,
		"backup_path":      result.BackupPath,
		"restored_to":      result.RestoredTo,
		"pre_restore_path": result.PreRestorePath,
	})
}

func (s *Server) registrySummary() (overviewRegistryDTO, error) {
	reg, err := registry.ForConfig(s.configPath)
	if err != nil {
		return overviewRegistryDTO{}, err
	}
	entries, err := reg.List()
	if err != nil {
		return overviewRegistryDTO{}, err
	}
	reports, err := reg.Health()
	if err != nil {
		return overviewRegistryDTO{}, err
	}
	out := overviewRegistryDTO{Entries: len(entries)}
	for _, report := range reports {
		if report.Healthy() {
			out.Healthy++
		} else {
			out.Unhealthy++
		}
	}
	return out, nil
}

func summarizeRuntime(resources []cerbapi.ResourceHealth) overviewRuntimeDTO {
	out := overviewRuntimeDTO{}
	for _, resource := range resources {
		status := strings.ToLower(resource.Status)
		switch {
		case resource.OperatorStopped || status == "stopped":
			out.Stopped++
		case resource.RecommendedAction != "" || !resource.Healthy || status == "failed" || status == "error" || status == "degraded":
			out.Attention++
		case status == "running" || status == "healthy":
			out.Running++
		default:
			out.Stopped++
		}
	}
	return out
}

func summarizeResourceInfos(resources []cerbapi.ResourceInfo) overviewRuntimeDTO {
	out := overviewRuntimeDTO{}
	for _, resource := range resources {
		status := strings.ToLower(resource.Status)
		switch {
		case resource.OperatorStopped || status == "stopped":
			out.Stopped++
		case resource.ArtifactStale || resource.RecommendedAction != "" || status == "failed" || status == "error" || status == "degraded":
			out.Attention++
		case status == "running" || status == "healthy":
			out.Running++
		default:
			out.Stopped++
		}
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(filepath.Clean(path))
	return err == nil
}

func (s *Server) configValidationResponse() (configValidationResponse, error) {
	reg, err := registry.ForConfig(s.configPath)
	if err != nil {
		return configValidationResponse{}, err
	}
	entries, err := reg.List()
	if err != nil {
		return configValidationResponse{}, err
	}

	resp := configValidationResponse{
		Registered: make([]configValidationFileDTO, 0, len(entries)),
	}
	if global := validateGlobalConfig(s.configPath); global != nil {
		resp.Global = global
		accumulateValidationSummary(&resp.Summary, *global)
	}
	for _, entry := range entries {
		dto := validateRegisteredEntry(entry)
		resp.Registered = append(resp.Registered, dto)
		accumulateValidationSummary(&resp.Summary, dto)
	}
	return resp, nil
}

func validateGlobalConfig(path string) *configValidationFileDTO {
	if path == "" || !fileExists(path) {
		return nil
	}
	dto := &configValidationFileDTO{
		Path: path,
		Kind: "config.yaml",
		OK:   true,
	}
	if _, err := config.LoadUnified(path); err != nil {
		dto.OK = false
		dto.Errors = []string{err.Error()}
	}
	return dto
}

func validateRegisteredEntry(entry registry.IndexEntry) configValidationFileDTO {
	dto := configValidationFileDTO{
		Path:  entry.Path,
		Kind:  entry.Kind,
		Owner: entry.Owner,
		OK:    true,
	}
	pc, err := registry.LoadProjectConfig(entry.Path)
	if err != nil {
		dto.OK = false
		dto.Errors = []string{err.Error()}
		return dto
	}
	result := registry.ValidateProjectConfig(pc)
	dto.Warnings = validationMessages(result.Warnings())
	dto.Errors = validationMessages(result.Errors())
	dto.OK = len(dto.Errors) == 0
	return dto
}

func validationMessages(issues []registry.ValidationIssue) []string {
	if len(issues) == 0 {
		return nil
	}
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		out = append(out, fmt.Sprintf("%s: %s", issue.Field, issue.Message))
	}
	return out
}

func accumulateValidationSummary(summary *configValidationSummaryDTO, file configValidationFileDTO) {
	summary.Files++
	if file.OK {
		summary.Valid++
	} else {
		summary.Invalid++
	}
	summary.Warnings += len(file.Warnings)
	summary.Errors += len(file.Errors)
}

func previewResponse(preview *configops.MigrationPreview) configMigratePreviewResponse {
	resp := configMigratePreviewResponse{
		ConfigPath:       preview.ConfigPath,
		ProjectsDir:      preview.ProjectsDir,
		BackupPath:       preview.BackupPath,
		ProjectCount:     len(preview.ProjectConfigs),
		TotalResources:   preview.TotalResources,
		Warnings:         preview.Warnings,
		ValidationErrors: preview.ValidationErrors,
		Entries:          make([]configMigrateEntryDTO, 0, len(preview.ProjectConfigs)),
	}
	for _, pc := range preview.ProjectConfigs {
		resp.Entries = append(resp.Entries, configMigrateEntryDTO{
			Owner:         pc.Owner,
			ProjectID:     pc.Project.ID,
			ProjectName:   pc.Project.Name,
			ResourceCount: len(pc.Resources),
			PipelineCount: len(pc.Pipelines),
			Destination:   filepath.Join(preview.ProjectsDir, pc.Owner+registry.FileSuffix),
		})
	}
	return resp
}

func (r overviewRuntimeDTO) String() string {
	return fmt.Sprintf("running=%d attention=%d stopped=%d", r.Running, r.Attention, r.Stopped)
}
