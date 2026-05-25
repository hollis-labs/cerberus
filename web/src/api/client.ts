import { createApiClient, type JsonObject } from '@hollis-labs/sysop-ui/api'

const http = createApiClient({ baseUrl: '' })

export interface SessionInfo {
  action_token: string
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
  error?: string
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

export interface ProjectInfo {
  id: string
  name: string
  description?: string
  resource_count: number
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
  raw?: number[]
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
  }
  resolve_warnings?: string[]
  skipped?: RegistryHealthReport[]
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

export interface MigrationValidationError {
  owner: string
  field: string
  message: string
}

export interface ConfigMigrateEntry {
  owner: string
  project_id: string
  project_name?: string
  resource_count: number
  pipeline_count: number
  destination: string
}

export interface ConfigMigratePreviewResponse {
  config_path?: string
  projects_dir?: string
  backup_path?: string
  project_count: number
  total_resources: number
  warnings?: string[]
  validation_errors?: MigrationValidationError[]
  entries: ConfigMigrateEntry[]
  error?: string
}

export interface ConfigMigrateResponse {
  success: boolean
  projects_dir?: string
  backup_path?: string
  warnings?: string[]
  written_paths?: string[]
  registered_owners?: string[]
  total_resources: number
  validation_errors?: MigrationValidationError[]
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
  suggested?: boolean
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
  suggestions?: DeploymentProfile[]
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

export interface PluginTrustOptions {
  dev_mode?: boolean
  catalog_signed?: boolean
  archive_signed?: boolean
  archive_sha256?: string
}

export interface ManagedPluginState {
  id: string
  version: string
  path: string
  loaded: boolean
  trust_tier?: string
}

export interface PluginHealth {
  id: string
  loaded: boolean
  healthy: boolean
  message?: string
}

export type ResourceAction = 'apply' | 'deploy' | 'reload' | 'stop' | 'sync' | 'remove'

export const apiClient = {
  getSession: (signal?: AbortSignal) => http.get<SessionInfo>('/api/session', { signal }),
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
  runPipeline: (id: string, token: string) =>
    http.post<PipelineRunResult>(`/api/pipelines/${encodeURIComponent(id)}/run`, {} as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  listRegistry: (signal?: AbortSignal) => http.get<RegistryListResponse>('/api/registry', { signal }),
  getRegistryHealth: (signal?: AbortSignal) => http.get<RegistryHealthResponse>('/api/registry/health', { signal }),
  getConfigValidation: (signal?: AbortSignal) => http.get<ConfigValidationResponse>('/api/config/validate', { signal }),
  getConfigResolve: (signal?: AbortSignal) => http.get<ConfigResolveResponse>('/api/config/resolve', { signal }),
  getConfigMigratePreview: (signal?: AbortSignal) => http.get<ConfigMigratePreviewResponse>('/api/config/migrate/preview', { signal }),
  runConfigMigrate: (token: string) =>
    http.post<ConfigMigrateResponse>('/api/config/migrate', {} as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
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
    http.get<{ deployments: DeploymentProfile[]; suggestions?: DeploymentProfile[]; state_path?: string }>('/api/deployments', { signal }),
  saveDeployment: (profile: DeploymentProfile, token: string) =>
    http.post<{ success: boolean }>('/api/deployments', profile as unknown as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  deleteDeployment: (id: string, token: string) =>
    http.post<{ success: boolean }>(`/api/deployments/${encodeURIComponent(id)}/delete`, {} as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  runDeployment: (id: string, token: string) =>
    http.post<DeploymentRunResult>(`/api/deployments/${encodeURIComponent(id)}/run`, {} as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  runConnectorOperation: (id: string, operation: string, body: ConnectorOperationRequest, token: string) =>
    http.post<ConnectorOperationResult>(`/api/connectors/${encodeURIComponent(id)}/operations/${encodeURIComponent(operation)}`, body as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  listManagedPlugins: (signal?: AbortSignal) => http.get<ManagedPluginState[]>('/api/plugins/connectors', { signal }),
  getManagedPluginHealth: (id: string, signal?: AbortSignal) =>
    http.get<PluginHealth>(`/api/plugins/connectors/${encodeURIComponent(id)}/health`, { signal }),
  checkPluginHealth: (pluginDir: string, trust: PluginTrustOptions, token: string) =>
    http.post<PluginHealth>('/api/plugins/connectors/health', { plugin_dir: pluginDir, trust } as unknown as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  installManagedPlugin: (pluginDir: string, trust: PluginTrustOptions, token: string) =>
    http.post<ManagedPluginState>('/api/plugins/connectors/install', { plugin_dir: pluginDir, trust } as unknown as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  loadManagedPlugin: (id: string, token: string) =>
    http.post<ManagedPluginState>(`/api/plugins/connectors/${encodeURIComponent(id)}/load`, {} as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  unloadManagedPlugin: (id: string, token: string) =>
    http.post<ManagedPluginState>(`/api/plugins/connectors/${encodeURIComponent(id)}/unload`, {} as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
  runResourceAction: (id: string, action: ResourceAction, token: string) =>
    http.post<OpResult>(`/api/resources/${encodeURIComponent(id)}/${action}`, {} as JsonObject, {
      headers: { 'X-Cerberus-Web-Token': token },
    }),
}
