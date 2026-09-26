import { ApiError, createApiClient, type JsonObject } from '@hollis-labs/sysop-ui/api'

const http = createApiClient({ baseUrl: '' })

// SessionInfo is served only to a signed-in session: its own action token,
// and its public id.
export interface SessionInfo {
  action_token: string
  session?: string
  posture?: PostureInfo
  passkeys?: PasskeysAlert | null
}

// PasskeysAlert is the header's line about out-of-band approval: loud while
// no passkey is enrolled, for a day after an enrollment, and during a
// cool-down after the key registry changed by other means.
export interface PasskeysAlert {
  summary: string
  alert: boolean
  state: string
  keys: number
}

export interface PasskeyInfo {
  fingerprint: string
  label?: string
  enrolled_at: string
}

export interface PasskeyStatus {
  state: 'not_set_up' | 'ok' | 'cooldown' | string
  keys: PasskeyInfo[]
  cooldown_until?: string
  last_enrolled_at?: string
  registry_hash: string
}

// PasskeyCeremony is one ceremony's id and the options the browser needs,
// as base64url of their JSON (see webauthn.ts for why they travel opaque).
export interface PasskeyCeremony {
  ceremony: string
  options: string
}

export interface EnrollBegin {
  ceremony: string
  creation: string
  authorize?: string
}

// PostureInfo is the applied posture (section 13): shown in the header so a
// permissive install can never be mistaken for a secure one.
export interface PostureInfo {
  summary: string
  permissive: boolean
}

export interface OverviewInfo {
  daemon: {
    running: boolean
    socket_path?: string
    socket_exists: boolean
    services_failed: number
    services_protected: number
  }
  inventory: {
    projects: number
    resources: number
    pipelines: number
    connectors: number
    plugins: number
  }
  runtime: {
    running: number
    attention: number
    stopped: number
  }
  registry: {
    entries: number
    healthy: number
    unhealthy: number
  }
  trends: OverviewTrends
  error?: string
}

// OverviewTrends carries the per-metric 24h hourly history that powers the
// SignalBars + MiniTrend widgets on the overview page. Each array is 24
// hourly buckets, oldest → newest. Empty (zero-filled) arrays mean the
// daemon hasn't recorded enough samples yet.
export interface OverviewTrends {
  resources: number[]
  projects: number[]
  pipelines: number[]
  connectors: number[]
  plugins: number[]
  running: number[]
  attention: number[]
  stopped: number[]
  services_failed: number[]
  registry_entries: number[]
  registry_healthy: number[]
  registry_unhealthy: number[]
}

export interface SystemInfo {
  config_path?: string
  config_exists: boolean
  registry_path?: string
  registry_exists: boolean
  socket_path?: string
  socket_exists: boolean
  daemon_running: boolean
  resolved_projects: number
  resolved_resources: number
  resolved_pipelines: number
  resolve_warnings?: string[]
  error?: string
}

export interface SettingsInfo {
  config_path?: string
  config_exists: boolean
  registry_path?: string
  registry_exists: boolean
  install_after_build_default: boolean
  version: number
  has_global_build_config: boolean
  backup_count: number
  resolved_projects: number
  resolved_resources: number
  resolved_pipelines: number
  error?: string
}

export interface ProjectLink {
  kind: string
  target: string
}

export interface ProjectInfo {
  id: string
  name: string
  description?: string
  resource_count: number
  capabilities?: string[]
  links?: ProjectLink[]
}

export interface ResourceInfo {
  id: string
  name: string
  type: string
  project: string
  connector: string
  mode?: string
  supervisor?: string
  run_from?: string
  url?: string
  port?: number
  has_build?: boolean
  status?: string
  operator_stopped?: boolean
  artifact_installed?: boolean
  artifact_stale?: boolean
  recommended_action?: string
  recommended_next_step?: string
  tags?: string[]
}

export interface ResourceRuntimeStatus extends ResourceInfo {
  status: string
  service_name?: string
  artifact_path?: string
  install_root?: string
  artifact_stale_reason?: string
  artifact_source?: string
  artifact_synced_at?: string
  recommended_reason?: string
  launchd_loaded?: boolean
  launchd_state?: string
  launchd_pid?: number
  launchd_last_exit_code?: number | null
  launchd_throttled?: boolean
  launchd_reason?: string
  launchd_diagnosis?: string
  launchd_highlights?: string[]
}

export interface LogLines {
  resource_id?: string
  stream?: string
  content: string
  log_path: string
}

export interface OpResult {
  success: boolean
  service_id: string
  message?: string
  build_output?: string
  install_output?: string
  install_skipped?: boolean
  error?: string
}

export interface PipelineInfo {
  id: string
  name: string
  description?: string
  stage_count: number
}

export interface PipelineRunResult {
  success: boolean
  error?: string
  // The pipeline result as JSON bytes, base64-encoded (a Go []byte on the wire).
  raw?: string
}

export interface RegistryEntry {
  owner: string
  namespace: string
  kind: string
  path: string
  registered_at?: string
  via?: string
  project_id?: string
  project_name?: string
  resource_count: number
  pipeline_count: number
  registry_urn?: string
  shared_identity?: boolean
  health_status?: string
  health_detail?: string
}

export interface RegistryListResponse {
  config_path?: string
  config_exists: boolean
  index_path?: string
  index_exists: boolean
  summary: {
    entries: number
    healthy: number
    unhealthy: number
    shared: number
    local_only: number
    resolve_skips: number
    resolve_warned: number
  }
  resolve_warnings?: string[]
  skipped?: RegistryHealthReport[]
  warned?: RegistryHealthReport[]
  entries: RegistryEntry[]
  error?: string
}

export interface RegistryHealthReport {
  owner: string
  path: string
  status: string
  detail?: string
}

export interface RegistryHealthResponse {
  summary: {
    entries: number
    healthy: number
    unhealthy: number
  }
  reports: RegistryHealthReport[]
  error?: string
}

export interface ConfigValidationFile {
  path: string
  kind: string
  owner?: string
  ok: boolean
  warnings?: string[]
  errors?: string[]
}

export interface ConfigValidationResponse {
  summary: {
    files: number
    valid: number
    invalid: number
    warnings: number
    errors: number
  }
  global?: ConfigValidationFile
  registered: ConfigValidationFile[]
  error?: string
}

export interface ConfigResolveResponse {
  config_path?: string
  index_path?: string
  projects: number
  resources: number
  pipelines: number
  warnings?: string[]
  skipped?: RegistryHealthReport[]
  warned?: RegistryHealthReport[]
  global?: ConfigValidationFile
  error?: string
}

export interface ConfigBackupInfo {
  name: string
  path: string
  size: number
  modified: string
}

export interface ConfigBackupResponse {
  backups: ConfigBackupInfo[]
  error?: string
}

export interface ConfigRestoreResponse {
  success: boolean
  backup_path?: string
  restored_to?: string
  pre_restore_path?: string
  error?: string
}

export interface InfraProviderField {
  name: string
  label: string
  description?: string
}

export interface InfraProviderSecret {
  name: string
  label: string
  description?: string
  present: boolean
}

export interface InfraProvider {
  id: string
  label: string
  fields: InfraProviderField[]
  secrets: InfraProviderSecret[]
  values?: Record<string, string>
}

export interface DeploymentProfile {
  id: string
  name: string
  provider: string
  repo_path: string
  domain?: string
  dns_provider?: string
  production_branch?: string
  git_remote?: string
  git_provider?: string
  git_owner?: string
  git_repo?: string
  vercel_project?: string
  vercel_scope?: string
  cloudflare_zone_id?: string
  namecheap_domain?: string
  preflight_command?: string
  build_command?: string
  deploy_command?: string
}

export interface DeploymentRunStep {
  name: string
  command: string
  success: boolean
  output?: string
  error?: string
}

export interface DeploymentRunResult {
  success: boolean
  profile_id: string
  provider: string
  deployment_url?: string
  git: {
    branch?: string
    commit?: string
    dirty: boolean
    remotes?: string[]
    remote_url?: string
  }
  steps: DeploymentRunStep[]
  error?: string
}

export interface InfraResponse {
  state_path?: string
  providers: InfraProvider[]
  deployments: DeploymentProfile[]
  error?: string
}

// What running a deployment profile will execute, for the confirm step.
// Credentials appear by name in brackets, never by value.
export interface DeploymentPlan {
  profile_id: string
  provider: string
  repo_path: string
  steps: { name: string; command: string }[]
  error?: string
}

export interface ConnectorConfigField {
  name: string
  type: string
  description?: string
  required?: boolean
  default?: unknown
}

export interface ConnectorSecretRequirement {
  name: string
  description?: string
  env?: string
  required?: boolean
}

export interface ConnectorOperation {
  name: string
  description?: string
  examples?: string[]
  input_schema?: Record<string, unknown>
  // The operation contract. requires_ack, destructive and supports_dry are
  // derived from it by the daemon; the console reads them, never infers them.
  effect?: string
  requires_ack?: boolean
  destructive?: boolean
  supports_dry?: boolean
}

export interface ConnectorDefinition {
  id: string
  version: string
  resource_types: string[]
  capabilities: {
    can_create: boolean
    can_destroy: boolean
    can_build: boolean
    can_logs: boolean
    can_health: boolean
  }
  config: {
    fields?: ConnectorConfigField[]
    secrets?: ConnectorSecretRequirement[]
  }
  operations: ConnectorOperation[]
}

export interface ConnectorOperationRequest {
  config?: Record<string, unknown>
  dry_run?: boolean
  acknowledged?: boolean
}

export interface ConnectorOperationResult {
  connector: string
  operation: string
  data: unknown
}

export interface ManagedPluginState {
  id: string
  version: string
  path: string
  loaded: boolean
  // How the plugin was installed: "installed", or "dev" for a development
  // install whose destructive operations are refused. Not a trust level.
  origin?: string
  // Fingerprint of the entrypoint at install. Change detection, not trust;
  // nothing compares it yet.
  entrypoint_sha256?: string
}

export interface PluginHealth {
  id: string
  loaded: boolean
  healthy: boolean
  message?: string
}

export type ResourceAction = 'apply' | 'deploy' | 'reload' | 'stop' | 'sync' | 'remove'

// An approval request, as the daemon's broker holds it (P3).
export interface ApprovalPrincipal {
  kind?: string
  surface?: string
  via?: string
  uid?: number
  client?: string
  session?: string
}

export interface ApprovalInfo {
  id: string
  status: 'pending' | 'approved' | 'denied' | 'expired' | 'consumed' | 'revoked'
  created_at: string
  expires_at: string
  principal: ApprovalPrincipal
  connector: string
  operation: string
  effect?: string
  target: { kind?: string; resource?: string; env?: string; owner?: string; admin?: string; fields?: Record<string, string> }
  args_digest: string
  plan_hash?: string
  rule?: string
  reason?: string
  channel: string
  scope: string
  decision?: { approve: boolean; by: ApprovalPrincipal; at: string; key_fingerprint?: string; reason?: string }
  consumed_at?: string
  revoked_at?: string
}

export interface ApprovalListResponse {
  approvals: ApprovalInfo[] | null
  problems?: string[]
}


// A call's plan, as an approval of it binds it (P3-2), and the hash the
// console's confirm step sends back (P3-3b).
export interface PlanTarget {
  kind?: string
  fields?: Record<string, string>
  resource?: string
  env?: string
  owner?: string
  admin?: string
  tags?: string[]
}

export interface PlanStep {
  name: string
  command: string
  dir?: string
  env?: string[]
}

export interface PlanView {
  v: number
  lane: string
  connector: string
  operation: string
  effect?: string
  target: PlanTarget
  args_digest: string
  preview_kind?: string
  preview?: unknown
  plugin_entrypoint_sha256?: string
  steps?: PlanStep[]
  digests?: Record<string, string>
  source?: { path: string; head: string; dirty: boolean }
  artifact?: string
  state?: string
  actions?: PlanView[]
}

export interface ConnectorPlan {
  plan_hash: string
  computed_by: string
  plan: PlanView
}

// The approval a refusal names (approval_pending / approval_required).
export interface ApprovalRef {
  id: string
  expires_at?: string
  approve_with?: string
  channel?: string
}

// confirmableApproval is the approval of a refusal the operator can meet in
// the console's confirm step: one policy wants confirmed on the call
// (tty_confirm). Anything else, including an out-of-band approval, is
// answered with the approvals page.
export function refusalApproval(err: unknown): ApprovalRef | null {
  if (!(err instanceof ApiError)) return null
  const data = err.data as { approval?: ApprovalRef } | undefined
  return data?.approval ?? null
}

export function confirmableApproval(err: unknown): ApprovalRef | null {
  const ref = refusalApproval(err)
  return ref && ref.channel === 'tty_confirm' ? ref : null
}

// confirmTarget is what the operator types to confirm a plan: the resource,
// else the target's first field, else its kind.
export function confirmTarget(p: PlanView): string {
  if (p.target.resource) return p.target.resource
  const fields = p.target.fields ?? {}
  const keys = Object.keys(fields).sort()
  if (keys.length > 0) return fields[keys[0]]
  return p.target.kind || `${p.connector}.${p.operation}`
}

interface ConfirmArgs {
  approval_id?: string
  confirmed_plan_hash: string
}

export const apiClient = {
  listApprovals: (signal?: AbortSignal) => http.get<ApprovalListResponse>('/api/approvals', { signal }),
  decideApproval: (id: string, token: string, approve: boolean, typed: string, reason: string, assertion?: unknown) =>
    http.post<ApprovalInfo>(`/api/approvals/${encodeURIComponent(id)}/decide`, { approve, typed, reason, assertion } as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  approvalChallenge: (id: string, token: string) =>
    http.post<PasskeyCeremony>(`/api/approvals/${encodeURIComponent(id)}/challenge`, {} as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  getPasskeys: (signal?: AbortSignal) => http.get<PasskeyStatus>('/api/approvals/keys', { signal }),
  passkeyAction: <T>(action: 'register/begin' | 'register/finish' | 'remove/begin' | 'remove/finish', token: string, body: Record<string, unknown>) =>
    http.post<T>(`/api/approvals/keys/${action}`, body as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  revokeApproval: (id: string, token: string, reason: string) =>
    http.post<ApprovalInfo>(`/api/approvals/${encodeURIComponent(id)}/revoke`, { reason } as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  getSession: (signal?: AbortSignal) => http.get<SessionInfo>('/api/session', { signal }),
  logout: (token: string) =>
    http.post<{ success: boolean }>('/api/logout', {} as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  getOverview: (signal?: AbortSignal) => http.get<OverviewInfo>('/api/overview', { signal }),
  getInfra: (signal?: AbortSignal) => http.get<InfraResponse>('/api/infra', { signal }),
  getSettings: (signal?: AbortSignal) => http.get<SettingsInfo>('/api/settings', { signal }),
  getSystem: (signal?: AbortSignal) => http.get<SystemInfo>('/api/system', { signal }),
  listProjects: (signal?: AbortSignal) => http.get<ProjectInfo[]>('/api/projects', { signal }),
  listResources: (signal?: AbortSignal) => http.get<ResourceInfo[]>('/api/resources', { signal }),
  getResource: (id: string, signal?: AbortSignal) => http.get<ResourceRuntimeStatus>(`/api/resources/${encodeURIComponent(id)}`, { signal }),
  getLogs: (id: string, stream: string, lines = 120, signal?: AbortSignal) =>
    http.get<LogLines>(`/api/resources/${encodeURIComponent(id)}/logs`, {
      signal,
      query: { stream, lines },
    }),
  listPipelines: (signal?: AbortSignal) => http.get<PipelineInfo[]>('/api/pipelines', { signal }),
  // acknowledged is true only when the operator confirmed the run.
  runPipeline: (id: string, token: string, acknowledged: boolean) =>
    http.post<PipelineRunResult>(`/api/pipelines/${encodeURIComponent(id)}/run`, { acknowledged } as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  listRegistry: (signal?: AbortSignal) => http.get<RegistryListResponse>('/api/registry', { signal }),
  getRegistryHealth: (signal?: AbortSignal) => http.get<RegistryHealthResponse>('/api/registry/health', { signal }),
  getConfigValidation: (signal?: AbortSignal) => http.get<ConfigValidationResponse>('/api/config/validate', { signal }),
  getConfigResolve: (signal?: AbortSignal) => http.get<ConfigResolveResponse>('/api/config/resolve', { signal }),
  listConfigBackups: (signal?: AbortSignal) => http.get<ConfigBackupResponse>('/api/config/backups', { signal }),
  restoreConfigBackup: (backupPath: string, token: string) =>
    http.post<ConfigRestoreResponse>('/api/config/backups/restore', { backup_path: backupPath }, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  registerConfig: (path: string, token: string) =>
    http.post<{ success: boolean; count: number }>('/api/registry/register', { path }, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  deregisterOwner: (owner: string, token: string) =>
    http.post<{ success: boolean }>('/api/registry/deregister', { owner }, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  listConnectors: (signal?: AbortSignal) => http.get<ConnectorDefinition[]>('/api/connectors', { signal }),
  saveInfraProvider: (id: string, body: { values?: Record<string, string>; secrets?: Record<string, string>; clear_secrets?: string[] }, token: string) =>
    http.post<{ success: boolean }>(`/api/infra/providers/${encodeURIComponent(id)}`, body as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  listDeployments: (signal?: AbortSignal) =>
    http.get<{ deployments: DeploymentProfile[]; state_path?: string }>('/api/deployments', { signal }),
  planDeployment: (id: string) => http.get<DeploymentPlan>(`/api/deployments/${encodeURIComponent(id)}/plan`),
  saveDeployment: (profile: DeploymentProfile, token: string) =>
    http.post<{ success: boolean }>('/api/deployments', profile as unknown as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  deleteDeployment: (id: string, token: string) =>
    http.post<{ success: boolean }>(`/api/deployments/${encodeURIComponent(id)}/delete`, {} as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  // acknowledged is true only when the operator confirmed the plan.
  runDeployment: (id: string, token: string, acknowledged: boolean) =>
    http.post<DeploymentRunResult>(`/api/deployments/${encodeURIComponent(id)}/run`, { acknowledged } as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  runConnectorOperation: (id: string, operation: string, body: ConnectorOperationRequest, token: string) =>
    http.post<ConnectorOperationResult>(`/api/connectors/${encodeURIComponent(id)}/operations/${encodeURIComponent(operation)}`, body as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  listManagedPlugins: (signal?: AbortSignal) => http.get<ManagedPluginState[]>('/api/plugins/connectors', { signal }),
  getManagedPluginHealth: (id: string, signal?: AbortSignal) =>
    http.get<PluginHealth>(`/api/plugins/connectors/${encodeURIComponent(id)}/health`, { signal }),
  loadManagedPlugin: (id: string, token: string) =>
    http.post<ManagedPluginState>(`/api/plugins/connectors/${encodeURIComponent(id)}/load`, {} as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  unloadManagedPlugin: (id: string, token: string) =>
    http.post<ManagedPluginState>(`/api/plugins/connectors/${encodeURIComponent(id)}/unload`, {} as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  // acknowledged is true only when the operator confirmed the action.
  runResourceAction: (id: string, action: ResourceAction, token: string, acknowledged: boolean) =>
    http.post<OpResult>(`/api/resources/${encodeURIComponent(id)}/${action}`, { acknowledged } as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  // The confirm step (P3-3b): each call's plan, and the call confirmed
  // against it on its own route.
  planResourceAction: (id: string, action: ResourceAction, token: string) =>
    http.post<ConnectorPlan>(`/api/resources/${encodeURIComponent(id)}/${action}/plan`, { acknowledged: true } as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  confirmResourceAction: (id: string, action: ResourceAction, token: string, confirm: ConfirmArgs) =>
    http.post<OpResult>(`/api/resources/${encodeURIComponent(id)}/${action}/confirm`, { acknowledged: true, ...confirm } as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  planPipeline: (id: string, token: string) =>
    http.post<ConnectorPlan>(`/api/pipelines/${encodeURIComponent(id)}/run/plan`, { acknowledged: true } as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  confirmPipeline: (id: string, token: string, confirm: ConfirmArgs) =>
    http.post<PipelineRunResult>(`/api/pipelines/${encodeURIComponent(id)}/run/confirm`, { acknowledged: true, ...confirm } as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  planDeploymentRun: (id: string, token: string) =>
    http.post<ConnectorPlan>(`/api/deployments/${encodeURIComponent(id)}/run/plan`, { acknowledged: true } as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  confirmDeploymentRun: (id: string, token: string, confirm: ConfirmArgs) =>
    http.post<DeploymentRunResult>(`/api/deployments/${encodeURIComponent(id)}/run/confirm`, { acknowledged: true, ...confirm } as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  planConnectorOperation: (id: string, operation: string, body: ConnectorOperationRequest, token: string) =>
    http.post<ConnectorPlan>(`/api/connectors/${encodeURIComponent(id)}/operations/${encodeURIComponent(operation)}/plan`, body as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  confirmConnectorOperation: (id: string, operation: string, body: ConnectorOperationRequest, token: string, confirm: ConfirmArgs) =>
    http.post<ConnectorOperationResult>(`/api/connectors/${encodeURIComponent(id)}/operations/${encodeURIComponent(operation)}/confirm`, { ...body, ...confirm } as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
}
