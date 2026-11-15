package api

import (
	"encoding/json"
	"time"

	"github.com/hollis-labs/cerberus/internal/hosts"
)

// The legacy-route shapes (docs/substrate-port/legacy-routes.md). Each type has the fields, JSON
// names and omitempty rules of the old build's internal/cerbapi type of the same name (dto.go
// and host_service.go on main), so tachyon and tether sysop read the same bodies. The golden
// tests in legacy_golden_test.go hold them to bodies generated from the old types.

// ActivationArtifact is the old connector/local type of the same name.
type ActivationArtifact struct {
	Path       string    `json:"path"`
	SHA256     string    `json:"sha256"`
	ModifiedAt time.Time `json:"modified_at"`
}

// HostView is the old cerbapi.HostView: declared and probed data side by side.
type HostView struct {
	Host            hosts.Host                 `json:"host"`
	State           string                     `json:"state"`
	OnboardingStage string                     `json:"onboarding_stage,omitempty"`
	Reason          string                     `json:"reason,omitempty"`
	LastSeen        *time.Time                 `json:"last_seen,omitempty"`
	ProbedAt        *time.Time                 `json:"probed_at,omitempty"`
	CheckedAt       *time.Time                 `json:"checked_at,omitempty"`
	Stale           bool                       `json:"stale"`
	Probed          *hosts.Probed              `json:"probed,omitempty"`
	Ceilings        *hosts.Ceilings            `json:"ceilings"`
	StorageSlots    []hosts.StorageSlot        `json:"storage_slots"`
	StorageSummary  map[string]hosts.SlotCount `json:"storage_summary"`
	Components      []hosts.Component          `json:"components,omitempty"`
	Warnings        []string                   `json:"warnings,omitempty"`
	Status          *hosts.HostStatus          `json:"status,omitempty"`
}

type ResourceInfo struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Type                string   `json:"type"`
	Project             string   `json:"project"`
	Connector           string   `json:"connector"`
	Host                string   `json:"host,omitempty"`
	Mode                string   `json:"mode,omitempty"`
	Supervisor          string   `json:"supervisor,omitempty"`
	RunFrom             string   `json:"run_from,omitempty"`
	URL                 string   `json:"url,omitempty"`
	Port                int      `json:"port,omitempty"`
	HasBuild            bool     `json:"has_build,omitempty"`
	Status              string   `json:"status,omitempty"`
	OperatorStopped     bool     `json:"operator_stopped,omitempty"`
	ArtifactInstalled   bool     `json:"artifact_installed,omitempty"`
	ArtifactStale       bool     `json:"artifact_stale,omitempty"`
	RecommendedAction   string   `json:"recommended_action,omitempty"`
	RecommendedNextStep string   `json:"recommended_next_step,omitempty"`
	Tags                []string `json:"tags,omitempty"`
	Env                 string   `json:"env"`
	Owner               string   `json:"owner"`
	Admin               string   `json:"admin"`
}

type ResourceRuntimeStatus struct {
	DependencyWarnings   []string `json:"dependency_warnings,omitempty"`
	ConfigWarnings       []string `json:"config_warnings,omitempty"`
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Type                 string   `json:"type"`
	Project              string   `json:"project"`
	Connector            string   `json:"connector"`
	Mode                 string   `json:"mode,omitempty"`
	Supervisor           string   `json:"supervisor,omitempty"`
	RunFrom              string   `json:"run_from,omitempty"`
	URL                  string   `json:"url,omitempty"`
	Port                 int      `json:"port,omitempty"`
	HasBuild             bool     `json:"has_build,omitempty"`
	Status               string   `json:"status"`
	OperatorStopped      bool     `json:"operator_stopped,omitempty"`
	ServiceName          string   `json:"service_name,omitempty"`
	ArtifactPath         string   `json:"artifact_path,omitempty"`
	InstallRoot          string   `json:"install_root,omitempty"`
	ArtifactInstalled    bool     `json:"artifact_installed,omitempty"`
	ArtifactStale        bool     `json:"artifact_stale,omitempty"`
	ArtifactStaleReason  string   `json:"artifact_stale_reason,omitempty"`
	ArtifactSource       string   `json:"artifact_source,omitempty"`
	ArtifactSyncedAt     string   `json:"artifact_synced_at,omitempty"`
	RecommendedAction    string   `json:"recommended_action,omitempty"`
	RecommendedReason    string   `json:"recommended_reason,omitempty"`
	RecommendedNextStep  string   `json:"recommended_next_step,omitempty"`
	LaunchdLoaded        bool     `json:"launchd_loaded,omitempty"`
	LaunchdState         string   `json:"launchd_state,omitempty"`
	LaunchdPID           int      `json:"launchd_pid,omitempty"`
	LaunchdLastExitCode  *int     `json:"launchd_last_exit_code,omitempty"`
	LaunchdThrottled     bool     `json:"launchd_throttled,omitempty"`
	LaunchdReason        string   `json:"launchd_reason,omitempty"`
	LaunchdDiagnosis     string   `json:"launchd_diagnosis,omitempty"`
	LaunchdHighlights    []string `json:"launchd_highlights,omitempty"`
	SystemdLoadState     string   `json:"systemd_load_state,omitempty"`
	SystemdActiveState   string   `json:"systemd_active_state,omitempty"`
	SystemdSubState      string   `json:"systemd_sub_state,omitempty"`
	SystemdUnitFileState string   `json:"systemd_unit_file_state,omitempty"`
	SystemdResult        string   `json:"systemd_result,omitempty"`
	SystemdPID           int      `json:"systemd_pid,omitempty"`
	SystemdExitStatus    *int     `json:"systemd_exit_status,omitempty"`
	SystemdRestarts      int      `json:"systemd_restarts,omitempty"`
	SystemdDiagnosis     string   `json:"systemd_diagnosis,omitempty"`
	SystemdHighlights    []string `json:"systemd_highlights,omitempty"`
}

type ResourceInspect struct {
	DependencyWarnings   []string `json:"dependency_warnings,omitempty"`
	ConfigWarnings       []string `json:"config_warnings,omitempty"`
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Type                 string   `json:"type"`
	Project              string   `json:"project"`
	Connector            string   `json:"connector"`
	Mode                 string   `json:"mode,omitempty"`
	Supervisor           string   `json:"supervisor,omitempty"`
	RunFrom              string   `json:"run_from,omitempty"`
	URL                  string   `json:"url,omitempty"`
	Port                 int      `json:"port,omitempty"`
	Status               string   `json:"status,omitempty"`
	OperatorStopped      bool     `json:"operator_stopped,omitempty"`
	WorkspaceDir         string   `json:"workspace_dir,omitempty"`
	WorkingDir           string   `json:"working_dir,omitempty"`
	Command              []string `json:"command,omitempty"`
	BuildStrategy        string   `json:"build_strategy,omitempty"`
	ServiceName          string   `json:"service_name,omitempty"`
	PlistPath            string   `json:"plist_path,omitempty"`
	InstallRoot          string   `json:"install_root,omitempty"`
	InstallWorkDir       string   `json:"install_work_dir,omitempty"`
	BinDir               string   `json:"bin_dir,omitempty"`
	ArtifactPath         string   `json:"artifact_path,omitempty"`
	ArtifactInstalled    bool     `json:"artifact_installed,omitempty"`
	ArtifactStale        bool     `json:"artifact_stale,omitempty"`
	ArtifactStaleReason  string   `json:"artifact_stale_reason,omitempty"`
	ArtifactSource       string   `json:"artifact_source,omitempty"`
	ArtifactSyncedAt     string   `json:"artifact_synced_at,omitempty"`
	StdoutLogPath        string   `json:"stdout_log_path,omitempty"`
	StderrLogPath        string   `json:"stderr_log_path,omitempty"`
	RecommendedAction    string   `json:"recommended_action,omitempty"`
	RecommendedReason    string   `json:"recommended_reason,omitempty"`
	RecommendedNextStep  string   `json:"recommended_next_step,omitempty"`
	LaunchdLoaded        bool     `json:"launchd_loaded,omitempty"`
	LaunchdState         string   `json:"launchd_state,omitempty"`
	LaunchdPID           int      `json:"launchd_pid,omitempty"`
	LaunchdLastExitCode  *int     `json:"launchd_last_exit_code,omitempty"`
	LaunchdThrottled     bool     `json:"launchd_throttled,omitempty"`
	LaunchdReason        string   `json:"launchd_reason,omitempty"`
	LaunchdDiagnosis     string   `json:"launchd_diagnosis,omitempty"`
	LaunchdHighlights    []string `json:"launchd_highlights,omitempty"`
	LaunchdRaw           string   `json:"launchd_raw,omitempty"`
	UnitPath             string   `json:"unit_path,omitempty"`
	SystemdLoadState     string   `json:"systemd_load_state,omitempty"`
	SystemdActiveState   string   `json:"systemd_active_state,omitempty"`
	SystemdSubState      string   `json:"systemd_sub_state,omitempty"`
	SystemdUnitFileState string   `json:"systemd_unit_file_state,omitempty"`
	SystemdResult        string   `json:"systemd_result,omitempty"`
	SystemdPID           int      `json:"systemd_pid,omitempty"`
	SystemdExitStatus    *int     `json:"systemd_exit_status,omitempty"`
	SystemdRestarts      int      `json:"systemd_restarts,omitempty"`
	SystemdDiagnosis     string   `json:"systemd_diagnosis,omitempty"`
	SystemdHighlights    []string `json:"systemd_highlights,omitempty"`
	SystemdRaw           string   `json:"systemd_raw,omitempty"`
	SystemdJournal       string   `json:"systemd_journal,omitempty"`
}

type ResourceDoctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type ResourceDoctor struct {
	ResourceID          string                `json:"resource_id"`
	Status              string                `json:"status,omitempty"`
	OperatorStopped     bool                  `json:"operator_stopped,omitempty"`
	Summary             string                `json:"summary"`
	RecommendedAction   string                `json:"recommended_action,omitempty"`
	RecommendedReason   string                `json:"recommended_reason,omitempty"`
	RecommendedNextStep string                `json:"recommended_next_step,omitempty"`
	Checks              []ResourceDoctorCheck `json:"checks"`
}

type LogLines struct {
	ServiceID  string `json:"service_id,omitempty"`
	ResourceID string `json:"resource_id,omitempty"`
	Stream     string `json:"stream,omitempty"`
	Content    string `json:"content"`
	LogPath    string `json:"log_path"`
}

type OpResult struct {
	Warnings       []string            `json:"warnings,omitempty"`
	BuildPerformed bool                `json:"build_performed"`
	Activation     *ActivationArtifact `json:"activation,omitempty"`
	Success        bool                `json:"success"`
	ServiceID      string              `json:"service_id"`
	Message        string              `json:"message,omitempty"`
	BuildOutput    string              `json:"build_output,omitempty"`
	BuildLogPath   string              `json:"build_log_path,omitempty"`
	InstallOutput  string              `json:"install_output,omitempty"`
	InstallSkipped bool                `json:"install_skipped,omitempty"`
	Error          string              `json:"error,omitempty"`
	Plan           json.RawMessage     `json:"plan,omitempty"`
}

type ResourceHealth struct {
	ResourceID          string `json:"resource_id"`
	Status              string `json:"status"`
	Healthy             bool   `json:"healthy"`
	OperatorStopped     bool   `json:"operator_stopped,omitempty"`
	Mode                string `json:"mode,omitempty"`
	Supervisor          string `json:"supervisor,omitempty"`
	RunFrom             string `json:"run_from,omitempty"`
	ArtifactInstalled   bool   `json:"artifact_installed,omitempty"`
	ArtifactStale       bool   `json:"artifact_stale,omitempty"`
	RecommendedAction   string `json:"recommended_action,omitempty"`
	RecommendedReason   string `json:"recommended_reason,omitempty"`
	RecommendedNextStep string `json:"recommended_next_step,omitempty"`
}

type ServiceHealth struct {
	ServiceID           string `json:"service_id"`
	HealthConfigured    bool   `json:"health_configured"`
	Healthy             bool   `json:"healthy"`
	LastCheck           string `json:"last_check,omitempty"`
	LastError           string `json:"last_error,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
}

type DaemonHealth struct {
	Services          []ServiceHealth  `json:"services"`
	Resources         []ResourceHealth `json:"resources,omitempty"`
	DaemonRunning     bool             `json:"daemon_running"`
	MonitorInterval   string           `json:"monitor_interval,omitempty"`
	ServicesProtected int              `json:"services_protected"`
	ServicesFailed    int              `json:"services_failed"`
}
