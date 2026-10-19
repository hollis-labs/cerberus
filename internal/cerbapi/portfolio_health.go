package cerbapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/cerberus/internal/audit"
	"github.com/hollis-labs/cerberus/internal/config"
	dockerconn "github.com/hollis-labs/cerberus/internal/connector/docker"
	localconn "github.com/hollis-labs/cerberus/internal/connector/local"
	"github.com/hollis-labs/cerberus/internal/domain"
	"github.com/hollis-labs/cerberus/internal/hosts"
	"github.com/hollis-labs/cerberus/internal/pausectl"
	"github.com/hollis-labs/cerberus/internal/policy"
	"github.com/hollis-labs/cerberus/internal/redact"
	"github.com/hollis-labs/cerberus/internal/target"
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/outpost"
)

const (
	PortfolioHealthSchemaVersion = 1
	PortfolioHealthContract      = "cerberus.portfolio.health.v1"

	DefaultControllerID     = "agent-os"
	DefaultProviderInstance = "cerberus-local"

	PortfolioModeCached      = "cached"
	PortfolioModeLiveBounded = "live_bounded"

	RollupHealthy         = "healthy"
	RollupDegraded        = "degraded"
	RollupDown            = "down"
	RollupUnknown         = "unknown"
	RollupExpectedStopped = "expected_stopped"
	RollupUnsupervised    = "unsupervised"
	RollupStale           = "stale"

	ReadinessReady         = "ready"
	ReadinessDegraded      = "degraded"
	ReadinessUnready       = "unready"
	ReadinessNotApplicable = "not_applicable"
	ReadinessUnknown       = "unknown"

	defaultReadinessTimeout = 750 * time.Millisecond
	defaultDockerTimeout    = 1000 * time.Millisecond
	defaultProbeCacheTTL    = 15 * time.Second
	defaultValidForSeconds  = 30
)

// PortfolioHealthQuery filters or tunes the portfolio health read projection.
type PortfolioHealthQuery struct {
	Mode                string `json:"mode,omitempty"`
	Project             string `json:"project,omitempty"`
	Host                string `json:"host,omitempty"`
	Kind                string `json:"kind,omitempty"`
	RollupStatus        string `json:"rollup_status,omitempty"`
	IncludeUnsupervised *bool  `json:"include_unsupervised,omitempty"`
}

// PortfolioHealthProjection is the canonical cerberus.portfolio.health.v1
// read-only projection consumed by Tachyon's status page and Basalisk's
// monitoring ingest.
type PortfolioHealthProjection struct {
	SchemaVersion    int                         `json:"schema_version"`
	Contract         string                      `json:"contract"`
	ControllerID     string                      `json:"controller_id"`
	ProviderInstance string                      `json:"provider_instance"`
	GeneratedAt      string                      `json:"generated_at"`
	ValidForSeconds  int                         `json:"valid_for_seconds"`
	CollectionMode   string                      `json:"collection_mode"`
	Authorization    PortfolioAuthorization      `json:"authorization"`
	Coverage         PortfolioCoverage           `json:"coverage"`
	Summary          PortfolioSummary            `json:"summary"`
	Hosts            []PortfolioHostHealth       `json:"hosts"`
	Projects         []PortfolioProjectHealth    `json:"projects"`
	Deployments      []PortfolioDeploymentHealth `json:"deployments"`
	Connectors       []PortfolioConnectorHealth  `json:"connectors"`
}

type PortfolioAuthorization struct {
	PrincipalKind string `json:"principal_kind"`
	Surface       string `json:"surface"`
	Scope         string `json:"scope"`
	Filtered      bool   `json:"filtered"`
}

type PortfolioCoverage struct {
	SupervisionScope      string   `json:"supervision_scope"`
	HostsDeclared         int      `json:"hosts_declared"`
	HostsProbedFresh      int      `json:"hosts_probed_fresh"`
	HostsStale            int      `json:"hosts_stale"`
	HostsDeferred         int      `json:"hosts_deferred"`
	ResourcesDeclared     int      `json:"resources_declared"`
	ResourcesSupervised   int      `json:"resources_supervised"`
	ResourcesUnsupervised int      `json:"resources_unsupervised"`
	RegistrySkipped       int      `json:"registry_skipped"`
	RegistryWarned        int      `json:"registry_warned"`
	SkippedOwners         []string `json:"skipped_owners,omitempty"`
	WarnedOwners          []string `json:"warned_owners,omitempty"`
}

type PortfolioSummary struct {
	OverallStatus     string                   `json:"overall_status"`
	ByStatus          map[string]int           `json:"by_status"`
	HostsByStatus     map[string]int           `json:"hosts_by_status"`
	ProjectsByStatus  map[string]int           `json:"projects_by_status"`
	AttentionRequired []PortfolioAttentionItem `json:"attention_required"`
}

type PortfolioAttentionItem struct {
	TargetType          string `json:"target_type"`
	ID                  string `json:"id"`
	ProjectID           string `json:"project_id,omitempty"`
	Host                string `json:"host,omitempty"`
	RollupStatus        string `json:"rollup_status"`
	Reason              string `json:"reason"`
	RecommendedAction   string `json:"recommended_action,omitempty"`
	RecommendedNextStep string `json:"recommended_next_step,omitempty"`
}

type PortfolioHostPlatform struct {
	OS        string `json:"os,omitempty"`
	Arch      string `json:"arch,omitempty"`
	OSVersion string `json:"os_version,omitempty"`
	Kernel    string `json:"kernel,omitempty"`
}

type PortfolioHostCapacity struct {
	CPU                  int             `json:"cpu,omitempty"`
	CPUModel             string          `json:"cpu_model,omitempty"`
	MemoryTotalBytes     int64           `json:"memory_total_bytes,omitempty"`
	MemoryAvailableBytes int64           `json:"memory_available_bytes,omitempty"`
	DiskMount            string          `json:"disk_mount,omitempty"`
	DiskTotalBytes       int64           `json:"disk_total_bytes,omitempty"`
	DiskFreeBytes        int64           `json:"disk_free_bytes,omitempty"`
	Load1                *float64        `json:"load1,omitempty"`
	UptimeSeconds        int64           `json:"uptime_seconds,omitempty"`
	Tools                map[string]bool `json:"tools,omitempty"`
}

type PortfolioHostHealth struct {
	Name            string                 `json:"name"`
	Role            string                 `json:"role,omitempty"`
	Provider        string                 `json:"provider,omitempty"`
	Platform        PortfolioHostPlatform  `json:"platform"`
	Transport       string                 `json:"transport"`
	Lifecycle       string                 `json:"lifecycle"`
	Reachability    string                 `json:"reachability"`
	Readiness       string                 `json:"readiness"`
	ReadinessSource string                 `json:"readiness_source"`
	ReadinessDetail string                 `json:"readiness_detail,omitempty"`
	RollupStatus    string                 `json:"rollup_status"`
	Stale           bool                   `json:"stale"`
	LastSeen        *time.Time             `json:"last_seen,omitempty"`
	ProbedAt        *time.Time             `json:"probed_at,omitempty"`
	CheckedAt       *time.Time             `json:"checked_at,omitempty"`
	AgeSeconds      *int64                 `json:"age_seconds,omitempty"`
	Capacity        *PortfolioHostCapacity `json:"capacity,omitempty"`
	Reason          string                 `json:"reason,omitempty"`
	Labels          map[string]string      `json:"labels,omitempty"`
}

type PortfolioProjectHealth struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Description    string         `json:"description,omitempty"`
	RollupStatus   string         `json:"rollup_status"`
	ResourceCounts map[string]int `json:"resource_counts"`
	Capabilities   []string       `json:"capabilities,omitempty"`
	Links          []config.Link  `json:"links,omitempty"`
}

type PortfolioDeploymentLabels struct {
	Env   string `json:"env"`
	Owner string `json:"owner"`
	Admin string `json:"admin"`
}

type PortfolioProvenance struct {
	Source     string `json:"source"`
	ObservedAt string `json:"observed_at"`
	AgeSeconds int64  `json:"age_seconds"`
	Stale      bool   `json:"stale"`
}

type PortfolioDeploymentHealth struct {
	ID                  string                    `json:"id"`
	Name                string                    `json:"name"`
	ProjectID           string                    `json:"project_id"`
	Host                string                    `json:"host"`
	Kind                string                    `json:"kind"`
	ResourceType        string                    `json:"resource_type"`
	Connector           string                    `json:"connector"`
	Mode                string                    `json:"mode,omitempty"`
	Supervisor          string                    `json:"supervisor,omitempty"`
	RunFrom             string                    `json:"run_from,omitempty"`
	Supervision         string                    `json:"supervision"`
	DesiredState        string                    `json:"desired_state"`
	ObservedState       string                    `json:"observed_state"`
	Readiness           string                    `json:"readiness"`
	ReadinessSource     string                    `json:"readiness_source"`
	ReadinessDetail     string                    `json:"readiness_detail,omitempty"`
	RollupStatus        string                    `json:"rollup_status"`
	OperatorStopped     bool                      `json:"operator_stopped,omitempty"`
	ArtifactInstalled   bool                      `json:"artifact_installed,omitempty"`
	ArtifactStale       bool                      `json:"artifact_stale,omitempty"`
	ArtifactStaleReason string                    `json:"artifact_stale_reason,omitempty"`
	ConfigWarnings      []string                  `json:"config_warnings,omitempty"`
	DependencyWarnings  []string                  `json:"dependency_warnings,omitempty"`
	URL                 string                    `json:"url,omitempty"`
	Port                int                       `json:"port,omitempty"`
	ServiceName         string                    `json:"service_name,omitempty"`
	RecommendedAction   string                    `json:"recommended_action,omitempty"`
	RecommendedReason   string                    `json:"recommended_reason,omitempty"`
	RecommendedNextStep string                    `json:"recommended_next_step,omitempty"`
	Labels              PortfolioDeploymentLabels `json:"labels"`
	Tags                []string                  `json:"tags,omitempty"`
	Provenance          PortfolioProvenance       `json:"provenance"`
}

type PortfolioConnectorHealth struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Version      string `json:"version,omitempty"`
	Live         bool   `json:"live"`
	RollupStatus string `json:"rollup_status"`
	Detail       string `json:"detail,omitempty"`
}

// PortfolioHealthClient is the optional client interface for portfolio health
// projections, kept separate from Client so older test fakes continue compiling.
type PortfolioHealthClient interface {
	PortfolioHealth(ctx context.Context, q PortfolioHealthQuery) (*PortfolioHealthProjection, error)
}

// PortfolioHealthClientOf returns client as a PortfolioHealthClient, or one
// that refuses every call with an explicit guidance error.
func PortfolioHealthClientOf(client any) PortfolioHealthClient {
	if p, ok := client.(PortfolioHealthClient); ok {
		return p
	}
	return unavailablePortfolioHealthClient{}
}

var errPortfolioHealthUnavailable = redact.Guidance("portfolio health is not available from this serving host; run the daemon from this build")

type unavailablePortfolioHealthClient struct{}

func (unavailablePortfolioHealthClient) PortfolioHealth(context.Context, PortfolioHealthQuery) (*PortfolioHealthProjection, error) {
	return nil, errPortfolioHealthUnavailable
}

// PortfolioTargetFilter decides whether a caller principal is permitted to view
// a specific target (host, project, deployment, or connector) before row
// emission and before summary aggregation.
type PortfolioTargetFilter func(ctx context.Context, targetType, id string, labels target.Labels) bool

// DockerInspector abstracts read-only Docker container status inspection for
// shared_infra container resources.
type DockerInspector interface {
	ContainerStatus(ctx context.Context, nameOrID string) (*dockerconn.Container, error)
	ComposePS(ctx context.Context, composeFile string) (*dockerconn.ComposeStack, error)
}

type defaultDockerInspector struct{}

func (defaultDockerInspector) ContainerStatus(ctx context.Context, nameOrID string) (*dockerconn.Container, error) {
	b, err := dockerconn.NewCLIBackend()
	if err != nil {
		return nil, err
	}
	return b.ContainerStatus(ctx, nameOrID)
}

func (defaultDockerInspector) ComposePS(ctx context.Context, composeFile string) (*dockerconn.ComposeStack, error) {
	b, err := dockerconn.NewCLIBackend()
	if err != nil {
		return nil, err
	}
	return b.ComposePS(ctx, composeFile)
}

// PortfolioHealthOptions configures a PortfolioHealthService.
type PortfolioHealthOptions struct {
	ControllerID     string
	ProviderInstance string
	Now              func() time.Time
	HTTPClient       *http.Client
	Docker           DockerInspector
	TargetFilter     PortfolioTargetFilter
	CacheTTL         time.Duration
}

type cachedReadiness struct {
	at              time.Time
	reachability    string
	readiness       string
	readinessSource string
	readinessDetail string
}

type cachedDockerState struct {
	at              time.Time
	observedState   string
	readiness       string
	readinessSource string
	readinessDetail string
}

type cachedUnitMap struct {
	at    time.Time
	units map[string]string
}

// PortfolioHealthService computes the combined portfolio health projection
// across local resources, declared hosts, shared Docker containers, and
// connectors.
type PortfolioHealthService struct {
	audit            audit.Sink
	runtime          *ResourceRuntimeService
	hosts            *HostService
	external         *ExternalConnectorService
	managedPlugins   *ManagedPluginConnectorService
	controllerID     string
	providerInstance string
	now              func() time.Time
	httpClient       *http.Client
	docker           DockerInspector
	targetFilter     PortfolioTargetFilter
	cacheTTL         time.Duration

	mu             sync.Mutex
	readinessCache map[string]cachedReadiness
	dockerCache    map[string]cachedDockerState
	unitCache      map[string]cachedUnitMap
}

// NewPortfolioHealthService constructs a PortfolioHealthService.
func NewPortfolioHealthService(
	sink audit.Sink,
	runtime *ResourceRuntimeService,
	hostSvc *HostService,
	external *ExternalConnectorService,
	managedPlugins *ManagedPluginConnectorService,
	opts ...PortfolioHealthOptions,
) *PortfolioHealthService {
	var o PortfolioHealthOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.ControllerID == "" {
		o.ControllerID = DefaultControllerID
	}
	if o.ProviderInstance == "" {
		o.ProviderInstance = DefaultProviderInstance
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: defaultReadinessTimeout}
	}
	if o.Docker == nil {
		o.Docker = defaultDockerInspector{}
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = defaultProbeCacheTTL
	}
	if sink == nil {
		sink = audit.Unavailable{Err: errors.New("no audit sink configured for portfolio health")}
	}
	return &PortfolioHealthService{
		audit:            sink,
		runtime:          runtime,
		hosts:            hostSvc,
		external:         external,
		managedPlugins:   managedPlugins,
		controllerID:     o.ControllerID,
		providerInstance: o.ProviderInstance,
		now:              o.Now,
		httpClient:       o.HTTPClient,
		docker:           o.Docker,
		targetFilter:     o.TargetFilter,
		cacheTTL:         o.CacheTTL,
		readinessCache:   map[string]cachedReadiness{},
		dockerCache:      map[string]cachedDockerState{},
		unitCache:        map[string]cachedUnitMap{},
	}
}

func portfolioHealthDefinition() contract.Definition {
	op := contract.Operation{
		Name:       "health",
		Effect:     contract.EffectRead,
		Reversible: true,
		Target:     contract.TargetDescriptor{Kind: "portfolio"},
		Preview:    contract.PreviewNone,
		Output:     contract.OutputStructured,
		Cost:       contract.CostNone,
		LocalFS:    contract.LocalFSNone,
	}.Finalize()
	return contract.Finalize(contract.Definition{
		ID:         "portfolio",
		Version:    "builtin",
		Operations: []contract.Operation{op},
	})
}

// PortfolioInvalidError indicates an invalid query parameter on a portfolio
// health request.
type PortfolioInvalidError struct{ Err error }

func (e *PortfolioInvalidError) Error() string { return e.Err.Error() }
func (e *PortfolioInvalidError) Unwrap() error { return e.Err }

func validatePortfolioQuery(q PortfolioHealthQuery) (PortfolioHealthQuery, error) {
	switch q.Mode {
	case "", PortfolioModeCached:
		q.Mode = PortfolioModeCached
	case PortfolioModeLiveBounded:
	default:
		return q, &PortfolioInvalidError{Err: redact.Guidance("mode must be %q or %q", PortfolioModeCached, PortfolioModeLiveBounded)}
	}
	switch q.Kind {
	case "", "local_service", "remote_service", "shared_infra", "connector_handle":
	default:
		return q, &PortfolioInvalidError{Err: redact.Guidance("kind must be local_service, remote_service, shared_infra, or connector_handle")}
	}
	switch q.RollupStatus {
	case "", RollupHealthy, RollupDegraded, RollupDown, RollupUnknown, RollupExpectedStopped, RollupUnsupervised, RollupStale:
	default:
		return q, &PortfolioInvalidError{Err: redact.Guidance("rollup_status must be one of healthy, degraded, down, unknown, expected_stopped, unsupervised, or stale")}
	}
	return q, nil
}

// Project computes the combined portfolio health projection after enforcing
// caller authentication, OAuth scopes, and target-level authorization.
func (s *PortfolioHealthService) Project(ctx context.Context, q PortfolioHealthQuery) (_ *PortfolioHealthProjection, retErr error) {
	if s == nil {
		return nil, errPortfolioHealthUnavailable
	}
	q, err := validatePortfolioQuery(q)
	if err != nil {
		return nil, err
	}
	ctx, scope := redact.EnsureScope(ctx)

	def := portfolioHealthDefinition()
	op, _ := def.Operation("health")
	var resLookup ResourceLookup
	if s.runtime != nil {
		resLookup = s.runtime.ResourceDef
	}
	spec := auditSpec{
		connector: "portfolio",
		operation: "health",
		op:        op,
		known:     true,
		config: map[string]any{
			"mode":          q.Mode,
			"project":       q.Project,
			"host":          q.Host,
			"kind":          q.Kind,
			"rollup_status": q.RollupStatus,
		},
		resources: resLookup,
	}
	call, err := beginGated(ctx, s.audit, nil, spec)
	if err != nil {
		return nil, err
	}
	defer func() { call.finish(retErr) }()

	now := s.now().UTC()
	authMeta := resolveAuthorizationMeta(ctx)
	filtered := false

	// 1. Gather and authorize hosts before computing host rollups or coverage.
	hostRows, hostMap, hostUnits, hostCov, hostFiltered := s.collectHosts(ctx, scope, now, q.Mode)
	if hostFiltered {
		filtered = true
	}

	// 2. Gather config snapshot and registry diagnostics.
	var cfg *config.ConfigV2
	var diag ResolveDiagnostics
	if s.runtime != nil {
		cfg = s.runtime.snapshotConfig()
		if d, dErr := s.runtime.ResolveDiagnostics(ctx); dErr == nil && d != nil {
			diag = *d
		}
	}

	// 3. Gather and authorize deployments (resources) before computing rollups or coverage.
	deployments, resCov, depFiltered := s.collectDeployments(ctx, scope, now, q.Mode, cfg, hostMap, hostUnits)
	if depFiltered {
		filtered = true
	}

	// 4. Gather and authorize projects based on authorized deployments.
	projects, projFiltered := s.collectProjects(ctx, scope, cfg, deployments, depFiltered)
	if projFiltered {
		filtered = true
	}

	// 5. Gather and authorize connectors.
	connectors, connFiltered := s.collectConnectors(ctx, scope)
	if connFiltered {
		filtered = true
	}

	// Apply caller query filters (project, host, kind, rollup_status) before
	// computing summary and returning rows.
	if q.Host != "" {
		filteredHosts := make([]PortfolioHostHealth, 0, len(hostRows))
		for _, h := range hostRows {
			if h.Name == q.Host {
				filteredHosts = append(filteredHosts, h)
			}
		}
		hostRows = filteredHosts
	}
	excludeUnsupervised := q.IncludeUnsupervised != nil && !*q.IncludeUnsupervised
	if q.Project != "" || q.Host != "" || q.Kind != "" || q.RollupStatus != "" || excludeUnsupervised {
		filteredDeps := make([]PortfolioDeploymentHealth, 0, len(deployments))
		for _, d := range deployments {
			if excludeUnsupervised && d.RollupStatus == RollupUnsupervised {
				continue
			}
			if q.Project != "" && d.ProjectID != q.Project {
				continue
			}
			if q.Host != "" && d.Host != q.Host {
				continue
			}
			if q.Kind != "" && d.Kind != q.Kind {
				continue
			}
			if q.RollupStatus != "" && d.RollupStatus != q.RollupStatus {
				continue
			}
			filteredDeps = append(filteredDeps, d)
		}
		deployments = filteredDeps
	}
	if q.Project != "" {
		filteredProjs := make([]PortfolioProjectHealth, 0, len(projects))
		for _, p := range projects {
			if p.ID == q.Project {
				filteredProjs = append(filteredProjs, p)
			}
		}
		projects = filteredProjs
	}

	authMeta.Filtered = filtered

	coverage := PortfolioCoverage{
		SupervisionScope:      "local_process_and_outpost_hosts",
		HostsDeclared:         hostCov.HostsDeclared,
		HostsProbedFresh:      hostCov.HostsProbedFresh,
		HostsStale:            hostCov.HostsStale,
		HostsDeferred:         hostCov.HostsDeferred,
		ResourcesDeclared:     resCov.ResourcesDeclared,
		ResourcesSupervised:   resCov.ResourcesSupervised,
		ResourcesUnsupervised: resCov.ResourcesUnsupervised,
		RegistrySkipped:       diag.Skipped,
		RegistryWarned:        diag.Warned,
		SkippedOwners:         redactSlice(scope, diag.SkippedOwners),
		WarnedOwners:          redactSlice(scope, diag.WarnedOwners),
	}

	summary := buildPortfolioSummary(scope, hostRows, projects, deployments, connectors, coverage)

	out := &PortfolioHealthProjection{
		SchemaVersion:    PortfolioHealthSchemaVersion,
		Contract:         PortfolioHealthContract,
		ControllerID:     scope.Text(s.controllerID),
		ProviderInstance: scope.Text(s.providerInstance),
		GeneratedAt:      now.Format(time.RFC3339),
		ValidForSeconds:  defaultValidForSeconds,
		CollectionMode:   q.Mode,
		Authorization:    authMeta,
		Coverage:         coverage,
		Summary:          summary,
		Hosts:            hostRows,
		Projects:         projects,
		Deployments:      deployments,
		Connectors:       connectors,
	}
	call.note(fmt.Sprintf("hosts=%d projects=%d deployments=%d connectors=%d overall=%s",
		len(out.Hosts), len(out.Projects), len(out.Deployments), len(out.Connectors), out.Summary.OverallStatus))
	return out, nil
}

func resolveAuthorizationMeta(ctx context.Context) PortfolioAuthorization {
	meta := PortfolioAuthorization{
		PrincipalKind: "unknown",
		Surface:       string(CallerSurfaceFrom(ctx)),
		Scope:         "portfolio:read",
	}
	if meta.Surface == "" {
		meta.Surface = string(SurfaceUnknown)
	}
	if p, ok := PrincipalFrom(ctx); ok {
		if p.Kind != "" {
			meta.PrincipalKind = string(p.Kind)
		}
		if len(p.Scopes) > 0 {
			meta.Scope = strings.Join(p.Scopes, " ")
		}
	} else if meta.Surface == string(SurfaceInProcess) {
		meta.PrincipalKind = string(PrincipalHuman)
	}
	return meta
}

func (s *PortfolioHealthService) allowTarget(ctx context.Context, connector, targetKind, id string, labels *target.ResourceLabels) bool {
	resolved := target.Resolve(connector, targetKind, id, labels, false)
	if s.targetFilter != nil && !s.targetFilter(ctx, targetKind, id, resolved.Labels) {
		return false
	}
	op := contract.Operation{
		Name:   "read",
		Effect: contract.EffectRead,
		Target: contract.TargetDescriptor{Kind: targetKind},
	}.Finalize()
	spec := auditSpec{
		connector:      connector,
		operation:      "read",
		op:             op,
		known:          true,
		resolvedTarget: &resolved,
	}
	req := policyRequest(ctx, spec, resolved)
	if enforced(req) {
		res := PolicyDecisionPoint().Authorize(req)
		if res.Decision == policy.Deny || res.Decision == policy.Approve {
			return false
		}
	}
	return true
}

func isHostDeferred(h hosts.Host) bool {
	if strings.EqualFold(h.Name, "work-laptop") {
		return true
	}
	if h.Labels != nil {
		if strings.EqualFold(h.Labels["lifecycle"], "deferred") || strings.EqualFold(h.Labels["deferred"], "true") {
			return true
		}
	}
	return false
}

func (s *PortfolioHealthService) collectHosts(
	ctx context.Context,
	scope *redact.Scope,
	now time.Time,
	mode string,
) ([]PortfolioHostHealth, map[string]PortfolioHostHealth, map[string]map[string]string, PortfolioCoverage, bool) {
	var cov PortfolioCoverage
	hostMap := map[string]PortfolioHostHealth{}
	hostUnits := map[string]map[string]string{}
	filtered := false

	var views []HostView
	if s.hosts != nil {
		if list, err := s.hosts.list(HostFilter{}); err == nil {
			views = list
		}
	}

	// Ensure the local controller host is represented even when hosts.yaml is
	// absent or does not list it explicitly.
	hasController := false
	for _, v := range views {
		if v.Host.Name == s.controllerID {
			hasController = true
			break
		}
	}
	if !hasController && s.controllerID != "" {
		views = append([]HostView{{
			Host:      hosts.Host{Name: s.controllerID, Role: "operator"},
			State:     hosts.StateOnline,
			LastSeen:  &now,
			ProbedAt:  &now,
			CheckedAt: &now,
			Stale:     false,
		}}, views...)
	}

	rows := make([]PortfolioHostHealth, 0, len(views))
	for _, v := range views {
		var labels *target.ResourceLabels
		if v.Host.Reach.SSH != "" && s.runtime != nil {
			if def, ok := s.runtime.ResourceDef(v.Host.Reach.SSH); ok && def != nil {
				bound := def.TargetLabels()
				labels = &bound
			}
		}
		if !s.allowTarget(ctx, "host", "host", v.Host.Name, labels) {
			filtered = true
			continue
		}

		cov.HostsDeclared++
		deferred := isHostDeferred(v.Host)
		transport := v.Host.Reach.EffectiveTransport()
		if transport == "" {
			if v.Host.Name == s.controllerID {
				transport = "local"
			} else {
				transport = "none"
			}
		}

		row := PortfolioHostHealth{
			Name:            scope.Text(v.Host.Name),
			Role:            scope.Text(v.Host.Role),
			Provider:        scope.Text(v.Host.Provider),
			Transport:       transport,
			Lifecycle:       "active",
			Reachability:    v.State,
			Readiness:       ReadinessUnknown,
			ReadinessSource: "none",
			Stale:           v.Stale,
			LastSeen:        v.LastSeen,
			ProbedAt:        v.ProbedAt,
			CheckedAt:       v.CheckedAt,
			Reason:          scope.Text(v.Reason),
		}
		if len(v.Host.Labels) > 0 {
			row.Labels = make(map[string]string, len(v.Host.Labels))
			for k, val := range v.Host.Labels {
				row.Labels[scope.Text(k)] = scope.Text(val)
			}
		}

		refTime := v.CheckedAt
		if v.ProbedAt != nil {
			refTime = v.ProbedAt
		}
		if refTime != nil {
			age := int64(now.Sub(*refTime).Seconds())
			if age < 0 {
				age = 0
			}
			row.AgeSeconds = &age
		}

		if v.Probed != nil {
			row.Platform = PortfolioHostPlatform{
				OS:        scope.Text(v.Probed.OS),
				Arch:      scope.Text(v.Probed.Arch),
				OSVersion: scope.Text(v.Probed.OSVersion),
				Kernel:    scope.Text(v.Probed.Kernel),
			}
			row.Capacity = &PortfolioHostCapacity{
				CPU:                  v.Probed.CPU,
				CPUModel:             scope.Text(v.Probed.CPUModel),
				MemoryTotalBytes:     v.Probed.MemoryTotal,
				MemoryAvailableBytes: v.Probed.MemoryAvailable,
				DiskMount:            scope.Text(v.Probed.DiskMount),
				DiskTotalBytes:       v.Probed.DiskTotal,
				DiskFreeBytes:        v.Probed.DiskFree,
				Load1:                v.Probed.Load1,
				UptimeSeconds:        v.Probed.UptimeSeconds,
			}
			if len(v.Probed.Tools) > 0 {
				row.Capacity.Tools = make(map[string]bool, len(v.Probed.Tools))
				for k, b := range v.Probed.Tools {
					row.Capacity.Tools[k] = b
				}
			}
			if len(v.Probed.Units) > 0 {
				u := make(map[string]string, len(v.Probed.Units))
				for k, st := range v.Probed.Units {
					u[k] = st
				}
				hostUnits[v.Host.Name] = u
			}
		} else {
			row.Platform = PortfolioHostPlatform{
				OS:   scope.Text(v.Host.Declared.OS),
				Arch: scope.Text(v.Host.Declared.Arch),
			}
		}

		if deferred {
			row.Lifecycle = "deferred"
			row.Reachability = "deferred"
			row.Readiness = ReadinessNotApplicable
			row.ReadinessSource = "none"
			row.ReadinessDetail = "host_deferred"
			row.RollupStatus = RollupExpectedStopped
			cov.HostsDeferred++
			rows = append(rows, row)
			hostMap[row.Name] = row
			continue
		}

		// Local controller readiness vs Outpost HTTP readiness check.
		switch {
		case v.Host.Name == s.controllerID && v.Host.Reach.Outpost == nil:
			row.Reachability = hosts.StateOnline
			row.Readiness = ReadinessReady
			row.ReadinessSource = "local_daemon"
			row.Stale = false
		case v.Host.Reach.EffectiveTransport() == "outpost" && v.Host.Reach.Outpost != nil && v.Host.Reach.Outpost.URL != "":
			probe := s.probeOutpostReadiness(ctx, scope, now, v.Host.Name, v.Host.Reach.Outpost, mode == PortfolioModeLiveBounded)
			if probe.reachability == hosts.StateOnline {
				row.Reachability = hosts.StateOnline
			} else if row.Reachability == "" || row.Reachability == hosts.StateUnknown {
				row.Reachability = probe.reachability
			} else if probe.reachability == hosts.StateOffline && mode == PortfolioModeLiveBounded {
				row.Reachability = hosts.StateOffline
			}
			row.Readiness = probe.readiness
			row.ReadinessSource = probe.readinessSource
			row.ReadinessDetail = scope.Text(probe.readinessDetail)

			if _, haveUnits := hostUnits[v.Host.Name]; !haveUnits && row.Reachability == hosts.StateOnline && mode == PortfolioModeLiveBounded {
				if units := s.fetchOutpostUnitsBounded(ctx, scope, now, v.Host.Name, v.Host.Reach.Outpost); len(units) > 0 {
					hostUnits[v.Host.Name] = units
				}
			}
		case v.Host.Reach.EffectiveTransport() == "ssh":
			row.Readiness = ReadinessUnknown
			row.ReadinessSource = "ssh_probe"
			if row.Reachability == hosts.StateOnline {
				row.ReadinessDetail = "ssh_reachability_only"
			}
		}

		if row.Name == s.controllerID && row.Reachability == hosts.StateOnline {
			// On the primary controller itself, live daemon + Outpost response means fresh.
			if row.Readiness == ReadinessReady || row.ReadinessSource == "outpost_healthz" || row.ReadinessSource == "local_daemon" {
				row.Stale = false
			}
		}

		// Compute host rollup_status from reachability, readiness, and staleness.
		switch {
		case row.Reachability == hosts.StateOffline:
			row.RollupStatus = RollupDown
		case row.Reachability == hosts.StateUnknown:
			row.RollupStatus = RollupUnknown
		case row.Readiness == ReadinessDegraded || row.Readiness == ReadinessUnready:
			row.RollupStatus = RollupDegraded
		case row.Stale:
			row.RollupStatus = RollupStale
			cov.HostsStale++
		default:
			row.RollupStatus = RollupHealthy
			cov.HostsProbedFresh++
		}
		if row.RollupStatus != RollupStale && row.Stale {
			cov.HostsStale++
		}

		rows = append(rows, row)
		hostMap[row.Name] = row
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, hostMap, hostUnits, cov, filtered
}

func (s *PortfolioHealthService) probeOutpostReadiness(
	ctx context.Context,
	scope *redact.Scope,
	now time.Time,
	hostName string,
	reach *hosts.OutpostReach,
	forceLive bool,
) cachedReadiness {
	s.mu.Lock()
	if cached, ok := s.readinessCache[hostName]; ok && !forceLive && now.Sub(cached.at) < s.cacheTTL {
		s.mu.Unlock()
		return cached
	}
	s.mu.Unlock()

	baseURL := strings.TrimRight(strings.TrimSpace(reach.URL), "/")
	token := readOptionalToken(scope, reach.TokenFile)

	res := cachedReadiness{
		at:              now,
		reachability:    hosts.StateUnknown,
		readiness:       ReadinessUnknown,
		readinessSource: "none",
	}

	code, body, err := s.doBoundedGET(ctx, baseURL+"/api/readyz", token)
	switch {
	case err == nil && code >= 200 && code < 300:
		res.reachability = hosts.StateOnline
		res.readinessSource = "outpost_readyz"
		res.readiness, res.readinessDetail = parseReadyzBody(body)
	case err == nil && (code == http.StatusNotFound || code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusMethodNotAllowed):
		// Fall back to /healthz as liveness-only (never fake readiness="ready").
		hCode, _, hErr := s.doBoundedGET(ctx, baseURL+"/healthz", token)
		if hErr == nil && hCode >= 200 && hCode < 300 {
			res.reachability = hosts.StateOnline
			res.readiness = ReadinessUnknown
			res.readinessSource = "outpost_healthz"
			res.readinessDetail = "liveness_only_healthz"
		} else if hErr != nil {
			res.reachability = hosts.StateOffline
			res.readiness = ReadinessUnready
			res.readinessSource = "outpost_healthz"
			res.readinessDetail = "outpost_unreachable"
		} else {
			res.reachability = hosts.StateOnline
			res.readiness = ReadinessUnready
			res.readinessSource = "outpost_healthz"
			res.readinessDetail = fmt.Sprintf("healthz_http_%d", hCode)
		}
	case err == nil && code >= 500:
		res.reachability = hosts.StateOnline
		res.readiness = ReadinessUnready
		res.readinessSource = "outpost_readyz"
		res.readinessDetail = fmt.Sprintf("readyz_http_%d", code)
	case err != nil:
		res.reachability = hosts.StateOffline
		res.readiness = ReadinessUnready
		res.readinessSource = "outpost_readyz"
		res.readinessDetail = "outpost_unreachable"
	default:
		res.reachability = hosts.StateOnline
		res.readiness = ReadinessUnknown
		res.readinessSource = "outpost_readyz"
		res.readinessDetail = fmt.Sprintf("readyz_http_%d", code)
	}

	s.mu.Lock()
	s.readinessCache[hostName] = res
	s.mu.Unlock()
	return res
}

func parseReadyzBody(body []byte) (readiness, detail string) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" || strings.EqualFold(trimmed, "ok") || strings.EqualFold(trimmed, "ready") {
		return ReadinessReady, ""
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ReadinessReady, ""
	}
	if status, ok := payload["status"].(string); ok {
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "ok", "ready", "healthy":
			return ReadinessReady, ""
		case "degraded", "warning":
			return ReadinessDegraded, "readyz_status_degraded"
		case "unready", "down", "failed", "error":
			return ReadinessUnready, "readyz_status_unready"
		}
	}
	if ready, ok := payload["ready"].(bool); ok {
		if ready {
			return ReadinessReady, ""
		}
		return ReadinessUnready, "readyz_not_ready"
	}
	if okVal, ok := payload["ok"].(bool); ok {
		if okVal {
			return ReadinessReady, ""
		}
		return ReadinessUnready, "readyz_not_ok"
	}
	return ReadinessReady, ""
}

func readOptionalToken(scope *redact.Scope, tokenFile string) string {
	if strings.TrimSpace(tokenFile) == "" {
		return ""
	}
	b, err := os.ReadFile(config.ExpandHomePath(tokenFile)) // #nosec G304 -- operator-configured Outpost token file path.
	if err != nil {
		return ""
	}
	tok := strings.TrimSpace(string(b))
	if tok != "" && scope != nil {
		scope.Add("outpost/token", tok)
	}
	return tok
}

func (s *PortfolioHealthService) doBoundedGET(ctx context.Context, rawURL, bearerToken string) (int, []byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, defaultReadinessTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, err
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, data, nil
}

func (s *PortfolioHealthService) fetchOutpostUnitsBounded(
	ctx context.Context,
	scope *redact.Scope,
	now time.Time,
	hostName string,
	reach *hosts.OutpostReach,
) map[string]string {
	s.mu.Lock()
	if cached, ok := s.unitCache[hostName]; ok && now.Sub(cached.at) < s.cacheTTL {
		s.mu.Unlock()
		return cached.units
	}
	s.mu.Unlock()

	if reach == nil || reach.URL == "" || reach.TokenFile == "" {
		return nil
	}
	uCtx, cancel := context.WithTimeout(ctx, defaultReadinessTimeout)
	defer cancel()
	client := outpost.Client{
		URL:       reach.URL,
		TokenFile: config.ExpandHomePath(reach.TokenFile),
		Credential: func(token string) {
			if scope != nil {
				scope.Add("outpost/token", token)
			}
		},
	}
	res, err := client.Execute(uCtx, outpost.Request{Verb: "units"})
	if err != nil || res.ExitCode != 0 {
		return nil
	}
	units := hosts.ParseUnitStates(res.Output)
	if len(units) > 0 {
		s.mu.Lock()
		s.unitCache[hostName] = cachedUnitMap{at: now, units: units}
		s.mu.Unlock()
	}
	return units
}

func (s *PortfolioHealthService) collectDeployments(
	ctx context.Context,
	scope *redact.Scope,
	now time.Time,
	mode string,
	cfg *config.ConfigV2,
	hostMap map[string]PortfolioHostHealth,
	hostUnits map[string]map[string]string,
) ([]PortfolioDeploymentHealth, PortfolioCoverage, bool) {
	var cov PortfolioCoverage
	if cfg == nil {
		return []PortfolioDeploymentHealth{}, cov, false
	}

	filtered := false
	allowed := make([]config.ResourceDef, 0, len(cfg.Resources))
	for _, r := range cfg.Resources {
		tl := r.TargetLabels()
		connector := r.Connector
		if connector == "" {
			connector = "local"
		}
		targetKind := connector + ".resource"
		if !s.allowTarget(ctx, connector, targetKind, r.ID, &tl) {
			filtered = true
			continue
		}
		allowed = append(allowed, r)
	}

	out := make([]PortfolioDeploymentHealth, len(allowed))
	var wg sync.WaitGroup
	for i := range allowed {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = s.inspectDeployment(ctx, scope, now, mode, allowed[i], cfg, hostMap, hostUnits)
		}()
	}
	wg.Wait()

	for _, d := range out {
		cov.ResourcesDeclared++
		if d.Supervision == "unmanaged_handle" {
			cov.ResourcesUnsupervised++
		} else {
			cov.ResourcesSupervised++
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, cov, filtered
}

func (s *PortfolioHealthService) inspectDeployment(
	ctx context.Context,
	scope *redact.Scope,
	now time.Time,
	mode string,
	r config.ResourceDef,
	cfg *config.ConfigV2,
	hostMap map[string]PortfolioHostHealth,
	hostUnits map[string]map[string]string,
) PortfolioDeploymentHealth {
	hostName := strings.TrimSpace(r.Host)
	if hostName == "" {
		hostName = s.controllerID
	}
	resolvedLabels := r.TargetLabels().Resolved()
	adminStr := resolvedLabels.Admin.String()
	if adminStr == "" {
		adminStr = target.AdminUnknown
	}
	envStr := string(resolvedLabels.Env)
	if envStr == "" {
		envStr = string(target.EnvUnknown)
	}
	ownerStr := resolvedLabels.Owner
	if ownerStr == "" {
		ownerStr = target.OwnerUnknown
	}

	dep := PortfolioDeploymentHealth{
		ID:           scope.Text(r.ID),
		Name:         scope.Text(r.Name),
		ProjectID:    scope.Text(r.Project),
		Host:         scope.Text(hostName),
		ResourceType: scope.Text(r.Type),
		Connector:    scope.Text(r.Connector),
		URL:          scope.Text(configString(r.Config, "url")),
		Port:         configInt(r.Config, "port"),
		Tags:         redactSlice(scope, r.Tags),
		Labels: PortfolioDeploymentLabels{
			Env:   scope.Text(envStr),
			Owner: scope.Text(ownerStr),
			Admin: scope.Text(adminStr),
		},
		Provenance: PortfolioProvenance{
			Source:     "cerberus_catalog",
			ObservedAt: now.Format(time.RFC3339),
			AgeSeconds: 0,
			Stale:      false,
		},
	}

	isRemoteHost := r.Host != "" && r.Host != s.controllerID

	switch {
	case r.Type == string(domain.ResourceProcess) && r.Connector == "local" && !isRemoteHost:
		s.populateLocalProcessDeployment(ctx, scope, now, mode, r, cfg, &dep)
	case r.Type == string(domain.ResourceProcess) && isRemoteHost:
		s.populateRemoteProcessDeployment(scope, r, hostMap, hostUnits, &dep)
	case r.Type == string(domain.ResourceContainer) || r.Connector == "docker":
		s.populateDockerDeployment(ctx, scope, now, mode, r, &dep)
	default:
		dep.Kind = "connector_handle"
		dep.Supervision = "unmanaged_handle"
		dep.DesiredState = "unmanaged"
		dep.ObservedState = UnsupervisedStatus
		dep.Readiness = ReadinessNotApplicable
		dep.ReadinessSource = "none"
		dep.RollupStatus = RollupUnsupervised
		dep.RecommendedReason = scope.Text(UnsupervisedReason(r.Type, r.Connector))
		dep.RecommendedNextStep = scope.Text(UnsupervisedNextStep(r.ID, r.Connector))
	}

	return dep
}

func (s *PortfolioHealthService) populateLocalProcessDeployment(
	ctx context.Context,
	scope *redact.Scope,
	now time.Time,
	mode string,
	r config.ResourceDef,
	cfg *config.ConfigV2,
	dep *PortfolioDeploymentHealth,
) {
	dep.Kind = "local_service"
	dep.Supervision = "cerberus_local"
	dep.Mode = resourceMode(r)
	dep.Supervisor = resourceSupervisor(r)
	dep.RunFrom = resourceRunFrom(r)
	dep.OperatorStopped = pausectl.IsServicePaused(r.ID)
	dep.Provenance.Source = "local_supervisor"

	spec, specErr := localconn.SpecFromResourceConfig(r.Config)
	if specErr == nil {
		// Register any inline literal environment values on the redaction scope so
		// they can never leak in warnings or error messages.
		for k, v := range spec.Env {
			if strings.TrimSpace(v) != "" && !strings.Contains(v, "://") {
				scope.Add("env/"+k, v)
			}
		}
		dep.ServiceName = scope.Text(spec.ServiceName)
		if dep.URL == "" && spec.URL != "" {
			dep.URL = scope.Text(spec.URL)
		}
		if dep.Port == 0 && spec.Port > 0 {
			dep.Port = spec.Port
		}
	}
	dep.ConfigWarnings = redactSlice(scope, localconn.ProcessConfigWarnings(r.Config))
	if s.runtime != nil && cfg != nil {
		dep.DependencyWarnings = redactSlice(scope, s.runtime.dependencyWarnings(ctx, &r, cfg))
	}

	// Determine desired state.
	desiredCfg := strings.ToLower(strings.TrimSpace(configString(r.Config, "desired_state")))
	switch {
	case dep.OperatorStopped || desiredCfg == "stopped":
		dep.DesiredState = "stopped"
	case desiredCfg == "on_demand" || configExplicitAutoStartFalse(r.Config):
		dep.DesiredState = "on_demand"
	case dep.Mode == string(localconn.ProcessModeDevSession) && specErr == nil && !spec.AutoStart:
		dep.DesiredState = "on_demand"
	default:
		dep.DesiredState = "running"
	}

	dr := resourceDefToDomain(&r)
	state := domain.StateUnknown
	if s.runtime != nil {
		if st, err := s.runtime.statusWithTimeout(ctx, dr); err == nil {
			state = st
		} else {
			dep.RecommendedReason = scope.Text(err.Error())
		}
	}
	switch state {
	case domain.StateRunning, domain.StateHealthy, domain.StateStarting:
		dep.ObservedState = "running"
	case domain.StateStopped:
		dep.ObservedState = "stopped"
	case domain.StateFailed, domain.StateUnhealthy:
		dep.ObservedState = "failed"
	default:
		dep.ObservedState = "unknown"
	}

	// Artifact drift check.
	if specErr == nil && s.runtime != nil {
		art, haveArt := s.runtime.lookupDrift(r.ID)
		if !haveArt {
			if _, basic, err := localconn.InspectArtifactInstallBasic(dr, spec); err == nil {
				art, haveArt = basic, true
			}
		}
		if haveArt {
			dep.ArtifactInstalled = art.Installed
			dep.ArtifactStale = art.Stale
			dep.ArtifactStaleReason = scope.Text(art.StaleReason)
			if action, reason := localconn.RecommendedStatusAction(spec, state, art); action != "" {
				dep.RecommendedAction = scope.Text(action)
				dep.RecommendedReason = scope.Text(reason)
				dep.RecommendedNextStep = scope.Text(localconn.RecommendedNextStep(action, reason))
			}
		}
		if dep.Mode != string(localconn.ProcessModeOSService) && dep.RecommendedAction == "" {
			if action, reason := localconn.RecommendedDevSessionAction(r.ID, spec, state); action != "" {
				dep.RecommendedAction = scope.Text(action)
				dep.RecommendedReason = scope.Text(reason)
				dep.RecommendedNextStep = scope.Text(localconn.RecommendedNextStep(action, reason))
			}
		}
	}

	// Readiness evaluation: never fake readiness="ready" from PID liveness or a
	// wildcard bind URL alone; only probe an explicit health / health_check.url.
	healthURL := ""
	if specErr == nil {
		if strings.TrimSpace(spec.HealthCheck.URL) != "" {
			healthURL = strings.TrimSpace(spec.HealthCheck.URL)
		} else if strings.TrimSpace(spec.Health) != "" {
			healthURL = strings.TrimSpace(spec.Health)
		}
	}
	switch {
	case dep.ObservedState == "stopped":
		dep.Readiness = ReadinessNotApplicable
		dep.ReadinessSource = "none"
	case dep.ObservedState == "failed":
		dep.Readiness = ReadinessUnready
		dep.ReadinessSource = "none"
	case dep.ObservedState == "running" && healthURL != "" && !isWildcardURL(healthURL):
		probe := s.probeServiceHealthURL(ctx, now, r.ID, healthURL, mode == PortfolioModeLiveBounded)
		dep.Readiness = probe.readiness
		dep.ReadinessSource = "healthcheck_http"
		dep.ReadinessDetail = scope.Text(probe.readinessDetail)
	case dep.ObservedState == "running":
		dep.Readiness = ReadinessUnknown
		dep.ReadinessSource = "none"
		if isWildcardURL(dep.URL) || isWildcardURL(healthURL) {
			dep.ReadinessDetail = "wildcard_bind_url_not_probed"
		} else {
			dep.ReadinessDetail = "process_liveness_only"
		}
	default:
		dep.Readiness = ReadinessUnknown
		dep.ReadinessSource = "none"
	}

	// Rollup status calculation.
	switch {
	case dep.ObservedState == "stopped" && (dep.DesiredState == "stopped" || dep.DesiredState == "on_demand"):
		dep.RollupStatus = RollupExpectedStopped
	case dep.ObservedState == "stopped" || dep.ObservedState == "failed":
		dep.RollupStatus = RollupDown
	case dep.ObservedState == "unknown":
		dep.RollupStatus = RollupUnknown
	case dep.Readiness == ReadinessUnready || dep.Readiness == ReadinessDegraded:
		dep.RollupStatus = RollupDegraded
	case dep.ArtifactStale || dep.RecommendedAction != "" || len(dep.ConfigWarnings) > 0 || len(dep.DependencyWarnings) > 0:
		dep.RollupStatus = RollupDegraded
	default:
		dep.RollupStatus = RollupHealthy
	}
}

func isWildcardURL(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return strings.Contains(raw, "0.0.0.0") || strings.Contains(raw, "[::]")
	}
	host := u.Hostname()
	return host == "0.0.0.0" || host == "::" || host == ""
}

func (s *PortfolioHealthService) probeServiceHealthURL(
	ctx context.Context,
	now time.Time,
	resourceID string,
	healthURL string,
	forceLive bool,
) cachedReadiness {
	cacheKey := "svc:" + resourceID + ":" + healthURL
	s.mu.Lock()
	if cached, ok := s.readinessCache[cacheKey]; ok && !forceLive && now.Sub(cached.at) < s.cacheTTL {
		s.mu.Unlock()
		return cached
	}
	s.mu.Unlock()

	code, body, err := s.doBoundedGET(ctx, healthURL, "")
	res := cachedReadiness{
		at:              now,
		readinessSource: "healthcheck_http",
	}
	switch {
	case err != nil:
		res.readiness = ReadinessUnready
		res.readinessDetail = "healthcheck_unreachable"
	case code >= 200 && code < 300:
		res.readiness, res.readinessDetail = parseReadyzBody(body)
	default:
		res.readiness = ReadinessUnready
		res.readinessDetail = fmt.Sprintf("healthcheck_http_%d", code)
	}

	s.mu.Lock()
	s.readinessCache[cacheKey] = res
	s.mu.Unlock()
	return res
}

func (s *PortfolioHealthService) populateRemoteProcessDeployment(
	scope *redact.Scope,
	r config.ResourceDef,
	hostMap map[string]PortfolioHostHealth,
	hostUnits map[string]map[string]string,
	dep *PortfolioDeploymentHealth,
) {
	dep.Kind = "remote_service"
	dep.Supervision = "outpost_unit"
	dep.Mode = resourceMode(r)
	dep.Supervisor = resourceSupervisor(r)
	dep.RunFrom = resourceRunFrom(r)

	serviceName := configString(r.Config, "service_name")
	if serviceName == "" {
		serviceName = configString(r.Config, "unit")
	}
	if serviceName == "" {
		serviceName = r.ID
	}
	dep.ServiceName = scope.Text(serviceName)

	desiredCfg := strings.ToLower(strings.TrimSpace(configString(r.Config, "desired_state")))
	switch {
	case desiredCfg == "stopped":
		dep.DesiredState = "stopped"
	case desiredCfg == "on_demand" || configExplicitAutoStartFalse(r.Config):
		dep.DesiredState = "on_demand"
	default:
		dep.DesiredState = "running"
	}

	hostRow, hostFound := hostMap[r.Host]
	if !hostFound {
		dep.ObservedState = "unknown"
		dep.Readiness = ReadinessUnknown
		dep.ReadinessSource = "outpost_unit"
		dep.ReadinessDetail = "target_host_not_declared"
		dep.RollupStatus = RollupUnknown
		dep.RecommendedReason = scope.Text(fmt.Sprintf("target host %q is not declared in hosts.yaml", r.Host))
		return
	}

	if hostRow.CheckedAt != nil {
		dep.Provenance.ObservedAt = hostRow.CheckedAt.UTC().Format(time.RFC3339)
		if hostRow.AgeSeconds != nil {
			dep.Provenance.AgeSeconds = *hostRow.AgeSeconds
		}
	}
	dep.Provenance.Source = "outpost_host_probe"
	dep.Provenance.Stale = hostRow.Stale

	if hostRow.Lifecycle == "deferred" {
		dep.ObservedState = "unknown"
		dep.Readiness = ReadinessNotApplicable
		dep.ReadinessSource = "none"
		dep.ReadinessDetail = "host_deferred"
		dep.RollupStatus = RollupExpectedStopped
		return
	}
	if hostRow.Reachability == hosts.StateOffline {
		dep.ObservedState = "unreachable"
		dep.Readiness = ReadinessUnready
		dep.ReadinessSource = "outpost_unit"
		dep.ReadinessDetail = "host_offline"
		dep.RollupStatus = RollupDown
		dep.RecommendedReason = scope.Text(fmt.Sprintf("host %q is offline", r.Host))
		return
	}

	units := hostUnits[r.Host]
	unitState := lookupUnitState(units, serviceName, r.ID)
	switch unitState {
	case "running":
		dep.ObservedState = "running"
		dep.Readiness = ReadinessUnknown
		dep.ReadinessSource = "outpost_unit"
		dep.ReadinessDetail = "unit_active_no_http_probe"
		if hostRow.Stale {
			dep.RollupStatus = RollupStale
		} else {
			dep.RollupStatus = RollupHealthy
		}
	case "stopped":
		dep.ObservedState = "stopped"
		dep.Readiness = ReadinessNotApplicable
		dep.ReadinessSource = "outpost_unit"
		if dep.DesiredState == "stopped" || dep.DesiredState == "on_demand" {
			dep.RollupStatus = RollupExpectedStopped
		} else {
			dep.RollupStatus = RollupDown
			dep.RecommendedAction = "start"
			dep.RecommendedReason = scope.Text(fmt.Sprintf("unit %q is stopped on %s", serviceName, r.Host))
		}
	case "failed":
		dep.ObservedState = "failed"
		dep.Readiness = ReadinessUnready
		dep.ReadinessSource = "outpost_unit"
		dep.RollupStatus = RollupDown
		dep.RecommendedAction = "restart"
		dep.RecommendedReason = scope.Text(fmt.Sprintf("unit %q is in failed state on %s", serviceName, r.Host))
	default:
		dep.ObservedState = "unknown"
		dep.Readiness = ReadinessUnknown
		dep.ReadinessSource = "outpost_unit"
		dep.ReadinessDetail = "unit_state_unprobed"
		dep.RollupStatus = RollupUnknown
	}
}

func lookupUnitState(units map[string]string, serviceName, resourceID string) string {
	if len(units) == 0 {
		return ""
	}
	candidates := []string{
		serviceName,
		strings.TrimSuffix(serviceName, ".service"),
		strings.TrimSuffix(serviceName, ".service") + ".service",
		resourceID,
		resourceID + ".service",
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, ok := units[c]; ok && st != "" {
			return st
		}
	}
	return ""
}

func (s *PortfolioHealthService) populateDockerDeployment(
	ctx context.Context,
	scope *redact.Scope,
	now time.Time,
	mode string,
	r config.ResourceDef,
	dep *PortfolioDeploymentHealth,
) {
	dep.Kind = "shared_infra"
	dep.Supervision = "docker_engine"
	dep.Provenance.Source = "docker_inspect"
	dep.RecommendedNextStep = scope.Text(UnsupervisedNextStep(r.ID, "docker"))

	desiredCfg := strings.ToLower(strings.TrimSpace(configString(r.Config, "desired_state")))
	switch desiredCfg {
	case "stopped", "on_demand":
		dep.DesiredState = desiredCfg
	default:
		dep.DesiredState = "running"
	}

	st := s.inspectDockerCached(ctx, now, r, mode == PortfolioModeLiveBounded)
	dep.ObservedState = st.observedState
	dep.Readiness = st.readiness
	dep.ReadinessSource = st.readinessSource
	dep.ReadinessDetail = scope.Text(st.readinessDetail)

	switch {
	case dep.ObservedState == "stopped" && (dep.DesiredState == "stopped" || dep.DesiredState == "on_demand"):
		dep.RollupStatus = RollupExpectedStopped
	case dep.ObservedState == "stopped" || dep.ObservedState == "failed":
		dep.RollupStatus = RollupDown
		dep.RecommendedAction = "up"
		dep.RecommendedReason = "container is not running"
	case dep.ObservedState == "unknown":
		dep.RollupStatus = RollupUnknown
	case dep.Readiness == ReadinessUnready || dep.Readiness == ReadinessDegraded:
		dep.RollupStatus = RollupDegraded
		dep.RecommendedAction = "logs"
		dep.RecommendedReason = "container is running but Docker healthcheck is not healthy"
	default:
		dep.RollupStatus = RollupHealthy
		dep.RecommendedNextStep = ""
	}
}

func (s *PortfolioHealthService) inspectDockerCached(
	ctx context.Context,
	now time.Time,
	r config.ResourceDef,
	forceLive bool,
) cachedDockerState {
	s.mu.Lock()
	if cached, ok := s.dockerCache[r.ID]; ok && !forceLive && now.Sub(cached.at) < s.cacheTTL {
		s.mu.Unlock()
		return cached
	}
	s.mu.Unlock()

	res := cachedDockerState{
		at:              now,
		observedState:   "unknown",
		readiness:       ReadinessUnknown,
		readinessSource: "none",
	}
	if s.docker == nil {
		return res
	}

	dCtx, cancel := context.WithTimeout(ctx, defaultDockerTimeout)
	defer cancel()

	containerName := strings.TrimSpace(configString(r.Config, "container"))
	composeFile := strings.TrimSpace(configString(r.Config, "compose_file"))
	if composeFile != "" {
		composeFile = config.ExpandHomePath(composeFile)
	}

	var inspected *dockerconn.Container
	if containerName != "" {
		if c, err := s.docker.ContainerStatus(dCtx, containerName); err == nil && c != nil {
			inspected = c
		}
	} else if composeFile != "" {
		if stack, err := s.docker.ComposePS(dCtx, composeFile); err == nil && stack != nil {
			if len(stack.Services) == 0 {
				res.observedState = "stopped"
				res.readiness = ReadinessNotApplicable
				res.readinessSource = "docker_healthcheck"
				res.readinessDetail = "compose_stack_stopped"
			} else {
				first := stack.Services[0]
				if c, cErr := s.docker.ContainerStatus(dCtx, first.Name); cErr == nil && c != nil {
					inspected = c
				} else {
					inspected = &dockerconn.Container{
						Name:   first.Name,
						State:  first.State,
						Status: first.State,
						Image:  first.Image,
						Ports:  first.Ports,
					}
				}
			}
		} else if err != nil {
			res.readinessDetail = "docker_compose_ps_unavailable"
		}
	} else {
		if c, err := s.docker.ContainerStatus(dCtx, r.ID); err == nil && c != nil {
			inspected = c
		}
	}

	if inspected != nil {
		stateLower := strings.ToLower(strings.TrimSpace(inspected.State))
		if stateLower == "" {
			stateLower = strings.ToLower(strings.TrimSpace(inspected.Status))
		}
		switch {
		case strings.Contains(stateLower, "running"):
			res.observedState = "running"
			switch strings.ToLower(strings.TrimSpace(inspected.Health)) {
			case "healthy":
				res.readiness = ReadinessReady
				res.readinessSource = "docker_healthcheck"
			case "unhealthy":
				res.readiness = ReadinessUnready
				res.readinessSource = "docker_healthcheck"
				res.readinessDetail = "docker_health_unhealthy"
			case "starting":
				res.readiness = ReadinessDegraded
				res.readinessSource = "docker_healthcheck"
				res.readinessDetail = "docker_health_starting"
			default:
				res.readiness = ReadinessUnknown
				res.readinessSource = "none"
				res.readinessDetail = "container_running_no_healthcheck"
			}
		case strings.Contains(stateLower, "dead") || strings.Contains(stateLower, "oom"):
			res.observedState = "failed"
			res.readiness = ReadinessUnready
			res.readinessSource = "docker_healthcheck"
		default:
			res.observedState = "stopped"
			res.readiness = ReadinessNotApplicable
			res.readinessSource = "docker_healthcheck"
		}
	}

	s.mu.Lock()
	s.dockerCache[r.ID] = res
	s.mu.Unlock()
	return res
}

func (s *PortfolioHealthService) collectProjects(
	ctx context.Context,
	scope *redact.Scope,
	cfg *config.ConfigV2,
	deployments []PortfolioDeploymentHealth,
	depFiltered bool,
) ([]PortfolioProjectHealth, bool) {
	if cfg == nil {
		return []PortfolioProjectHealth{}, false
	}

	byProject := map[string][]PortfolioDeploymentHealth{}
	for _, d := range deployments {
		byProject[d.ProjectID] = append(byProject[d.ProjectID], d)
	}

	filtered := false
	out := make([]PortfolioProjectHealth, 0, len(cfg.Projects))
	for _, p := range cfg.Projects {
		if !s.allowTarget(ctx, "local", "project", p.ID, nil) {
			filtered = true
			continue
		}
		deps := byProject[p.ID]
		// If deployments were filtered by target policy and none remain for a
		// non-empty project, omit the project so restricted projects don't leak.
		if depFiltered && len(deps) == 0 {
			hasDeclared := false
			for _, r := range cfg.Resources {
				if r.Project == p.ID {
					hasDeclared = true
					break
				}
			}
			if hasDeclared {
				filtered = true
				continue
			}
		}

		counts := map[string]int{
			"total":               len(deps),
			RollupHealthy:         0,
			RollupDegraded:        0,
			RollupDown:            0,
			RollupUnknown:         0,
			RollupExpectedStopped: 0,
			RollupUnsupervised:    0,
			RollupStale:           0,
		}
		for _, d := range deps {
			counts[d.RollupStatus]++
		}

		projRollup := computeProjectRollup(counts)
		var links []config.Link
		if len(p.Links) > 0 {
			links = make([]config.Link, len(p.Links))
			for i, l := range p.Links {
				links[i] = config.Link{
					Kind:   scope.Text(l.Kind),
					Target: scope.Text(l.Target),
				}
			}
		}

		out = append(out, PortfolioProjectHealth{
			ID:             scope.Text(p.ID),
			Name:           scope.Text(p.Name),
			Description:    scope.Text(p.Description),
			RollupStatus:   projRollup,
			ResourceCounts: counts,
			Capabilities:   redactSlice(scope, p.Capabilities),
			Links:          links,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, filtered
}

func computeProjectRollup(counts map[string]int) string {
	total := counts["total"]
	if total == 0 {
		return RollupUnknown
	}
	switch {
	case counts[RollupDown] > 0:
		return RollupDown
	case counts[RollupDegraded] > 0:
		return RollupDegraded
	case counts[RollupStale] > 0:
		return RollupStale
	case counts[RollupHealthy] > 0:
		return RollupHealthy
	case counts[RollupUnknown] > 0:
		return RollupUnknown
	case counts[RollupExpectedStopped] == total:
		return RollupExpectedStopped
	case counts[RollupUnsupervised] == total:
		return RollupUnsupervised
	case counts[RollupExpectedStopped]+counts[RollupUnsupervised] == total:
		return RollupExpectedStopped
	default:
		return RollupUnknown
	}
}

func (s *PortfolioHealthService) collectConnectors(
	ctx context.Context,
	scope *redact.Scope,
) ([]PortfolioConnectorHealth, bool) {
	filtered := false
	out := []PortfolioConnectorHealth{}
	seen := map[string]bool{}

	if s.external != nil {
		defs := s.external.Definitions()
		liveDefs := s.external.LiveDefinitionsContext(ctx)
		liveSet := make(map[string]bool, len(liveDefs))
		for _, ld := range liveDefs {
			liveSet[ld.ID] = true
		}
		for _, def := range defs {
			if !s.allowTarget(ctx, def.ID, "connector", def.ID, nil) {
				filtered = true
				continue
			}
			seen[def.ID] = true
			live := liveSet[def.ID]
			status := RollupHealthy
			if !live {
				status = RollupUnknown
			}
			out = append(out, PortfolioConnectorHealth{
				ID:           scope.Text(def.ID),
				Kind:         "builtin",
				Version:      scope.Text(def.Version),
				Live:         live,
				RollupStatus: status,
			})
		}
	}

	if s.managedPlugins != nil {
		if plugins, err := s.managedPlugins.List(ctx); err == nil {
			for _, pl := range plugins {
				if seen[pl.ID] {
					continue
				}
				if !s.allowTarget(ctx, pl.ID, "connector", pl.ID, nil) {
					filtered = true
					continue
				}
				seen[pl.ID] = true
				live := pl.Loaded
				detail := pl.GaveUp
				if detail == "" && len(pl.ConfigProblems) > 0 {
					detail = pl.ConfigProblems[0]
				}
				if detail == "" && len(pl.MissingSecrets) > 0 {
					detail = "missing secrets: " + strings.Join(pl.MissingSecrets, ", ")
				}
				status := RollupExpectedStopped
				switch {
				case live && detail == "":
					status = RollupHealthy
				case live && detail != "":
					status = RollupDegraded
				case pl.GaveUp != "" || len(pl.ConfigProblems) > 0:
					status = RollupDown
				}
				out = append(out, PortfolioConnectorHealth{
					ID:           scope.Text(pl.ID),
					Kind:         "managed_plugin",
					Version:      scope.Text(pl.Version),
					Live:         live,
					RollupStatus: status,
					Detail:       scope.Text(detail),
				})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, filtered
}

func buildPortfolioSummary(
	scope *redact.Scope,
	hosts []PortfolioHostHealth,
	projects []PortfolioProjectHealth,
	deployments []PortfolioDeploymentHealth,
	connectors []PortfolioConnectorHealth,
	coverage PortfolioCoverage,
) PortfolioSummary {
	byStatus := map[string]int{
		RollupHealthy:         0,
		RollupDegraded:        0,
		RollupDown:            0,
		RollupUnknown:         0,
		RollupExpectedStopped: 0,
		RollupUnsupervised:    0,
		RollupStale:           0,
	}
	hostsByStatus := map[string]int{
		"online":   0,
		"degraded": 0,
		"offline":  0,
		"unknown":  0,
		"deferred": 0,
	}
	projectsByStatus := map[string]int{
		RollupHealthy:         0,
		RollupDegraded:        0,
		RollupDown:            0,
		RollupUnknown:         0,
		RollupExpectedStopped: 0,
		RollupUnsupervised:    0,
		RollupStale:           0,
	}
	attention := []PortfolioAttentionItem{}

	for _, h := range hosts {
		switch {
		case h.Lifecycle == "deferred":
			hostsByStatus["deferred"]++
		case h.Reachability == "offline":
			hostsByStatus["offline"]++
		case h.RollupStatus == RollupDegraded || h.RollupStatus == RollupStale:
			hostsByStatus["degraded"]++
		case h.Reachability == "online":
			hostsByStatus["online"]++
		default:
			hostsByStatus["unknown"]++
		}

		if h.RollupStatus == RollupDown || h.RollupStatus == RollupDegraded || h.RollupStatus == RollupStale {
			reason := h.Reason
			if reason == "" {
				reason = h.ReadinessDetail
			}
			if reason == "" && h.Stale {
				reason = "host probe data is stale"
			}
			if reason == "" {
				reason = fmt.Sprintf("host reachability=%s readiness=%s", h.Reachability, h.Readiness)
			}
			attention = append(attention, PortfolioAttentionItem{
				TargetType:          "host",
				ID:                  h.Name,
				Host:                h.Name,
				RollupStatus:        h.RollupStatus,
				Reason:              scope.Text(reason),
				RecommendedAction:   "probe",
				RecommendedNextStep: fmt.Sprintf("cerberus host probe %s", h.Name),
			})
		}
	}

	for _, p := range projects {
		projectsByStatus[p.RollupStatus]++
	}

	for _, d := range deployments {
		byStatus[d.RollupStatus]++
		if d.RollupStatus == RollupDown || d.RollupStatus == RollupDegraded || d.RollupStatus == RollupStale {
			reason := d.RecommendedReason
			if reason == "" && d.ArtifactStaleReason != "" {
				reason = d.ArtifactStaleReason
			}
			if reason == "" && len(d.ConfigWarnings) > 0 {
				reason = d.ConfigWarnings[0]
			}
			if reason == "" && len(d.DependencyWarnings) > 0 {
				reason = d.DependencyWarnings[0]
			}
			if reason == "" && d.ReadinessDetail != "" {
				reason = d.ReadinessDetail
			}
			if reason == "" {
				reason = fmt.Sprintf("observed_state=%s readiness=%s", d.ObservedState, d.Readiness)
			}
			attention = append(attention, PortfolioAttentionItem{
				TargetType:          "deployment",
				ID:                  d.ID,
				ProjectID:           d.ProjectID,
				Host:                d.Host,
				RollupStatus:        d.RollupStatus,
				Reason:              scope.Text(reason),
				RecommendedAction:   d.RecommendedAction,
				RecommendedNextStep: d.RecommendedNextStep,
			})
		}
	}

	for _, c := range connectors {
		if c.RollupStatus == RollupDown || c.RollupStatus == RollupDegraded {
			attention = append(attention, PortfolioAttentionItem{
				TargetType:   "connector",
				ID:           c.ID,
				RollupStatus: c.RollupStatus,
				Reason:       scope.Text(c.Detail),
			})
		}
	}

	if coverage.RegistrySkipped > 0 || coverage.RegistryWarned > 0 {
		attention = append(attention, PortfolioAttentionItem{
			TargetType:          "registry",
			ID:                  "registry",
			RollupStatus:        RollupDegraded,
			Reason:              fmt.Sprintf("registry resolution skipped %d config(s) and warned on %d config(s)", coverage.RegistrySkipped, coverage.RegistryWarned),
			RecommendedAction:   "validate",
			RecommendedNextStep: "cerberus registry health",
		})
	}

	var overall string
	switch {
	case byStatus[RollupDown] > 0 || hostsByStatus["offline"] > 0:
		overall = RollupDown
	case byStatus[RollupDegraded] > 0 || byStatus[RollupStale] > 0 || hostsByStatus["degraded"] > 0 || coverage.RegistrySkipped > 0 || coverage.RegistryWarned > 0:
		overall = RollupDegraded
	case byStatus[RollupHealthy] > 0 || hostsByStatus["online"] > 0:
		overall = RollupHealthy
	case byStatus[RollupExpectedStopped] > 0:
		overall = RollupExpectedStopped
	case byStatus[RollupUnsupervised] > 0:
		overall = RollupUnsupervised
	default:
		overall = RollupUnknown
	}

	return PortfolioSummary{
		OverallStatus:     overall,
		ByStatus:          byStatus,
		HostsByStatus:     hostsByStatus,
		ProjectsByStatus:  projectsByStatus,
		AttentionRequired: attention,
	}
}

func redactSlice(scope *redact.Scope, in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		if scope != nil {
			out[i] = scope.Text(s)
		} else {
			out[i] = s
		}
	}
	return out
}

func configExplicitAutoStartFalse(cfg map[string]any) bool {
	if cfg == nil {
		return false
	}
	v, ok := cfg["auto_start"]
	if !ok {
		return false
	}
	switch val := v.(type) {
	case bool:
		return !val
	case string:
		return strings.EqualFold(strings.TrimSpace(val), "false")
	default:
		return false
	}
}
