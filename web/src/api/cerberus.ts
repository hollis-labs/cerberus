import { request } from "./request"
import type {
  ApprovalInfo,
  ApprovalListResponse,
  BrakeState,
  ConfigBackupResponse,
  ConfigResolveResponse,
  ConfigRestoreResponse,
  ConfigValidationResponse,
  ConfirmArgs,
  ConnectorDefinition,
  ConnectorOperationRequest,
  ConnectorOperationResult,
  ConnectorPlan,
  ConsoleStatus,
  CredentialSaveResult,
  CredentialsResponse,
  EnrollBegin,
  LogLines,
  ManagedPluginState,
  OpResult,
  OverviewInfo,
  PasskeyCeremony,
  PasskeyStatus,
  PipelineInfo,
  PipelineRunResult,
  PluginHealth,
  ProjectInfo,
  RegistryHealthResponse,
  RegistryListResponse,
  ResourceAction,
  ResourceInfo,
  ResourceRuntimeStatus,
  SettingsInfo,
  SystemInfo,
} from "./types"

// Typed stubs for every route in docs/substrate-port/console-api-contract.md. The comment on each
// group is the scope the route requires. Pages wire these in slice 11b; the passkey ceremonies
// (approve, enroll, remove) in slice 11c.

const id = encodeURIComponent

/** A gated write: the call, its plan, the call confirmed against the plan, and the retry under an approval. */
export interface GatedWrite<T> {
  send: () => Promise<T>
  plan: () => Promise<ConnectorPlan>
  confirm: (c: ConfirmArgs) => Promise<T>
  retry: (approvalID: string) => Promise<T>
}

function gated<T>(path: string, body: Record<string, unknown>): GatedWrite<T> {
  return {
    send: () => request<T>(path, "POST", body),
    plan: () => request<ConnectorPlan>(`${path}/plan`, "POST", body),
    confirm: (c) => request<T>(`${path}/confirm`, "POST", { ...body, ...c }),
    retry: (approvalID) => request<T>(path, "POST", { ...body, approval_id: approvalID }),
  }
}

export const cerberus = {
  // cerberus.read
  consoleStatus: () => request<ConsoleStatus>("console/status"),
  overview: () => request<OverviewInfo>("overview"),
  system: () => request<SystemInfo>("system"),
  settings: () => request<SettingsInfo>("settings"),
  listProjects: () => request<ProjectInfo[]>("projects"),
  listResources: () => request<ResourceInfo[]>("resources"),
  getResource: (rid: string) => request<ResourceRuntimeStatus>(`resources/${id(rid)}`),
  getLogs: (rid: string, stream: string, lines = 120) =>
    request<LogLines>(`resources/${id(rid)}/logs`, "GET", undefined, { stream, lines }),
  listPipelines: () => request<PipelineInfo[]>("pipelines"),
  listRegistry: () => request<RegistryListResponse>("registry"),
  registryHealth: () => request<RegistryHealthResponse>("registry/health"),
  configValidate: () => request<ConfigValidationResponse>("config/validate"),
  configResolve: () => request<ConfigResolveResponse>("config/resolve"),
  listConfigBackups: () => request<ConfigBackupResponse>("config/backups"),
  listCredentials: () => request<CredentialsResponse>("credentials"),
  listConnectors: () => request<ConnectorDefinition[]>("connectors"),
  listPlugins: () => request<ManagedPluginState[]>("plugins/connectors"),
  pluginHealth: (pid: string) => request<PluginHealth>(`plugins/connectors/${id(pid)}/health`),
  listApprovals: () => request<ApprovalListResponse>("approvals"),
  getApproval: (aid: string) => request<ApprovalInfo>(`approvals/${id(aid)}`),
  passkeys: () => request<PasskeyStatus>("approvals/keys"),

  // cerberus.operate (resource remove: cerberus.admin)
  resourceAction: (rid: string, action: ResourceAction, acknowledged: boolean) =>
    gated<OpResult>(`resources/${id(rid)}/${action}`, { acknowledged }),
  runPipeline: (pid: string, acknowledged: boolean) =>
    gated<PipelineRunResult>(`pipelines/${id(pid)}/run`, { acknowledged }),
  engageLockdown: (reason: string) =>
    request<{ state: BrakeState }>("brakes/lockdown", "POST", { reason }),
  liftLockdown: (approvalID?: string) =>
    request<{ state: BrakeState }>("brakes/lockdown/lift", "POST", {
      approval_id: approvalID ?? "",
    }),
  liftFreeze: (fid: string, approvalID?: string) =>
    request<{ state: BrakeState }>(`brakes/freeze/${id(fid)}/lift`, "POST", {
      approval_id: approvalID ?? "",
    }),
  resetSuspension: (sid: string, typed: string) =>
    request<{ state: BrakeState }>(`brakes/suspensions/${id(sid)}/reset`, "POST", { typed }),

  // cerberus.admin
  connectorOperation: (cid: string, op: string, body: ConnectorOperationRequest) =>
    gated<ConnectorOperationResult>(
      `connectors/${id(cid)}/operations/${id(op)}`,
      body as Record<string, unknown>,
    ),
  saveCredentials: (
    pid: string,
    body: { secrets?: Record<string, string>; clear_secrets?: string[] },
  ) => gated<CredentialSaveResult>(`credentials/${id(pid)}`, body),
  loadPlugin: (pid: string) => gated<ManagedPluginState>(`plugins/connectors/${id(pid)}/load`, {}),
  unloadPlugin: (pid: string) =>
    gated<ManagedPluginState>(`plugins/connectors/${id(pid)}/unload`, {}),
  registerConfig: (path: string) =>
    gated<{ success: boolean; count: number }>("registry/register", { path }),
  deregisterOwner: (owner: string) => gated<{ success: boolean }>("registry/deregister", { owner }),
  restoreConfigBackup: (backupPath: string) =>
    gated<ConfigRestoreResponse>("config/backups/restore", { backup_path: backupPath }),

  // cerberus.approve (a console session only; never an API key or a local-trust caller)
  approvalChallenge: (aid: string) =>
    request<PasskeyCeremony>(`approvals/${id(aid)}/challenge`, "POST", {}),
  decideApproval: (
    aid: string,
    body: { approve: boolean; typed: string; reason: string; assertion?: unknown },
  ) => request<ApprovalInfo>(`approvals/${id(aid)}/decide`, "POST", body),
  revokeApproval: (aid: string, reason: string) =>
    request<ApprovalInfo>(`approvals/${id(aid)}/revoke`, "POST", { reason }),
  // The enroll token comes from the host-side `cerberus approvals enroll` (host authentication).
  enrollBegin: (enrollToken: string, label: string) =>
    request<EnrollBegin>("approvals/keys/register/begin", "POST", { token: enrollToken, label }),
  enrollFinish: (body: { ceremony: string; credential: unknown; authorize?: unknown }) =>
    request<{ fingerprint: string }>("approvals/keys/register/finish", "POST", body),
  removeKeyBegin: (fingerprint: string) =>
    request<PasskeyCeremony>("approvals/keys/remove/begin", "POST", { fingerprint }),
  removeKeyFinish: (body: { ceremony: string; credential: unknown }) =>
    request<PasskeyStatus>("approvals/keys/remove/finish", "POST", body),
}
