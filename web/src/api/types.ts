// Response and request shapes for the console API (docs/substrate-port/console-api-contract.md).
// Carried over from the old console client (main: web/src/api/client.ts) so the backend keeps its
// bodies; what changed is listed in the contract doc. Request failures are the flat ApiError.

// ConsoleStatus feeds the header badges and the brakes banner (GET /api/console/status). It
// replaces the badge fields of the old GET /api/session, which also carried the action token.
export interface ConsoleStatus {
  posture?: PostureInfo
  passkeys?: PasskeysAlert | null
  break_glass?: BreakGlassAlert | null
  enforcement?: EnforcementAlert | null
  brakes?: BrakeState | null
}

// BrakeState is the emergency brake: a lockdown, freezes and suspensions.
export interface BrakeEngagement {
  id: string
  engaged_at: string
  by: { kind: string; via: string }
  reason?: string
}
export interface BrakeState {
  lockdown?: BrakeEngagement | null
  freezes?: (BrakeEngagement & { scope: string })[] | null
  suspensions?: BrakeSuspension[] | null
}

// BrakeSuspension is an agent session the circuit breaker stopped (P5-c).
export interface BrakeSuspension {
  id: string
  key: string
  principal: { kind: string; via: string; client?: string; session?: string }
  tripped_at: string
  denials: number
  window: string
}

// EnforcementAlert is the header's enforcement badge (P3-7): what is
// enforced, and a snapshot mismatch's path, loudly.
export interface EnforcementAlert {
  summary: string
  enforced: boolean
  mismatch?: string
}

// BreakGlassAlert is the header's break-glass badge (P3-5b).
export interface BreakGlassAlert {
  recent: number
  open: number
  summary: string
}

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
  enrollment?: { method: string; available: boolean; next_step: string }
  state: "not_set_up" | "ok" | "cooldown" | string
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

// A connector's declared secrets, as the credential editor lists them: names,
// kinds and whether a value is stored, never a value.
export interface CredentialSecret {
  name: string
  description: string
  env?: string
  required?: boolean
  kind: "credential" | "name" | "path" | string
  present: boolean
  // Where the value stands, found without resolving it: a reference is
  // resolved only on use, never to draw the page.
  stored: "missing" | "stored" | "reference" | string
}

export interface CredentialProvider {
  id: string
  version?: string
  secrets: CredentialSecret[]
}

export interface CredentialsResponse {
  providers: CredentialProvider[]
  error?: string
}

export interface CredentialSaveResult {
  success: boolean
  plugin_reloaded?: boolean
  plugin_reload_error?: string
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
  // The out-of-band approval a retry runs under.
  approval_id?: string
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

export type ResourceAction = "apply" | "deploy" | "reload" | "stop" | "sync" | "remove"

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
  status: "pending" | "approved" | "denied" | "expired" | "consumed" | "revoked"
  created_at: string
  expires_at: string
  principal: ApprovalPrincipal
  connector: string
  operation: string
  effect?: string
  target: {
    kind?: string
    resource?: string
    env?: string
    owner?: string
    admin?: string
    fields?: Record<string, string>
  }
  args_digest: string
  plan_hash?: string
  // What the approver is shown (H3): the plan the approval binds to and the
  // call's arguments, redacted by the daemon. untrusted names, as JSON
  // pointers, the parts the requester wrote rather than Cerberus.
  shown?: {
    plan?: PlanView
    arguments?: Record<string, unknown>
    untrusted?: string[]
    flagged?: string[]
    truncated?: boolean
  }
  rule?: string
  reason?: string
  channel: string
  scope: string
  decision?: {
    approve: boolean
    by: ApprovalPrincipal
    at: string
    key_fingerprint?: string
    reason?: string
    same_surface?: boolean
  }
  consumed_at?: string
  revoked_at?: string
  // A grant's uses (scope session or window, P3-5).
  uses?: number
  // A break-glass use (P3-5b): why, what was typed, and its follow-up.
  break_glass?: { reason: string; typed: string; acked_at?: string }
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

/** The confirm step's arguments: the approval it meets and the plan hash the operator saw. */
export interface ConfirmArgs {
  approval_id?: string
  confirmed_plan_hash: string
}
